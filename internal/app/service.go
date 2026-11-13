package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/discovery"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/drivers/worktrunk"
	"github.com/usingcoding/workdrive/internal/lifecycle"
	"github.com/usingcoding/workdrive/internal/manifest"
	"github.com/usingcoding/workdrive/internal/registry"
)

type Service struct {
	Config      *config.Config
	Registry    *registry.Registry
	Journal     *lifecycle.Journal
	Adapter     worktrunk.Adapter
	StateDir    string
	mu          sync.Mutex
	discoveries map[string]discovery.Result
}

func Open(cfg *config.Config, stateDir string) (*Service, error) {
	index, err := registry.Open(filepath.Join(stateDir, "registry.db"))
	if err != nil {
		return nil, err
	}
	journal, err := lifecycle.NewJournal(filepath.Join(stateDir, "operations"))
	if err != nil {
		index.Close()
		return nil, err
	}
	s := &Service{Config: cfg, Registry: index, Journal: journal, Adapter: worktrunk.New(cfg.Worktree.Executable), StateDir: stateDir, discoveries: map[string]discovery.Result{}}
	if cfg.Repositories.RefreshOnStart {
		go s.Discover(context.Background(), "")
	}
	return s, nil
}

func (s *Service) Close() error { return s.Registry.Close() }

func (s *Service) lock(key string) (*lifecycle.Lock, error) {
	if err := domain.ValidateKey(key); err != nil {
		return nil, err
	}
	return lifecycle.AcquireLock(filepath.Join(s.StateDir, "locks", key+".lock"))
}

func (s *Service) Find(key string) (*domain.Workspace, error) {
	if err := domain.ValidateKey(key); err != nil {
		return nil, err
	}
	entry, err := s.Registry.Get(key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s (run refresh to discover manifests)", ErrNotFound, key)
	}
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("%w: %s (run refresh to discover manifests)", ErrNotFound, key)
	}
	w, err := manifest.Read(entry.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest for %s: %v", ErrInterrupted, key, err)
	}
	if w.Key != key || w.Path != entry.Path {
		return nil, fmt.Errorf("%w: registry and manifest disagree for %s", ErrConflict, key)
	}
	return w, nil
}

// List rechecks manifests; the SQLite index alone never establishes ownership.
func (s *Service) List() ([]*domain.Workspace, []string, error) {
	entries, err := s.Registry.List()
	if err != nil {
		return nil, nil, err
	}
	var result []*domain.Workspace
	var warnings []string
	seen := make(map[string]string)
	for _, e := range entries {
		w, err := manifest.Read(e.Path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: manifest missing or invalid: %v", e.Key, err))
			continue
		}
		if w.Key != e.Key || w.Path != e.Path {
			warnings = append(warnings, fmt.Sprintf("%s: manifest/index identity conflict", e.Key))
			continue
		}
		seen[e.Key] = e.Path
		markUnverified(w)
		result = append(result, w)
	}
	depthLimit := 1 + strings.Count(filepath.ToSlash(s.Config.Workspace.DirectoryTemplate), "/")
	for _, root := range s.roots() {
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			warnings = append(warnings, fmt.Sprintf("scan %s: %v", root, err))
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				warnings = append(warnings, fmt.Sprintf("scan %s: %v", path, walkErr))
				return nil
			}
			if !entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			if path == root {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if strings.Count(rel, string(filepath.Separator))+1 > depthLimit || entry.Name() == ".workdrive" || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			if _, err := os.Lstat(manifest.Path(path)); errors.Is(err, os.ErrNotExist) {
				return nil
			} else if err != nil {
				warnings = append(warnings, err.Error())
				return filepath.SkipDir
			}
			w, err := manifest.Read(path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("invalid manifest %s: %v", path, err))
				return filepath.SkipDir
			}
			if previous, exists := seen[w.Key]; exists {
				if previous != w.Path {
					warnings = append(warnings, fmt.Sprintf("%s: duplicate manifest key at %s", w.Key, w.Path))
				}
				return filepath.SkipDir
			}
			warnings = append(warnings, fmt.Sprintf("%s: manifest is not indexed (run refresh)", w.Key))
			seen[w.Key] = w.Path
			markUnverified(w)
			result = append(result, w)
			return filepath.SkipDir
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("scan %s: %v", root, err))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, warnings, nil
}

// Listing is cached and never treats yesterday's successful manifest as proof
// that a worktree is still healthy. Inspection performs the actual Git checks.
func markUnverified(w *domain.Workspace) {
	w.ActualStatus = domain.HealthUnknown
	if st, err := os.Lstat(w.Path); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		w.ActualStatus = domain.HealthMissing
		return
	}
	for i := range w.Resources {
		r := &w.Resources[i]
		r.ActualStatus = domain.HealthUnknown
		if st, err := os.Lstat(r.Path); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			r.ActualStatus = domain.HealthMissing
			w.ActualStatus = domain.HealthMissing
		}
	}
}

func (s *Service) roots() []string {
	var roots []string
	add := func(root string) {
		if canonical, err := config.CanonicalPath(root); err == nil {
			roots = append(roots, canonical)
		}
	}
	add(s.Config.Workspace.Root)
	for name := range s.Config.Profiles {
		c, err := s.Config.Effective(name)
		if err == nil && c.Workspace.Root != "" {
			add(c.Workspace.Root)
		}
	}
	return roots
}

func (s *Service) Discover(ctx context.Context, profile string) discovery.Result {
	if profile == "" {
		profile = s.Config.Workspace.DefaultProfile
	}
	s.mu.Lock()
	cached, ok := s.discoveries[profile]
	s.mu.Unlock()
	if ok {
		return cached
	}
	return s.RefreshDiscovery(ctx, profile)
}

// RefreshDiscovery discards the cached source list for the selected profile.
func (s *Service) RefreshDiscovery(ctx context.Context, profile string) discovery.Result {
	if profile == "" {
		profile = s.Config.Workspace.DefaultProfile
	}
	cfg, err := s.Config.Effective(profile)
	if err != nil {
		return discovery.Result{Warnings: []discovery.Warning{{Message: err.Error()}}}
	}
	result := discovery.Scan(ctx, cfg.Repositories, s.roots()...)
	if ctx.Err() == nil {
		s.mu.Lock()
		s.discoveries[profile] = result
		s.mu.Unlock()
	}
	return result
}

func (s *Service) PlanNew(req NewRequest) (Plan, error) {
	if err := domain.ValidateKey(req.Key); err != nil {
		return Plan{}, err
	}
	cfg, err := s.Config.Effective(req.Profile)
	if err != nil {
		return Plan{}, err
	}
	name := req.Name
	if name == "" {
		name = req.Key
	}
	path, err := cfg.WorkspacePathWithName(req.Key, name)
	if err != nil {
		return Plan{}, err
	}
	if req.Path != "" {
		path, err = config.ValidateDestination(cfg.Workspace.Root, req.Path)
		if err != nil {
			return Plan{}, err
		}
	}
	profile := req.Profile
	if profile == "" {
		profile = cfg.Workspace.DefaultProfile
	}
	w, err := domain.NewWorkspace(req.Key, name, path, profile, time.Now().UTC())
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Kind: "create", Workspace: w, Add: req.Repos}
	known, warnings, err := s.List()
	if err != nil {
		return Plan{}, err
	}
	plan.Warnings = append(plan.Warnings, warnings...)
	for _, other := range known {
		if other.Key == req.Key {
			plan.Blockers = append(plan.Blockers, "key already has a manifest: "+other.Path)
		}
	}
	if old, err := s.Registry.Get(req.Key); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Plan{}, err
	} else if old != nil {
		plan.Blockers = append(plan.Blockers, "key already registered: "+old.Path)
	}
	if err := directoryEmpty(path); err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	}
	seenPaths := map[string]bool{}
	seenSources := map[string]bool{}
	for _, choice := range req.Repos {
		resource, err := buildResource(cfg, w, choice)
		if err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
			continue
		}
		if seenPaths[resource.Path] || seenSources[resource.Source] {
			plan.Blockers = append(plan.Blockers, "duplicate repository or destination: "+resource.Path)
			continue
		}
		seenPaths[resource.Path], seenSources[resource.Source] = true, true
		w.Resources = append(w.Resources, resource)
		if _, err := os.Lstat(resource.Path); err == nil {
			plan.Blockers = append(plan.Blockers, "worktree destination already exists: "+resource.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		status, err := s.Adapter.Inspect(context.Background(), request(resource))
		if err != nil {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("source %s: %v", resource.Source, err))
		} else if status.Present {
			plan.Blockers = append(plan.Blockers, "worktree already registered: "+resource.Path)
		} else if !expectedMissing(status.Blockers) {
			plan.Blockers = append(plan.Blockers, status.Blockers...)
		}
	}
	return plan, nil
}

func buildResource(cfg *config.Config, w *domain.Workspace, c RepositoryChoice) (domain.Resource, error) {
	source, err := config.CanonicalPath(c.Source)
	if err != nil {
		return domain.Resource{}, err
	}
	name := c.Name
	if name == "" {
		name = filepath.Base(source)
	}
	path := c.Path
	if path == "" {
		path, err = cfg.WorktreePathWithContext(w.Key, w.Name, w.Path, name, source)
		if err != nil {
			return domain.Resource{}, err
		}
	}
	path, err = config.ValidateDestination(w.Path, path)
	if err != nil {
		return domain.Resource{}, err
	}
	branch := c.Branch
	if branch == "" {
		branch, err = cfg.Branch(w.Key, w.Name, w.Path, name, source)
		if err != nil {
			return domain.Resource{}, err
		}
	}
	if err := config.ValidateBranch(branch); err != nil {
		return domain.Resource{}, err
	}
	id, err := domain.NewID()
	if err != nil {
		return domain.Resource{}, err
	}
	base := c.Base
	if base == "" {
		base = cfg.Worktree.BaseRef
	}
	return domain.Resource{ID: id, Type: "worktree", Driver: "worktrunk", Ownership: domain.OwnershipOwned, Source: source, Path: path, Branch: branch, Base: base, ActualStatus: domain.HealthUnknown}, nil
}

func directoryEmpty(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination is not an ordinary directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("workspace directory is not empty: %s", path)
	}
	return nil
}

func (s *Service) Refresh(ctx context.Context) ([]string, error) {
	_, warnings, err := s.List()
	if err != nil {
		return warnings, err
	}
	if err := s.Registry.Rebuild(s.roots()); err != nil {
		return warnings, err
	}
	s.mu.Lock()
	s.discoveries = map[string]discovery.Result{}
	s.mu.Unlock()
	workspaces, _, err := s.List()
	if err != nil {
		return warnings, err
	}
	for _, w := range workspaces {
		if err := ctx.Err(); err != nil {
			return warnings, err
		}
		actual, notes, err := s.Inspect(ctx, w.Key)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", w.Key, err))
			continue
		}
		for _, note := range notes {
			warnings = append(warnings, fmt.Sprintf("%s: %s", w.Key, note))
		}
		if err := s.Registry.Upsert(actual); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

func (s *Service) Inspect(ctx context.Context, key string) (*domain.Workspace, []string, error) {
	w, err := s.Find(key)
	if errors.Is(err, ErrNotFound) {
		if op, lookupErr := s.pendingKey(key); lookupErr == nil {
			planned := *op.Workspace
			planned.ActualStatus = domain.HealthUnknown
			return &planned, []string{fmt.Sprintf("create operation %s has no manifest yet; review the directory and run workdrive resume %s", op.ID, key)}, nil
		}
	}
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	if w.State == domain.StateAttentionRequired || w.State == domain.StateCleaning || w.State == domain.StateCreating {
		if op, err := s.pending(w.ID); err == nil {
			warnings = append(warnings, fmt.Sprintf("interrupted %s operation %s: %s (run workdrive resume %s after review)", op.Kind, op.ID, op.Error, w.Key))
		} else {
			warnings = append(warnings, err.Error())
		}
	}
	if st, err := os.Lstat(w.Path); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		w.ActualStatus = domain.HealthMissing
		warnings = append(warnings, "workspace directory missing or unsafe")
		return w, warnings, nil
	}
	w.ActualStatus = domain.HealthHealthy
	for i := range w.Resources {
		r := &w.Resources[i]
		status, err := s.Adapter.Inspect(ctx, request(*r))
		if err != nil {
			r.ActualStatus = domain.HealthError
			warnings = append(warnings, fmt.Sprintf("%s: %v", r.Path, err))
			w.ActualStatus = domain.HealthError
			continue
		}
		if !status.Present {
			r.ActualStatus = domain.HealthMissing
			w.ActualStatus = domain.HealthMissing
			warnings = append(warnings, "missing worktree: "+r.Path)
		} else if len(status.Blockers) > 0 {
			r.ActualStatus = domain.HealthConflict
			w.ActualStatus = domain.HealthConflict
			warnings = append(warnings, status.Blockers...)
		} else {
			r.ActualStatus = domain.HealthHealthy
		}
	}
	return w, warnings, nil
}

func request(r domain.Resource) worktrunk.Request {
	return worktrunk.Request{Source: r.Source, Path: r.Path, Branch: r.Branch, Base: r.Base}
}

func (s *Service) profileFor(w *domain.Workspace) (*config.Config, error) {
	if w.Profile == "" {
		return s.Config.Effective("")
	}
	return s.Config.Effective(w.Profile)
}

func sameRoot(path, root string) bool {
	canonical, err := config.CanonicalPath(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(canonical, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
