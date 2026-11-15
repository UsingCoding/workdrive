package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/lifecycle"
	"github.com/usingcoding/workdrive/internal/manifest"
)

func (s *Service) Cleanup(ctx context.Context, plan Plan) error {
	if plan.Kind != "cleanup" || plan.Workspace == nil {
		return fmt.Errorf("invalid cleanup plan")
	}
	if !plan.Safe() {
		return Blockers(plan.Blockers)
	}
	lock, err := s.lock(plan.Workspace.Key)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	fresh, err := s.PlanCleanup(ctx, plan.Workspace.Key)
	if err != nil {
		return err
	}
	if !fresh.Safe() {
		return Blockers(fresh.Blockers)
	}
	w := fresh.Workspace
	steps := make([]lifecycle.Step, 0, len(w.Resources)+2)
	for _, r := range w.Resources {
		steps = append(steps, lifecycle.Step{ID: r.ID, Description: "Remove owned worktree " + r.Path + " (preserve branch " + r.Branch + ")"})
	}
	steps = append(steps, lifecycle.Step{ID: "workspace", Description: "Remove empty workspace and known metadata"}, lifecycle.Step{ID: "registry", Description: "Remove registry entry"})
	op, err := s.Journal.Plan(w.ID, "cleanup", steps)
	if err != nil {
		return err
	}
	w.State = domain.StateCleaning
	w.UpdatedAt = time.Now().UTC()
	if err := manifest.Write(w); err != nil {
		return s.fail(w, op.ID, err)
	}
	if err := s.Registry.Upsert(w); err != nil {
		return s.fail(w, op.ID, err)
	}
	for _, r := range w.Resources {
		if err := checkTree(w); err != nil {
			return s.fail(w, op.ID, err)
		}
		status, err := s.Adapter.Inspect(ctx, request(r))
		if err != nil {
			return s.fail(w, op.ID, err)
		}
		if status.Present {
			if len(status.Blockers) > 0 {
				return s.fail(w, op.ID, Blockers(status.Blockers))
			}
		} else if !expectedMissing(status.Blockers) {
			return s.fail(w, op.ID, Blockers(status.Blockers))
		}
		if status.Present {
			if err := s.Adapter.Remove(ctx, request(r)); err != nil {
				return s.fail(w, op.ID, err)
			}
			status, err = s.Adapter.Inspect(ctx, request(r))
			if err != nil {
				return s.fail(w, op.ID, err)
			}
			if status.Present {
				return s.fail(w, op.ID, fmt.Errorf("worktree still registered: %s", r.Path))
			}
		}
		if _, err := s.Journal.Complete(op.ID, r.ID); err != nil {
			return s.fail(w, op.ID, err)
		}
	}
	if err := checkTree(w); err != nil {
		return s.fail(w, op.ID, err)
	}
	for _, r := range w.Resources {
		if _, err := os.Lstat(r.Path); err == nil {
			return s.fail(w, op.ID, fmt.Errorf("worktree path remains: %s", r.Path))
		} else if !errors.Is(err, os.ErrNotExist) {
			return s.fail(w, op.ID, err)
		}
	}
	parents := make(map[string]bool)
	for _, r := range w.Resources {
		for parent := filepath.Dir(r.Path); parent != w.Path; parent = filepath.Dir(parent) {
			parents[parent] = true
		}
	}
	var ordered []string
	for parent := range parents {
		ordered = append(ordered, parent)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, parent := range ordered {
		if err := os.Remove(parent); err != nil && !errors.Is(err, os.ErrNotExist) {
			return s.fail(w, op.ID, err)
		}
	}
	meta := manifest.Path(w.Path)
	if err := os.Remove(meta); err != nil {
		return s.fail(w, op.ID, err)
	}
	if err := os.Remove(filepath.Dir(meta)); err != nil {
		manifest.Write(w)
		return s.fail(w, op.ID, err)
	}
	if err := os.Remove(w.Path); err != nil {
		_ = manifest.Write(w)
		return s.fail(w, op.ID, err)
	}
	if _, err := s.Journal.Complete(op.ID, "workspace"); err != nil {
		return err
	}
	if err := s.Registry.Delete(w.Key); err != nil {
		cfg, cfgErr := s.profileFor(w)
		if cfgErr != nil {
			return errors.Join(err, cfgErr)
		}
		if _, pathErr := config.ValidateDestination(cfg.Workspace.Root, w.Path); pathErr != nil {
			return errors.Join(err, pathErr)
		}
		if pathErr := directoryEmpty(w.Path); pathErr != nil {
			return errors.Join(err, pathErr)
		}
		if mkdirErr := os.MkdirAll(w.Path, 0700); mkdirErr != nil {
			return errors.Join(err, mkdirErr)
		}
		return s.fail(w, op.ID, err)
	}
	_, err = s.Journal.Complete(op.ID, "registry")
	return err
}

func (s *Service) PlanRemove(ctx context.Context, key, selector string) (Plan, error) {
	w, err := s.Find(key)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Kind: "remove", Workspace: w}
	if w.State != domain.StateActive && w.State != domain.StateParked {
		plan.Blockers = append(plan.Blockers, "workspace needs recovery before removing a repository")
	}
	r, err := ResolveResource(w, selector)
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
		return plan, nil
	}
	plan.Remove = []domain.Resource{r}
	if r.Ownership != domain.OwnershipOwned || r.Type != "worktree" {
		plan.Blockers = append(plan.Blockers, "resource is not an owned worktree")
	}
	if err := checkTree(w); err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	}
	status, err := s.Adapter.Inspect(ctx, request(r))
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	} else if !status.Present {
		plan.Blockers = append(plan.Blockers, "worktree missing; inspect and recover first")
	} else {
		plan.Blockers = append(plan.Blockers, status.Blockers...)
	}
	return plan, nil
}

func (s *Service) Remove(ctx context.Context, plan Plan) error {
	if plan.Kind != "remove" || len(plan.Remove) != 1 || plan.Workspace == nil {
		return fmt.Errorf("invalid remove plan")
	}
	if !plan.Safe() {
		return Blockers(plan.Blockers)
	}
	lock, err := s.lock(plan.Workspace.Key)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	fresh, err := s.PlanRemove(ctx, plan.Workspace.Key, plan.Remove[0].ID)
	if err != nil {
		return err
	}
	if !fresh.Safe() {
		return Blockers(fresh.Blockers)
	}
	w := fresh.Workspace
	r := fresh.Remove[0]
	op, err := s.Journal.Plan(w.ID, "remove", []lifecycle.Step{{ID: r.ID, Description: "Remove " + r.Path + " and preserve branch"}, {ID: "manifest", Description: "Detach worktree metadata"}})
	if err != nil {
		return err
	}
	if err := s.Adapter.Remove(ctx, request(r)); err != nil {
		return s.fail(w, op.ID, err)
	}
	if _, err := s.Journal.Complete(op.ID, r.ID); err != nil {
		return s.fail(w, op.ID, err)
	}
	var keep []domain.Resource
	for _, item := range w.Resources {
		if item.ID != r.ID {
			keep = append(keep, item)
		}
	}
	w.Resources = keep
	w.UpdatedAt = time.Now().UTC()
	if err := manifest.Write(w); err != nil {
		return s.fail(w, op.ID, err)
	}
	if err := s.Registry.Upsert(w); err != nil {
		return s.fail(w, op.ID, err)
	}
	_, err = s.Journal.Complete(op.ID, "manifest")
	return err
}

// Resume inspects the actual filesystem before replaying an interrupted create
// or cleanup. No uncertain worktree is adopted or deleted automatically.
func (s *Service) Resume(ctx context.Context, key string) error {
	w, err := s.Find(key)
	if errors.Is(err, ErrNotFound) {
		op, lookupErr := s.pendingKey(key)
		if lookupErr != nil {
			return lookupErr
		}
		if err := s.restoreCreateIntent(op); err != nil {
			return err
		}
		w, err = s.Find(key)
	}
	if err != nil {
		return err
	}
	if w.State != domain.StateAttentionRequired && w.State != domain.StateCreating && w.State != domain.StateCleaning {
		return fmt.Errorf("workspace %s has no interrupted operation", key)
	}
	op, err := s.pending(w.ID)
	if err != nil {
		return err
	}
	switch op.Kind {
	case "create", "add":
		lock, err := s.lock(key)
		if err != nil {
			return err
		}
		defer lock.Unlock()
		w, err = s.Find(key)
		if err != nil {
			return err
		}
		if _, err := s.Journal.Complete(op.ID, "manifest"); err != nil {
			return s.fail(w, op.ID, err)
		}
		resources := make(map[string]domain.Resource, len(w.Resources))
		for _, r := range w.Resources {
			resources[r.ID] = r
		}
		for _, step := range op.Steps {
			r, ok := resources[step.ID]
			if !ok {
				continue
			}
			status, err := s.Adapter.Inspect(ctx, request(r))
			if err != nil {
				return s.fail(w, op.ID, err)
			}
			if status.Present {
				if len(status.Blockers) > 0 {
					return Blockers(status.Blockers)
				}
			} else {
				if !expectedMissing(status.Blockers) {
					return Blockers(status.Blockers)
				}
				if _, err := os.Lstat(r.Path); err == nil {
					return Blockers{"unregistered path exists: " + r.Path}
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if err := s.Adapter.Create(ctx, request(r)); err != nil {
					return s.fail(w, op.ID, err)
				}
			}
			if _, err := s.Journal.Complete(op.ID, r.ID); err != nil {
				return s.fail(w, op.ID, err)
			}
		}
		w.State = domain.StateActive
		w.ActualStatus = domain.HealthHealthy
		w.UpdatedAt = time.Now().UTC()
		if err := manifest.Write(w); err != nil {
			return s.fail(w, op.ID, err)
		}
		if err := s.Registry.Upsert(w); err != nil {
			return s.fail(w, op.ID, err)
		}
		if op.Kind == "create" {
			_, err = s.Journal.Complete(op.ID, "active")
		}
		return err
	case "park", "switch":
		lock, err := s.lock(key)
		if err != nil {
			return err
		}
		defer lock.Unlock()
		w, err = s.Find(key)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(w.Path); err != nil {
			return err
		}
		if op.Kind == "park" {
			w.State = domain.StateParked
		} else {
			w.State = domain.StateActive
		}
		w.UpdatedAt = time.Now().UTC()
		if err := manifest.Write(w); err != nil {
			return s.fail(w, op.ID, err)
		}
		if err := s.Registry.Upsert(w); err != nil {
			return s.fail(w, op.ID, err)
		}
		_, err = s.Journal.Complete(op.ID, "state")
		return err
	case "remove":
		lock, err := s.lock(key)
		if err != nil {
			return err
		}
		defer lock.Unlock()
		w, err = s.Find(key)
		if err != nil {
			return err
		}
		var target *domain.Resource
		for i := range w.Resources {
			if w.Resources[i].ID == op.Steps[0].ID {
				target = &w.Resources[i]
				break
			}
		}
		if target == nil {
			return fmt.Errorf("removed resource not found in manifest; review operation %s", op.ID)
		}
		if target.Type != "worktree" || target.Ownership != domain.OwnershipOwned {
			return Blockers{"cannot resume removal of unowned resource"}
		}
		if err := checkTree(w); err != nil {
			return err
		}
		status, err := s.Adapter.Inspect(ctx, request(*target))
		if err != nil {
			return err
		}
		if status.Present {
			if len(status.Blockers) > 0 {
				return Blockers(status.Blockers)
			}
			if err := s.Adapter.Remove(ctx, request(*target)); err != nil {
				return s.fail(w, op.ID, err)
			}
		} else if !expectedMissing(status.Blockers) {
			return Blockers(status.Blockers)
		}
		if _, err := os.Lstat(target.Path); !errors.Is(err, os.ErrNotExist) {
			return Blockers{"worktree path remains: " + target.Path}
		}
		if _, err := s.Journal.Complete(op.ID, target.ID); err != nil {
			return s.fail(w, op.ID, err)
		}
		var keep []domain.Resource
		for _, r := range w.Resources {
			if r.ID != target.ID {
				keep = append(keep, r)
			}
		}
		w.Resources = keep
		w.State = domain.StateActive
		w.UpdatedAt = time.Now().UTC()
		if err := manifest.Write(w); err != nil {
			return s.fail(w, op.ID, err)
		}
		if err := s.Registry.Upsert(w); err != nil {
			return s.fail(w, op.ID, err)
		}
		_, err = s.Journal.Complete(op.ID, "manifest")
		return err
	case "cleanup":
		plan, err := s.PlanCleanup(ctx, key)
		if err != nil {
			return err
		}
		if !plan.Safe() {
			return Blockers(plan.Blockers)
		}
		if err := s.Cleanup(ctx, plan); err != nil {
			return err
		}
		lock, err := s.lock(key)
		if err != nil {
			return err
		}
		defer lock.Unlock()
		if _, err := os.Lstat(w.Path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workspace still present after cleanup: %v", err)
		}
		entry, err := s.Registry.Get(key)
		if err != nil {
			return err
		}
		if entry != nil {
			return fmt.Errorf("workspace still indexed after cleanup")
		}
		for _, step := range op.Steps {
			if step.ID != "workspace" && step.ID != "registry" {
				for _, r := range w.Resources {
					if r.ID == step.ID {
						if _, err := os.Lstat(r.Path); !errors.Is(err, os.ErrNotExist) {
							return fmt.Errorf("worktree path still present: %s", r.Path)
						}
					}
				}
			}
			if _, err := s.Journal.Complete(op.ID, step.ID); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("operation %s (%s) needs manual review", op.ID, op.Kind)
	}
}

// pendingKey locates durable create intent when the first manifest write failed.
func (s *Service) pendingKey(key string) (*lifecycle.Operation, error) {
	if err := domain.ValidateKey(key); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.StateDir, "operations"))
	if err != nil {
		return nil, err
	}
	var selected *lifecycle.Operation
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		op, err := s.Journal.Load(id)
		if err != nil || op.Done() || op.Kind != "create" || op.Workspace == nil || op.Workspace.Key != key || op.Workspace.ID != op.WorkspaceID {
			continue
		}
		if selected == nil || op.CreatedAt.After(selected.CreatedAt) {
			selected = op
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("%w: no recoverable create intent for %s", ErrNotFound, key)
	}
	return selected, nil
}

func (s *Service) restoreCreateIntent(op *lifecycle.Operation) error {
	w := *op.Workspace
	w.Resources = append([]domain.Resource(nil), op.Workspace.Resources...)
	lock, err := s.lock(w.Key)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	cfg, err := s.profileFor(&w)
	if err != nil {
		return err
	}
	if _, err := config.ValidateDestination(cfg.Workspace.Root, w.Path); err != nil {
		return err
	}
	if existing, err := manifest.Read(w.Path); err == nil {
		if existing.ID != w.ID {
			return Blockers{"workspace manifest belongs to another identity"}
		}
		if err := s.Registry.Upsert(existing); err != nil {
			return err
		}
		_, err = s.Journal.Complete(op.ID, "manifest")
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return Blockers{"unrecognized workspace metadata: " + err.Error()}
	}
	if st, err := os.Lstat(w.Path); err == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return Blockers{"unsafe workspace path: " + w.Path}
		}
		entries, err := os.ReadDir(w.Path)
		if err != nil {
			return err
		}
		if len(entries) > 1 {
			return Blockers{"unknown workspace files block recovery: " + w.Path}
		}
		if len(entries) == 1 {
			if entries[0].Name() != ".workdrive" || !entries[0].IsDir() || entries[0].Type()&os.ModeSymlink != 0 {
				return Blockers{"unknown workspace files block recovery: " + w.Path}
			}
			meta, err := os.ReadDir(filepath.Join(w.Path, ".workdrive"))
			if err != nil {
				return err
			}
			if len(meta) > 0 {
				return Blockers{"unknown metadata blocks recovery: " + w.Path}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(w.Path, 0700); err != nil {
		return err
	}
	if err := manifest.Write(&w); err != nil {
		return err
	}
	if err := s.Registry.Upsert(&w); err != nil {
		return err
	}
	_, err = s.Journal.Complete(op.ID, "manifest")
	return err
}

func expectedMissing(blockers []string) bool {
	return len(blockers) == 0 || (len(blockers) == 1 && strings.Contains(blockers[0], "not registered for the source"))
}

func (s *Service) pending(workspaceID string) (*lifecycle.Operation, error) {
	entries, err := os.ReadDir(filepath.Join(s.StateDir, "operations"))
	if err != nil {
		return nil, err
	}
	var selected *lifecycle.Operation
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		op, err := s.Journal.Load(id)
		if err != nil {
			continue
		}
		if op.WorkspaceID == workspaceID && !op.Done() && (selected == nil || op.CreatedAt.After(selected.CreatedAt)) {
			selected = op
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("%w: no operation journal for workspace", ErrInterrupted)
	}
	return selected, nil
}
