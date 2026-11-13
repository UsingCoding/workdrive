package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNestedUnknownFileBlocksBeforeWorktreeRemoval(t *testing.T) {
	s, fake, root := testService(t)
	ctx := context.Background()
	choices := testChoices(t, root, 1)
	workspace, err := s.Config.WorkspacePath("nested")
	if err != nil {
		t.Fatal(err)
	}
	choices[0].Path = filepath.Join(workspace, "services", "a")
	plan, err := s.PlanNew(NewRequest{Key: "nested", Repos: choices})
	if err != nil || !plan.Safe() {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	w, err := s.Create(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(w.Path, "services", "notes.txt")
	if err := os.WriteFile(note, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanup, err := s.PlanCleanup(ctx, w.Key)
	if err != nil || cleanup.Safe() {
		t.Fatalf("nested data not blocked: %+v %v", cleanup, err)
	}
	if fake.removes != 0 {
		t.Fatal("preflight removed worktree")
	}
	if err := os.Remove(note); err != nil {
		t.Fatal(err)
	}
	cleanup, err = s.PlanCleanup(ctx, w.Key)
	if err != nil || !cleanup.Safe() {
		t.Fatalf("clean plan: %+v %v", cleanup, err)
	}
	if err := s.Cleanup(ctx, cleanup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains: %v", err)
	}
}

func TestResumeAddDoesNotReplayExistingWorktree(t *testing.T) {
	s, fake, root := testService(t)
	ctx := context.Background()
	choices := testChoices(t, root, 3)
	plan, err := s.PlanNew(NewRequest{Key: "add-resume", Repos: choices[:1]})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Create(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	fake.failCreateAt = 3
	add, err := s.PlanAdd(ctx, w.Key, choices[1:])
	if err != nil || !add.Safe() {
		t.Fatalf("add plan: %+v %v", add, err)
	}
	if _, err := s.Add(ctx, add); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("wanted interruption: %v", err)
	}
	fake.failCreateAt = 0
	if err := s.Resume(ctx, w.Key); err != nil {
		t.Fatal(err)
	}
	if fake.creates != 4 {
		t.Fatalf("replayed existing worktree: creates=%d", fake.creates)
	}
}
