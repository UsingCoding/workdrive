package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/usingcoding/workdrive/internal/manifest"
)

func TestQueryMissingStateRemainsMissing(t *testing.T) {
	state := filepath.Join(t.TempDir(), "not-created")
	q, err := OpenQuery(state)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if ws, err := q.List(); err != nil || len(ws) != 0 {
		t.Fatalf("list: %v %v", ws, err)
	}
	if _, err := q.Find("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("find: %v", err)
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatalf("query created state: %v", err)
	}
}

func TestQueryRejectsStaleIdentityAndConfiguration(t *testing.T) {
	s, _, _ := testService(t)
	plan, err := s.PlanNew(NewRequest{Key: "task"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	q, err := OpenQuery(s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if found, err := q.Find(w.Key); err != nil || found.ID != w.ID {
		t.Fatalf("find: %+v %v", found, err)
	}
	// An unindexed manifest must never enter indexed completion suggestions.
	other := *w
	other.ID, other.Key, other.Path = "other-id", "unindexed", filepath.Join(s.Config.Workspace.Root, "unindexed")
	if err := os.Mkdir(other.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(&other); err != nil {
		t.Fatal(err)
	}
	if found, err := q.List(); err != nil || len(found) != 1 {
		t.Fatalf("list scanned roots: %+v %v", found, err)
	}
	originalID := w.ID
	w.ID = "replacement-identity"
	if err := manifest.Write(w); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Find(w.Key); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused key accepted: %v", err)
	}
	if found, err := q.List(); err != nil || len(found) != 0 {
		t.Fatalf("stale identity listed: %+v %v", found, err)
	}
	w.ID = originalID
	if err := manifest.Write(w); err != nil {
		t.Fatal(err)
	}
	// A later root/profile change must not strand existing workspaces from
	// inspect/resume/cleanup; only navigation enforces the current root.
	s.Config.Workspace.Root = filepath.Join(t.TempDir(), "elsewhere")
	if _, err := q.Find(w.Key); err != nil {
		t.Fatalf("workspace unreachable after root change: %v", err)
	}
	if _, err := s.Find(w.Key); err != nil {
		t.Fatalf("service cannot recover workspace after root change: %v", err)
	}
	if _, err := s.Navigate(NavigationRequest{Key: w.Key}); !errors.Is(err, ErrConflict) {
		t.Fatalf("navigation outside configured root accepted: %v", err)
	}
	if err := os.Remove(manifest.Path(w.Path)); err != nil {
		t.Fatal(err)
	}
	if found, err := q.List(); err != nil || len(found) != 0 {
		t.Fatalf("missing manifest listed: %+v %v", found, err)
	}
}
