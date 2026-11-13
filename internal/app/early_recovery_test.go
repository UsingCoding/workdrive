package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/lifecycle"
)

func TestResumeBeforeFirstManifestWrite(t *testing.T) {
	s, f, root := testService(t)
	plan, err := s.PlanNew(NewRequest{Key: "early-crash", Repos: testChoices(t, root, 1)})
	if err != nil || !plan.Safe() {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	w := plan.Workspace
	steps := []lifecycle.Step{{ID: "manifest", Description: "Create workspace metadata"}, {ID: w.Resources[0].ID, Description: "Create worktree"}, {ID: "active", Description: "Mark active"}}
	op, err := s.Journal.PlanWorkspace(w, "create", steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.Path, ".workdrive"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "unknown.txt"), []byte("valuable"), 0600); err != nil {
		t.Fatal(err)
	}
	observed, notes, err := s.Inspect(context.Background(), w.Key)
	if err != nil || observed.ID != w.ID || len(notes) == 0 {
		t.Fatalf("early operation not inspectable: %+v %v %v", observed, notes, err)
	}
	if err := s.Resume(context.Background(), w.Key); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unknown data not blocked: %v", err)
	}
	if f.creates != 0 {
		t.Fatal("recovery created worktree despite unknown data")
	}
	if err := os.Remove(filepath.Join(w.Path, "unknown.txt")); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(context.Background(), w.Key); err != nil {
		t.Fatal(err)
	}
	actual, err := s.Find(w.Key)
	if err != nil || actual.State != domain.StateActive || f.creates != 1 {
		t.Fatalf("recovery: %+v creates=%d err=%v", actual, f.creates, err)
	}
	recorded, err := s.Journal.Load(op.ID)
	if err != nil || !recorded.Done() {
		t.Fatalf("journal incomplete: %+v %v", recorded, err)
	}
}
