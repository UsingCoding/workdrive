package app

import (
	"fmt"
	"path/filepath"

	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/manifest"
	"github.com/usingcoding/workdrive/internal/registry"
)

// Query is a read-only view of indexed, authoritative manifests. It never opens
// the lifecycle service, scans workspace roots, refreshes discovery or repairs
// state. Missing state is an empty view, not a reason to initialize it.
type Query struct{ index *registry.Registry }

func OpenQuery(stateDir string) (*Query, error) {
	index, err := registry.OpenReadOnly(filepath.Join(stateDir, "registry.db"))
	if err != nil {
		return nil, err
	}
	return &Query{index: index}, nil
}

func (q *Query) Close() error {
	if q.index != nil {
		return q.index.Close()
	}
	return nil
}

func (q *Query) Find(key string) (*domain.Workspace, error) {
	if err := domain.ValidateKey(key); err != nil {
		return nil, err
	}
	if q.index == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	entry, err := q.index.Get(key)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return validateIndexedManifest(entry)
}

// List skips stale/conflicting entries and does not discover unindexed paths.
func (q *Query) List() ([]*domain.Workspace, error) {
	if q.index == nil {
		return nil, nil
	}
	entries, err := q.index.List()
	if err != nil {
		return nil, err
	}
	var workspaces []*domain.Workspace
	for i := range entries {
		w, err := validateIndexedManifest(&entries[i])
		if err == nil {
			workspaces = append(workspaces, w)
		}
	}
	return workspaces, nil
}

// validateIndexedManifest is shared with service reads: an index row alone is
// never authority, nor may a reused key silently adopt another identity. It
// deliberately does not require the current profile root: after a root or
// profile change, inspect/resume/cleanup must still reach existing workspaces.
// Navigation applies its own root check.
func validateIndexedManifest(entry *registry.Entry) (*domain.Workspace, error) {
	w, err := manifest.Read(entry.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest for %s: %v", ErrInterrupted, entry.Key, err)
	}
	if w.ID != entry.ID || w.Key != entry.Key || w.Path != entry.Path {
		return nil, fmt.Errorf("%w: registry and manifest disagree for %s", ErrConflict, entry.Key)
	}
	return w, nil
}
