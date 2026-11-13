// Package manifest reads and atomically writes authoritative workspace metadata.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/usingcoding/workdrive/internal/domain"
)

const FileName = "workspace.toml"

// Path returns the manifest location inside a workspace root.
func Path(root string) string { return filepath.Join(root, ".workdrive", FileName) }

// Read loads an existing workspace. Unknown fields are rejected so rewriting an
// unrecognized future schema cannot silently discard ownership information.
func Read(root string) (*domain.Workspace, error) {
	path := Path(root)
	if err := checkMetadataPaths(root); err != nil {
		return nil, fmt.Errorf("read workspace manifest %s: %w", path, err)
	}
	var w domain.Workspace
	metadata, err := toml.DecodeFile(path, &w)
	if err != nil {
		return nil, fmt.Errorf("read workspace manifest %s: %w", path, err)
	}
	if unknown := metadata.Undecoded(); len(unknown) != 0 {
		return nil, fmt.Errorf("unknown workspace manifest field %s", unknown[0])
	}
	if err := validate(&w); err != nil {
		return nil, fmt.Errorf("invalid workspace manifest %s: %w", path, err)
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != w.Path {
		return nil, fmt.Errorf("manifest path %q does not match workspace %q", w.Path, root)
	}
	return &w, nil
}

// Write atomically replaces the manifest, syncing the file and its directory.
// The caller must hold the per-workspace advisory lock. It does not index SQLite.
func Write(w *domain.Workspace) error {
	if err := validate(w); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(w.Path)
	if err != nil {
		return fmt.Errorf("stat workspace root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace root must be a real directory: %s", w.Path)
	}
	dir := filepath.Join(w.Path, ".workdrive")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create metadata directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("metadata path must be a real directory: %s", dir)
	}
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(w); err != nil {
		return fmt.Errorf("encode workspace manifest: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".workspace-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data.Bytes()); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), Path(w.Path)); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

func checkMetadataPaths(root string) error {
	for _, path := range []string{root, filepath.Join(root, ".workdrive")} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace metadata directory is not a real directory: %s", path)
		}
	}
	info, err := os.Lstat(Path(root))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("workspace manifest is not a regular file: %s", Path(root))
	}
	return nil
}

func validate(w *domain.Workspace) error {
	if w == nil {
		return errors.New("nil workspace")
	}
	if w.SchemaVersion != domain.SchemaVersion {
		return fmt.Errorf("unsupported manifest schema version %d", w.SchemaVersion)
	}
	if err := domain.ValidateKey(w.Key); err != nil {
		return err
	}
	if w.ID == "" || w.Name == "" || w.Profile == "" {
		return errors.New("workspace identity, name and profile are required")
	}
	switch w.State {
	case domain.StateCreating, domain.StateActive, domain.StateParked, domain.StateCleaning, domain.StateAttentionRequired, domain.StateRemoved:
	default:
		return fmt.Errorf("unknown lifecycle state %q", w.State)
	}
	if !filepath.IsAbs(w.Path) || filepath.Clean(w.Path) != w.Path {
		return fmt.Errorf("workspace path must be a cleaned absolute path: %q", w.Path)
	}
	if w.CreatedAt.IsZero() || w.UpdatedAt.IsZero() || w.UpdatedAt.Before(w.CreatedAt) {
		return errors.New("workspace timestamps must be valid")
	}
	ids := map[string]bool{}
	for _, r := range w.Resources {
		if r.ID == "" || r.Type == "" || r.Driver == "" || ids[r.ID] {
			return fmt.Errorf("invalid or duplicate resource identity %q", r.ID)
		}
		switch r.Ownership {
		case domain.OwnershipOwned, domain.OwnershipLinked, domain.OwnershipAdopted:
		default:
			return fmt.Errorf("unknown ownership %q for resource %q", r.Ownership, r.ID)
		}
		ids[r.ID] = true
		if r.Path != "" && (!filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path) {
			return fmt.Errorf("resource %q path must be absolute and cleaned", r.ID)
		}
		if r.Source != "" && (!filepath.IsAbs(r.Source) || filepath.Clean(r.Source) != r.Source) {
			return fmt.Errorf("resource %q source must be absolute and cleaned", r.ID)
		}
	}
	return nil
}
