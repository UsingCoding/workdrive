package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/lifecycle"
	"github.com/usingcoding/workdrive/internal/manifest"
)

func (s *Service) Create(ctx context.Context, plan Plan) (*domain.Workspace, error) {
	if plan.Kind != "create" || plan.Workspace == nil {
		return nil, fmt.Errorf("invalid creation plan")
	}
	if !plan.Safe() {
		return nil, Blockers(plan.Blockers)
	}
	lock, err := s.lock(plan.Workspace.Key)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	fresh, err := s.PlanNew(NewRequest{Key: plan.Workspace.Key, Name: plan.Workspace.Name, Path: plan.Workspace.Path, Profile: plan.Workspace.Profile, Repos: plan.Add})
	if err != nil {
		return nil, err
	}
	if !fresh.Safe() {
		return nil, Blockers(fresh.Blockers)
	}
	w := fresh.Workspace
	steps := make([]lifecycle.Step, 0, len(w.Resources)+2)
	steps = append(steps, lifecycle.Step{ID: "manifest", Description: "Create workspace metadata"})
	for _, r := range w.Resources {
		steps = append(steps, lifecycle.Step{ID: r.ID, Description: "Create worktree " + r.Path})
	}
	steps = append(steps, lifecycle.Step{ID: "active", Description: "Mark workspace active"})
	op, err := s.Journal.PlanWorkspace(w, "create", steps)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(w.Path, 0700); err != nil {
		s.Journal.RecordError(op.ID, err)
		return nil, err
	}
	if err := manifest.Write(w); err != nil {
		s.Journal.RecordError(op.ID, err)
		return nil, err
	}
	if err := s.Registry.Upsert(w); err != nil {
		return w, s.fail(w, op.ID, err)
	}
	if _, err := s.Journal.Complete(op.ID, "manifest"); err != nil {
		return w, s.fail(w, op.ID, err)
	}
	for _, r := range w.Resources {
		if err := s.Adapter.Create(ctx, request(r)); err != nil {
			return w, s.fail(w, op.ID, err)
		}
		if _, err := s.Journal.Complete(op.ID, r.ID); err != nil {
			return w, s.fail(w, op.ID, err)
		}
	}
	if err := w.Transition(domain.StateActive, time.Now().UTC()); err != nil {
		return w, s.fail(w, op.ID, err)
	}
	w.ActualStatus = domain.HealthHealthy
	for i := range w.Resources {
		w.Resources[i].ActualStatus = domain.HealthHealthy
	}
	if err := manifest.Write(w); err != nil {
		return w, s.fail(w, op.ID, err)
	}
	if err := s.Registry.Upsert(w); err != nil {
		return w, s.fail(w, op.ID, err)
	}
	if _, err := s.Journal.Complete(op.ID, "active"); err != nil {
		return w, err
	}
	return w, nil
}

func (s *Service) fail(w *domain.Workspace, opID string, cause error) error {
	w.State = domain.StateAttentionRequired
	w.ActualStatus = domain.HealthError
	w.UpdatedAt = time.Now().UTC()
	writeErr := manifest.Write(w)
	if writeErr == nil {
		writeErr = s.Registry.Upsert(w)
	}
	_, journalErr := s.Journal.RecordError(opID, cause)
	return errors.Join(fmt.Errorf("%w: %v (operation %s; inspect and resume)", ErrInterrupted, cause, opID), writeErr, journalErr)
}

func (s *Service) Park(key string) (*domain.Workspace, error) {
	lock, err := s.lock(key)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	w, err := s.Find(key)
	if err != nil {
		return nil, err
	}
	if w.State == domain.StateParked {
		return w, nil
	}
	if w.State != domain.StateActive {
		return nil, fmt.Errorf("workspace %s needs recovery before parking", key)
	}
	op, err := s.Journal.Plan(w.ID, "park", []lifecycle.Step{{ID: "state", Description: "Mark workspace parked"}})
	if err != nil {
		return nil, err
	}
	if err := w.Transition(domain.StateParked, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := manifest.Write(w); err != nil {
		s.Journal.RecordError(op.ID, err)
		return nil, err
	}
	if err := s.Registry.Upsert(w); err != nil {
		return nil, s.fail(w, op.ID, err)
	}
	_, err = s.Journal.Complete(op.ID, "state")
	if err != nil {
		return w, s.fail(w, op.ID, err)
	}
	return w, nil
}

func (s *Service) Switch(key string) (*domain.Workspace, []string, error) {
	lock, err := s.lock(key)
	if err != nil {
		return nil, nil, err
	}
	defer lock.Unlock()
	w, err := s.Find(key)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := s.profileFor(w)
	if err != nil {
		return nil, nil, err
	}
	if !sameRoot(w.Path, cfg.Workspace.Root) {
		return nil, nil, fmt.Errorf("%w: workspace outside configured root", ErrConflict)
	}
	st, err := os.Lstat(w.Path)
	if err != nil {
		return nil, nil, err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%w: workspace path is not an ordinary directory", ErrConflict)
	}
	if w.State == domain.StateParked {
		op, err := s.Journal.Plan(w.ID, "switch", []lifecycle.Step{{ID: "state", Description: "Mark workspace active"}})
		if err != nil {
			return nil, nil, err
		}
		if err := w.Transition(domain.StateActive, time.Now().UTC()); err != nil {
			return nil, nil, err
		}
		if err := manifest.Write(w); err != nil {
			s.Journal.RecordError(op.ID, err)
			return nil, nil, err
		}
		if err := s.Registry.Upsert(w); err != nil {
			return nil, nil, s.fail(w, op.ID, err)
		}
		if _, err := s.Journal.Complete(op.ID, "state"); err != nil {
			return nil, nil, s.fail(w, op.ID, err)
		}
	} else if w.State != domain.StateActive {
		return nil, nil, fmt.Errorf("%w: workspace state %s needs recovery", ErrInterrupted, w.State)
	}
	var warnings []string
	for _, r := range w.Resources {
		if _, err := os.Lstat(r.Path); err != nil {
			warnings = append(warnings, "missing worktree: "+r.Path)
		}
	}
	return w, warnings, nil
}

func (s *Service) PlanAdd(ctx context.Context, key string, choices []RepositoryChoice) (Plan, error) {
	w, err := s.Find(key)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Kind: "add", Workspace: w, Add: choices}
	if w.State != domain.StateActive && w.State != domain.StateParked {
		plan.Blockers = append(plan.Blockers, "workspace needs recovery")
	}
	cfg, err := s.profileFor(w)
	if err != nil {
		return Plan{}, err
	}
	seen := map[string]bool{}
	for _, r := range w.Resources {
		seen[r.Source] = true
		seen[r.Path] = true
	}
	for _, choice := range choices {
		r, err := buildResource(cfg, w, choice)
		if err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
			continue
		}
		if seen[r.Source] || seen[r.Path] {
			plan.Blockers = append(plan.Blockers, "repository or path already present: "+r.Source)
			continue
		}
		seen[r.Source], seen[r.Path] = true, true
		if _, err := os.Lstat(r.Path); err == nil {
			plan.Blockers = append(plan.Blockers, "destination already exists: "+r.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		status, err := s.Adapter.Inspect(ctx, request(r))
		if err != nil {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("source %s: %v", r.Source, err))
		} else if status.Present {
			plan.Blockers = append(plan.Blockers, "worktree already registered: "+r.Path)
		} else if !expectedMissing(status.Blockers) {
			plan.Blockers = append(plan.Blockers, status.Blockers...)
		}
	}
	return plan, nil
}

func (s *Service) Add(ctx context.Context, plan Plan) (*domain.Workspace, error) {
	if plan.Kind != "add" || plan.Workspace == nil {
		return nil, fmt.Errorf("invalid add plan")
	}
	if !plan.Safe() {
		return nil, Blockers(plan.Blockers)
	}
	lock, err := s.lock(plan.Workspace.Key)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	fresh, err := s.PlanAdd(ctx, plan.Workspace.Key, plan.Add)
	if err != nil {
		return nil, err
	}
	if !fresh.Safe() {
		return nil, Blockers(fresh.Blockers)
	}
	w := fresh.Workspace
	cfg, err := s.profileFor(w)
	if err != nil {
		return nil, err
	}
	var added []domain.Resource
	for _, c := range plan.Add {
		r, err := buildResource(cfg, w, c)
		if err != nil {
			return nil, err
		}
		added = append(added, r)
	}
	steps := []lifecycle.Step{{ID: "manifest", Description: "Record intended worktrees"}}
	for _, r := range added {
		steps = append(steps, lifecycle.Step{ID: r.ID, Description: "Create worktree " + r.Path})
	}
	op, err := s.Journal.Plan(w.ID, "add", steps)
	if err != nil {
		return nil, err
	}
	w.Resources = append(w.Resources, added...)
	w.UpdatedAt = time.Now().UTC()
	if err := manifest.Write(w); err != nil {
		return nil, s.fail(w, op.ID, err)
	}
	if _, err := s.Journal.Complete(op.ID, "manifest"); err != nil {
		return nil, s.fail(w, op.ID, err)
	}
	for i := range added {
		r := added[i]
		if err := s.Adapter.Create(ctx, request(r)); err != nil {
			return w, s.fail(w, op.ID, err)
		}
		if _, err := s.Journal.Complete(op.ID, r.ID); err != nil {
			return w, s.fail(w, op.ID, err)
		}
		for j := range w.Resources {
			if w.Resources[j].ID == r.ID {
				w.Resources[j].ActualStatus = domain.HealthHealthy
			}
		}
		if err := manifest.Write(w); err != nil {
			return w, s.fail(w, op.ID, err)
		}
	}
	return w, s.Registry.Upsert(w)
}

func (s *Service) PlanCleanup(ctx context.Context, key string) (Plan, error) {
	w, err := s.Find(key)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Kind: "cleanup", Workspace: w, Remove: append([]domain.Resource(nil), w.Resources...)}
	if w.State != domain.StateActive && w.State != domain.StateParked && w.State != domain.StateAttentionRequired && w.State != domain.StateCleaning {
		plan.Blockers = append(plan.Blockers, "workspace state does not permit cleanup")
	}
	cfg, err := s.profileFor(w)
	if err != nil {
		return Plan{}, err
	}
	if !sameRoot(w.Path, cfg.Workspace.Root) {
		plan.Blockers = append(plan.Blockers, "workspace outside configured root")
	}
	if err := checkTree(w); err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	}
	for _, r := range w.Resources {
		if r.Type != "worktree" || r.Driver != "worktrunk" || r.Ownership != domain.OwnershipOwned {
			plan.Blockers = append(plan.Blockers, "unrecognized or unowned resource: "+r.ID)
			continue
		}
		if _, err := config.ValidateDestination(w.Path, r.Path); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
			continue
		}
		status, err := s.Adapter.Inspect(ctx, request(r))
		if err != nil {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("inspect %s: %v", r.Path, err))
			continue
		}
		if !status.Present {
			if !expectedMissing(status.Blockers) {
				plan.Blockers = append(plan.Blockers, status.Blockers...)
			} else if w.State == domain.StateAttentionRequired || w.State == domain.StateCleaning {
				plan.Warnings = append(plan.Warnings, "missing worktree on recovery: "+r.Path)
			} else {
				plan.Blockers = append(plan.Blockers, "registered worktree missing: "+r.Path)
			}
		} else {
			plan.Blockers = append(plan.Blockers, status.Blockers...)
		}
	}
	return plan, nil
}

// checkTree examines every directory that is not an owned worktree. A nested
// worktree may have parent directories, but no unrelated file may hide there.
func checkTree(w *domain.Workspace) error {
	owned := make(map[string]bool, len(w.Resources))
	parents := make(map[string]bool)
	for _, r := range w.Resources {
		rel, err := filepath.Rel(w.Path, r.Path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("worktree outside workspace: %s", r.Path)
		}
		owned[r.Path] = true
		for parent := filepath.Dir(r.Path); parent != w.Path; parent = filepath.Dir(parent) {
			parents[parent] = true
		}
	}
	meta := filepath.Join(w.Path, ".workdrive")
	return filepath.WalkDir(w.Path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == w.Path || path == meta || parents[path] {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("unsafe workspace directory: %s", path)
			}
			return nil
		}
		if owned[path] {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("unsafe worktree directory: %s", path)
			}
			return filepath.SkipDir
		}
		if path == filepath.Join(meta, "workspace.toml") && entry.Type().IsRegular() {
			return nil
		}
		return fmt.Errorf("unknown workspace data blocks cleanup: %s", path)
	})
}
