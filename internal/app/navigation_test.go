package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/manifest"
)

func TestRepositoryNavigationValidatesBeforeActivation(t *testing.T) {
	for _, kind := range []string{"missing", "outside", "symlink", "ambiguous", "unsupported", "identity"} {
		t.Run(kind, func(t *testing.T) {
			s, _, root := testService(t)
			plan, err := s.PlanNew(NewRequest{Key: "target", Repos: testChoices(t, root, 2)})
			if err != nil || !plan.Safe() {
				t.Fatalf("plan: %v %+v", err, plan)
			}
			w, err := s.Create(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			w, err = s.Park(w.Key)
			if err != nil {
				t.Fatal(err)
			}
			req := NavigationRequest{Key: w.Key, Repository: w.Resources[0].ID}
			switch kind {
			case "missing":
				if err := os.Remove(w.Resources[0].Path); err != nil {
					t.Fatal(err)
				}
			case "outside":
				w.Resources[0].Path = root
			case "symlink":
				old := w.Resources[0].Path
				if err := os.Remove(old); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(w.Resources[1].Path, old); err != nil {
					t.Fatal(err)
				}
			case "ambiguous":
				w.Resources[1].Source = filepath.Join(root, "another", filepath.Base(w.Resources[0].Source))
				req.Repository = filepath.Base(w.Resources[0].Source)
			case "unsupported":
				w.Resources[0].Type = "other"
			case "identity":
				req.ExpectedID = "stale-identity"
			}
			if err := manifest.Write(w); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Navigate(req); err == nil {
				t.Fatal("invalid navigation succeeded")
			}
			got, err := s.Find(w.Key)
			if err != nil || got.State != domain.StateParked {
				t.Fatalf("activation after invalid target: %+v %v", got, err)
			}
		})
	}
}

func TestNavigationRecordedDestinationAndWorkspaceAt(t *testing.T) {
	s, _, root := testService(t)
	choices := testChoices(t, root, 2)
	canonical, err := config.CanonicalPath(s.Config.Workspace.Root)
	if err != nil {
		t.Fatal(err)
	}
	choices[0].Path = filepath.Join(canonical, "target", "custom", "checkout")
	plan, err := s.PlanNew(NewRequest{Key: "target", Repos: choices})
	if err != nil || !plan.Safe() {
		t.Fatalf("plan: %v %+v", err, plan)
	}
	w, err := s.Create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Resources[0].Path, "dirty"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(w.Resources[1].Path); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{w.Resources[0].ID, w.Resources[0].Source, w.Resources[0].Path, filepath.Base(w.Resources[0].Source)} {
		n, err := s.Navigate(NavigationRequest{Key: w.Key, Repository: selector, ExpectedID: w.ID, ExpectedPath: w.Path})
		if err != nil || n.Path != choices[0].Path || len(n.Warnings) != 1 {
			t.Fatalf("navigate %q: %+v %v", selector, n, err)
		}
	}
	got, err := s.WorkspaceAt(w.Resources[0].Path)
	if err != nil || got == nil || got.ID != w.ID {
		t.Fatalf("workspace at checkout: %+v %v", got, err)
	}
	got, err = s.WorkspaceAt(root)
	if err != nil || got != nil {
		t.Fatalf("outside workspace: %+v %v", got, err)
	}
	if err := s.Registry.Delete(w.Key); err != nil {
		t.Fatal(err)
	}
	got, err = s.WorkspaceAt(w.Path)
	if err != nil || got != nil {
		t.Fatalf("unindexed origin: %+v %v", got, err)
	}
}
