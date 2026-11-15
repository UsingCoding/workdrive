package tui

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/domain"
)

func navigationService(t *testing.T) *app.Service {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	cfg.Repositories.Sources = nil
	s, err := app.Open(cfg, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDashboardNavigationResultAndPreflight(t *testing.T) {
	s := navigationService(t)
	plan, err := s.PlanNew(app.NewRequest{Key: "target"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Park(w.Key)
	if err != nil {
		t.Fatal(err)
	}
	d := newDashboard(context.Background(), s)
	d.items = []*domain.Workspace{w}
	d.preflight = func() error { return errors.New("setup required") }
	m, cmd := d.Update(key("enter"))
	d = m.(dashboard)
	if cmd != nil || d.navigation != nil || d.status != "setup required" {
		t.Fatalf("blocked dashboard: %+v", d)
	}
	got, _ := s.Find(w.Key)
	if got.State != domain.StateParked {
		t.Fatal("preflight activated workspace")
	}
	d.preflight = func() error { return nil }
	m, cmd = d.Update(key("enter"))
	d = m.(dashboard)
	if d.navigation == nil || d.navigation.Path != w.Path || cmd == nil {
		t.Fatalf("no navigation result: %+v", d)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("navigation did not quit dashboard")
	}
	fresh := newDashboard(context.Background(), s)
	fresh.items = []*domain.Workspace{w}
	m, _ = fresh.Update(key("q"))
	if m.(dashboard).navigation != nil {
		t.Fatal("plain quit navigates")
	}
}

func TestWizardNavigationRequiresAcknowledgedActualSuccess(t *testing.T) {
	s := navigationService(t)
	plan, err := s.PlanNew(app.NewRequest{Key: "edited-key"})
	if err != nil {
		t.Fatal(err)
	}
	w := newWizard(context.Background(), s, app.NewRequest{}, nil, "", "")
	w.step, w.plan, w.switchAfter = 3, plan, true
	m, cmd := w.Update(key("enter"))
	w = m.(wizard)
	msg := cmd().(tea.BatchMsg)[1]()
	m, _ = w.Update(msg)
	w = m.(wizard)
	persisted, err := s.Find("edited-key")
	if err != nil {
		t.Fatal(err)
	}
	if w.workspace == nil || w.workspace.ID != persisted.ID || w.workspace.ID == plan.Workspace.ID {
		t.Fatal("wizard did not retain the actual replanned creation identity")
	}
	m, _ = w.Update(key("enter"))
	if !m.(wizard).navigate {
		t.Fatal("successful Enter did not authorize navigation")
	}
	for _, closeKey := range []string{"q", "esc"} {
		m, _ = w.Update(key(closeKey))
		if m.(wizard).navigate {
			t.Fatalf("%s authorized navigation", closeKey)
		}
	}
	m, _ = w.Update(applied{workspace: persisted, err: errors.New("partial creation")})
	m, _ = m.(wizard).Update(key("enter"))
	if m.(wizard).navigate {
		t.Fatal("partial workspace authorized navigation")
	}
	w.step, w.cancelRequested = 4, false
	m, _ = w.Update(key("esc"))
	m, _ = m.(wizard).Update(applied{workspace: persisted})
	m, _ = m.(wizard).Update(key("enter"))
	if m.(wizard).navigate {
		t.Fatal("late creation success after cancellation authorized navigation")
	}
}
