// Package registry provides a rebuildable SQLite index of workspace manifests.
// Manifests, not registry rows, authorize workspace mutations or deletion.
package registry

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/manifest"
	_ "modernc.org/sqlite"
)

// Entry holds only non-secret list data; full resource details live in manifests.
type Entry struct {
	ID            string                `json:"id"`
	Key           string                `json:"key"`
	Name          string                `json:"name"`
	Path          string                `json:"path"`
	Profile       string                `json:"profile"`
	State         domain.LifecycleState `json:"state"`
	ActualStatus  domain.ActualHealth   `json:"actual_status"`
	WorktreeCount int                   `json:"worktree_count"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

type Registry struct{ db *sql.DB }

// Open initializes a private database at an absolute path.
func Open(path string) (*Registry, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("registry path must be absolute: %q", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000;
		CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT NOT NULL UNIQUE,
			key TEXT NOT NULL PRIMARY KEY COLLATE BINARY,
			name TEXT NOT NULL,
			path TEXT NOT NULL UNIQUE,
			profile TEXT NOT NULL,
			state TEXT NOT NULL,
			actual_status TEXT NOT NULL,
			worktree_count INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize registry: %w", err)
	}
	return &Registry{db: db}, nil
}

// OpenReadOnly opens only an existing ordinary database, without creating
// directories or schema. A missing database returns (nil, nil). The registry
// uses SQLite's rollback journal; read-only mode retains normal locking against
// concurrent writers without creating a journal of its own.
func OpenReadOnly(path string) (*Registry, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("registry path must be absolute: %q", path)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("registry is not an ordinary file: %q", path)
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(50)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &Registry{db: db}, nil
}

func (r *Registry) Close() error { return r.db.Close() }

// Upsert refreshes an index entry, refusing to replace a different workspace
// with the same case-sensitive key, identity or path.
func (r *Registry) Upsert(w *domain.Workspace) error {
	if w == nil || w.ID == "" || w.Key == "" || !filepath.IsAbs(w.Path) {
		return errors.New("workspace identity, key and absolute path required")
	}
	entry := fromWorkspace(w)
	result, err := r.db.Exec(`INSERT INTO workspaces
		(id,key,name,path,profile,state,actual_status,worktree_count,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET
		name=excluded.name,profile=excluded.profile,state=excluded.state,
		actual_status=excluded.actual_status,worktree_count=excluded.worktree_count,
		updated_at=excluded.updated_at
		WHERE workspaces.id=excluded.id AND workspaces.path=excluded.path`,
		entry.ID, entry.Key, entry.Name, entry.Path, entry.Profile, entry.State,
		entry.ActualStatus, entry.WorktreeCount, entry.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("index workspace %q (conflicting identity or path?): %w", w.Key, err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("workspace key %q belongs to a different identity or path", w.Key)
	}
	return nil
}

// Get returns (nil, nil) when a key is absent. Verify the manifest separately
// before relying on an entry for any side-effecting operation.
func (r *Registry) Get(key string) (*Entry, error) {
	row := r.db.QueryRow(`SELECT id,key,name,path,profile,state,actual_status,worktree_count,updated_at FROM workspaces WHERE key=?`, key)
	entry, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return entry, err
}

// List returns cached entries in case-sensitive key order.
func (r *Registry) List() ([]Entry, error) {
	rows, err := r.db.Query(`SELECT id,key,name,path,profile,state,actual_status,worktree_count,updated_at FROM workspaces ORDER BY key COLLATE BINARY`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		entry, err := scan(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry)
	}
	return entries, rows.Err()
}

// Delete removes a cache entry only; it must be the last step of cleanup.
func (r *Registry) Delete(key string) error {
	_, err := r.db.Exec(`DELETE FROM workspaces WHERE key=?`, key)
	return err
}

// Rebuild scans configured roots without following symlinked directories. It
// replaces the index in a transaction only after every manifest validates and
// duplicate keys/identities/paths have been rejected. No workspace is modified.
func (r *Registry) Rebuild(roots []string) error {
	entries := []Entry{}
	keys, ids, paths := map[string]bool{}, map[string]bool{}, map[string]bool{}
	seenRoots := map[string]bool{}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("workspace scan root must be absolute: %q", root)
		}
		root = filepath.Clean(root)
		if seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !d.IsDir() {
				return nil
			}
			if d.Name() == ".workdrive" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(manifest.Path(path)); err == nil {
				w, err := manifest.Read(path)
				if err != nil {
					return err
				}
				if keys[w.Key] || ids[w.ID] || paths[w.Path] {
					return fmt.Errorf("duplicate workspace identity, key or path in %s", path)
				}
				keys[w.Key], ids[w.ID], paths[w.Path] = true, true, true
				entries = append(entries, fromWorkspace(w))
				return filepath.SkipDir
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("scan workspace root %s: %w", root, err)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM workspaces`); err != nil {
		return err
	}
	for _, e := range entries {
		_, err := tx.Exec(`INSERT INTO workspaces
			(id,key,name,path,profile,state,actual_status,worktree_count,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, e.ID, e.Key, e.Name, e.Path, e.Profile,
			e.State, e.ActualStatus, e.WorktreeCount, e.UpdatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("reindex workspace %q: %w", e.Key, err)
		}
	}
	return tx.Commit()
}

func fromWorkspace(w *domain.Workspace) Entry {
	count := 0
	for _, resource := range w.Resources {
		if resource.Type == "worktree" {
			count++
		}
	}
	return Entry{ID: w.ID, Key: w.Key, Name: w.Name, Path: w.Path,
		Profile: w.Profile, State: w.State, ActualStatus: w.ActualStatus,
		WorktreeCount: count, UpdatedAt: w.UpdatedAt}
}

type scanner interface{ Scan(dest ...any) error }

func scan(s scanner) (*Entry, error) {
	var e Entry
	var updatedAt string
	if err := s.Scan(&e.ID, &e.Key, &e.Name, &e.Path, &e.Profile, &e.State,
		&e.ActualStatus, &e.WorktreeCount, &updatedAt); err != nil {
		return nil, err
	}
	var err error
	e.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("invalid cached timestamp: %w", err)
	}
	return &e, nil
}
