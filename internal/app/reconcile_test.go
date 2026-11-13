package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestNestedManifestReconciliation(t *testing.T) {
	s, _, _ := testService(t)
	s.Config.Workspace.DirectoryTemplate = "tasks/{{ .Workspace.Key }}"
	plan, err := s.PlanNew(NewRequest{Key: "reindex"})
	if err != nil || !plan.Safe() {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	w, err := s.Create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(w.Path)) != "tasks" {
		t.Fatalf("not nested: %s", w.Path)
	}
	if err := s.Registry.Delete(w.Key); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.PlanNew(NewRequest{Key: w.Key, Path: filepath.Join(filepath.Dir(filepath.Dir(w.Path)), "other-location")})
	if err != nil || duplicate.Safe() {
		t.Fatalf("unindexed duplicate key accepted: %+v %v", duplicate, err)
	}
	listed, warnings, err := s.List()
	if err != nil || len(listed) != 1 || !strings.Contains(strings.Join(warnings, " "), "not indexed") {
		t.Fatalf("reconciliation: %+v %v %v", listed, warnings, err)
	}
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Find(w.Key); err != nil {
		t.Fatalf("reindex failed: %v", err)
	}
}
