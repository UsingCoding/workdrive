package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/drivers/worktrunk"
)

type fakeWorktrunk struct {
	present          map[string]bool
	failCreateAt     int
	failRemoveAt     int
	creates, removes int
}

func (f *fakeWorktrunk) Create(_ context.Context, r worktrunk.Request) error {
	f.creates++
	if f.creates == f.failCreateAt {
		return errors.New("simulated create interruption")
	}
	if err := os.MkdirAll(r.Path, 0700); err != nil {
		return err
	}
	f.present[r.Path] = true
	return nil
}
func (f *fakeWorktrunk) Inspect(_ context.Context, r worktrunk.Request) (worktrunk.Inspection, error) {
	return worktrunk.Inspection{Present: f.present[r.Path], Clean: true}, nil
}
func (f *fakeWorktrunk) Remove(_ context.Context, r worktrunk.Request) error {
	f.removes++
	if f.removes == f.failRemoveAt {
		return errors.New("simulated remove interruption")
	}
	if err := os.Remove(r.Path); err != nil {
		return err
	}
	delete(f.present, r.Path)
	return nil
}
func testService(t *testing.T) (*Service, *fakeWorktrunk, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	cfg.Repositories.Sources = nil
	s, err := Open(cfg, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	fake := &fakeWorktrunk{present: map[string]bool{}}
	s.Adapter = fake
	return s, fake, root
}
func testChoices(t *testing.T, root string, n int) []RepositoryChoice {
	t.Helper()
	var choices []RepositoryChoice
	for i := 0; i < n; i++ {
		name := string(rune('a' + i))
		source := filepath.Join(root, "sources", name)
		if err := os.MkdirAll(source, 0700); err != nil {
			t.Fatal(err)
		}
		choices = append(choices, RepositoryChoice{Source: source, Name: name})
	}
	return choices
}
func TestZeroRepoWorkspaceLifecycle(t *testing.T) {
	s, _, _ := testService(t)
	ctx := context.Background()
	plan, err := s.PlanNew(NewRequest{Key: "scratch-api"})
	if err != nil || !plan.Safe() {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	w, err := s.Create(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	listed, _, err := s.List()
	if err != nil || len(listed) != 1 || listed[0].ActualStatus != domain.HealthUnknown {
		t.Fatalf("list must not infer health from manifest: %+v %v", listed, err)
	}
	if w.State != domain.StateActive || len(w.Resources) != 0 {
		t.Fatalf("created: %+v", w)
	}
	if _, err := s.Park(w.Key); err != nil {
		t.Fatal(err)
	}
	resumed, _, err := s.Switch(w.Key)
	if err != nil || resumed.State != domain.StateActive {
		t.Fatalf("switch: %+v %v", resumed, err)
	}
	cleanup, err := s.PlanCleanup(ctx, w.Key)
	if err != nil || !cleanup.Safe() {
		t.Fatalf("cleanup plan: %+v %v", cleanup, err)
	}
	if err := s.Cleanup(ctx, cleanup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains: %v", err)
	}
	if _, err := s.Find(w.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("index remains: %v", err)
	}
}
func TestAddAndUnknownDataBlocksCleanup(t *testing.T) {
	s, _, root := testService(t)
	ctx := context.Background()
	choices := testChoices(t, root, 3)
	plan, err := s.PlanNew(NewRequest{Key: "task", Repos: choices[:2]})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Create(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	add, err := s.PlanAdd(ctx, w.Key, choices[2:])
	if err != nil || !add.Safe() {
		t.Fatalf("add: %+v %v", add, err)
	}
	w, err = s.Add(ctx, add)
	if err != nil || len(w.Resources) != 3 {
		t.Fatalf("added: %+v %v", w, err)
	}
	for _, r := range w.Resources {
		if _, err := os.Stat(r.Path); err != nil {
			t.Fatal(err)
		}
	}
	unknown := filepath.Join(w.Path, "notes.txt")
	if err := os.WriteFile(unknown, []byte("valuable"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanup, err := s.PlanCleanup(ctx, w.Key)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Safe() {
		t.Fatal("unknown data permitted cleanup")
	}
	if err := s.Cleanup(ctx, cleanup); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	cleanup, err = s.PlanCleanup(ctx, w.Key)
	if err != nil || !cleanup.Safe() {
		t.Fatalf("clean plan: %+v %v", cleanup, err)
	}
	if err := s.Cleanup(ctx, cleanup); err != nil {
		t.Fatal(err)
	}
}
func TestInterruptedCreateResumesOnlyMissingWorktree(t *testing.T) {
	s, f, root := testService(t)
	ctx := context.Background()
	f.failCreateAt = 2
	plan, err := s.PlanNew(NewRequest{Key: "failure", Repos: testChoices(t, root, 2)})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Create(ctx, plan)
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("wanted recoverable error: %v", err)
	}
	if w.State != domain.StateAttentionRequired || !f.present[w.Resources[0].Path] {
		t.Fatalf("partial state: %+v", w)
	}
	f.failCreateAt = 0
	if err := s.Resume(ctx, w.Key); err != nil {
		t.Fatal(err)
	}
	if f.creates != 3 {
		t.Fatalf("expected two initial calls plus one recovery, got %d", f.creates)
	}
	w, err = s.Find(w.Key)
	if err != nil || w.State != domain.StateActive {
		t.Fatalf("resume: %+v %v", w, err)
	}
}
func TestInterruptedCleanupResumes(t *testing.T) {
	s, f, root := testService(t)
	ctx := context.Background()
	plan, err := s.PlanNew(NewRequest{Key: "failure-cleanup", Repos: testChoices(t, root, 2)})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Create(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	f.failRemoveAt = 2
	cleanup, err := s.PlanCleanup(ctx, w.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx, cleanup); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("wanted interruption: %v", err)
	}
	if _, err := s.Find(w.Key); err != nil {
		t.Fatal(err)
	}
	f.failRemoveAt = 0
	if err := s.Resume(ctx, w.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find(w.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still registered: %v", err)
	}
}
func TestRejectPathAndKeyCollisions(t *testing.T) {
	s, _, root := testService(t)
	for _, key := range []string{"../oops", ".workdrive", " leading", "a/b"} {
		if _, err := s.PlanNew(NewRequest{Key: key}); err == nil {
			t.Errorf("accepted key %q", key)
		}
	}
	outside := filepath.Join(root, "outside")
	if _, err := s.PlanNew(NewRequest{Key: "valid", Path: outside}); err == nil {
		t.Fatal("accepted outside path")
	}
	collision := filepath.Join(s.Config.Workspace.Root, "collision")
	if err := os.MkdirAll(collision, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collision, "data"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := s.PlanNew(NewRequest{Key: "collision"})
	if err != nil || p.Safe() {
		t.Fatalf("accepted collision: %+v %v", p, err)
	}
	if !strings.Contains(strings.Join(p.Blockers, " "), "not empty") {
		t.Fatalf("wrong blocker: %v", p.Blockers)
	}
}
