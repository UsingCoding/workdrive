package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/manifest"
)

type NavigationRequest struct {
	Key, Repository          string
	ExpectedID, ExpectedPath string
}

type Navigation struct {
	Workspace *domain.Workspace
	Path      string
	Warnings  []string
}

// ResolveResource shares the selector vocabulary of navigation and removal.
func ResolveResource(w *domain.Workspace, selector string) (domain.Resource, error) {
	var matches []domain.Resource
	for _, r := range w.Resources {
		if selector == r.ID || selector == r.Source || selector == r.Path || selector == filepath.Base(r.Source) {
			matches = append(matches, r)
		}
	}
	if len(matches) != 1 {
		return domain.Resource{}, fmt.Errorf("repository selector %q matches %d worktrees; use its absolute source path or ID", selector, len(matches))
	}
	return matches[0], nil
}

func ordinaryDirectory(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: path is not an ordinary directory: %s", ErrConflict, path)
	}
	return nil
}

func (s *Service) validateNavigationRoot(w *domain.Workspace) error {
	cfg, err := s.profileFor(w)
	if err != nil {
		return err
	}
	if !sameRoot(w.Path, cfg.Workspace.Root) {
		return fmt.Errorf("%w: workspace outside configured root", ErrConflict)
	}
	return ordinaryDirectory(w.Path)
}

// WorkspaceAt observes actual cwd, never lifecycle state, and never imports an
// unindexed manifest. An unrelated or invalid ancestor is not workspace history.
func (s *Service) WorkspaceAt(cwd string) (*domain.Workspace, error) {
	path, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, err
	}
	for {
		if w, err := manifest.Read(path); err == nil {
			registered, err := s.Find(w.Key)
			if err == nil && registered.ID == w.ID && registered.Path == w.Path && s.validateNavigationRoot(registered) == nil {
				return registered, nil
			}
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil, nil
		}
		path = parent
	}
}
