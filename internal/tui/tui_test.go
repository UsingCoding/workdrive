package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/discovery"
	"github.com/usingcoding/workdrive/internal/domain"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "ctrl+n":
		return tea.KeyMsg{Type: tea.KeyCtrlN}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}
func TestInputUnicodeAndCursor(t *testing.T) {
	x := newInput()
	for _, msg := range []tea.KeyMsg{key("界"), key("a"), {Type: tea.KeyLeft}, key("β"), {Type: tea.KeyBackspace}} {
		x, _ = x.Update(msg)
	}
	if got := x.Value(); got != "界a" {
		t.Fatalf("got %q", got)
	}
}
func TestPickerFiltersAndSelects(t *testing.T) {
	p := newPicker("pick", []string{"alpha", "beta"}, []string{"Alpha one", "Beta two"})
	p.input, _ = p.input.Update(key("beta"))
	if matches := p.matches(); len(matches) != 1 || matches[0] != 1 {
		t.Fatalf("matches: %v", matches)
	}
	m, _ := p.Update(key("enter"))
	if got := m.(picker).selected; got != "beta" {
		t.Fatalf("selected %q", got)
	}
}
func TestWizardReviewDoesNotMutateUntilApproved(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	cfg.Repositories.Sources = nil
	s, err := app.Open(cfg, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	w := newWizard(ctx, s, app.NewRequest{Key: "TASK-1", Name: "Test workspace"}, nil, "", "")
	scanCmd := w.goNext()
	if scanCmd == nil {
		t.Fatal("wizard did not schedule repository scan")
	}
	m, _ := w.Update(scanCmd())
	w = m.(wizard)
	if w.step != 1 {
		t.Fatalf("identity step: %d", w.step)
	}
	w.goNext()
	if w.step != 2 {
		t.Fatalf("repo step: %d", w.step)
	}
	reviewCmd := w.goNext()
	if reviewCmd == nil {
		t.Fatal("review preflight did not run asynchronously")
	}
	m, _ = w.Update(reviewCmd())
	w = m.(wizard)
	if w.step != 3 || !w.plan.Safe() || w.plan.Workspace == nil {
		t.Fatalf("review failed: %+v", w.plan)
	}
	if _, err := os.Stat(w.plan.Workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("review modified workspace: %v", err)
	}
	m, cmd := w.Update(key("enter"))
	w = m.(wizard)
	if w.step != 4 || cmd == nil {
		t.Fatalf("confirmation did not start apply")
	}
	result := cmd().(tea.BatchMsg)[1]()
	m, _ = w.Update(result)
	w = m.(wizard)
	if w.err != nil {
		t.Fatal(w.err)
	}
	if w.step != 5 {
		t.Fatalf("result step: %d", w.step)
	}
	if _, err := s.Find("TASK-1"); err != nil {
		t.Fatalf("workspace not created: %v", err)
	}
}
func TestWizardPrefillAndAmbiguousNames(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	s, err := app.Open(cfg, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := discovery.Repository{Name: "service", DisplayName: "service [one]", Path: filepath.Join(root, "one", "service")}
	b := discovery.Repository{Name: "service", DisplayName: "service [two]", Path: filepath.Join(root, "two", "service")}
	w := newWizard(context.Background(), s, app.NewRequest{Key: "TASK-2"}, []string{"service"}, "origin/main", "")
	w.goNext()
	m, _ := w.Update(discovered{result: discovery.Result{Repositories: []discovery.Repository{a, b}}})
	w = m.(wizard)
	if len(w.selected) != 0 || len(w.warnings) == 0 || !strings.Contains(w.warnings[0], "ambiguous") {
		t.Fatalf("ambiguous repo silently selected: %+v", w)
	}
	w.prefill = []string{a.DisplayName}
	m, _ = w.Update(discovered{result: discovery.Result{Repositories: []discovery.Repository{a, b}}})
	w = m.(wizard)
	if got := w.selected[a.Path].Base; got != "origin/main" {
		t.Fatalf("base prefill: %q", got)
	}
}

func TestWizardWaitsForPrefilledRepositoryScan(t *testing.T) {
	w := newWizard(context.Background(), nil, app.NewRequest{Key: "TASK-1"}, []string{"api"}, "", "")
	w.step, w.loading, w.form = 1, true, nil
	m, _ := w.Update(key("enter"))
	w = m.(wizard)
	if w.step != 1 {
		t.Fatal("wizard skipped repository scan and prefilled choice")
	}
	repo := discovery.Repository{Name: "api", DisplayName: "api", Path: "/tmp/api"}
	m, _ = w.Update(discovered{result: discovery.Result{Repositories: []discovery.Repository{repo}}})
	w = m.(wizard)
	if len(w.selected) != 1 {
		t.Fatalf("prefill missing after scan: %+v", w.selected)
	}
	m, _ = w.Update(key("ctrl+n"))
	w = m.(wizard)
	if w.step != 2 {
		t.Fatalf("wizard did not advance after scan: %d", w.step)
	}
}

func TestFuzzyFilteringAndStateToggle(t *testing.T) {
	if !matchQuery("PLATFORM-1425 — Authentication Service", "plt1425 auth") || matchQuery("PLATFORM-1425", "unrelated") {
		t.Fatal("fuzzy matching failed")
	}
	d := newDashboard(context.Background(), nil)
	d.items = []*domain.Workspace{
		{Key: "ALPHA-1", Name: "First", State: domain.StateActive},
		{Key: "BETA-2", Name: "Second", State: domain.StateParked},
	}
	m, _ := d.Update(key("f"))
	d = m.(dashboard)
	if got := d.visible(); len(got) != 1 || got[0].Key != "ALPHA-1" {
		t.Fatalf("active filter: %+v", got)
	}
	m, _ = d.Update(key("f"))
	d = m.(dashboard)
	if got := d.visible(); len(got) != 1 || got[0].Key != "BETA-2" {
		t.Fatalf("parked filter: %+v", got)
	}
	d.query.SetValue("beta second")
	if got := d.visible(); len(got) != 1 {
		t.Fatalf("search: %+v", got)
	}
}

func TestBlockedCleanupCannotBeApproved(t *testing.T) {
	d := newDashboard(context.Background(), nil)
	d.action = "cleanup"
	plan := app.Plan{Kind: "cleanup", Workspace: &domain.Workspace{Key: "BLOCKED"}, Blockers: []string{"untracked files"}}
	m, _ := d.Update(prepared{plan: plan})
	d = m.(dashboard)
	if !strings.Contains(d.View(), "untracked files") {
		t.Fatal("blocker not displayed")
	}
	m, cmd := d.Update(key("y"))
	d = m.(dashboard)
	if cmd != nil || d.busy {
		t.Fatal("unsafe cleanup started")
	}
}

func TestEmptyWizardReachesReviewAndCancellation(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	s, err := app.Open(cfg, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := newWizard(context.Background(), s, app.NewRequest{Key: "CANCEL-1"}, nil, "", "")
	w.step = 2
	w.loadEdits()
	reviewCmd := w.goNext()
	if reviewCmd == nil {
		t.Fatal("review preflight did not run asynchronously")
	}
	m, _ := w.Update(reviewCmd())
	w = m.(wizard)
	if w.step != 3 || w.plan.Workspace == nil {
		t.Fatalf("review not reached: %d %v", w.step, w.status)
	}
	m, _ = w.Update(key("esc"))
	w = m.(wizard)
	if !w.done {
		t.Fatal("cancel did not exit")
	}
	if _, err := s.Find("CANCEL-1"); err == nil {
		t.Fatal("canceled wizard created workspace")
	}
}

func TestDashboardSelectsSpecificRepositoryForRemoval(t *testing.T) {
	d := newDashboard(context.Background(), nil)
	d.items = []*domain.Workspace{{Key: "KEY-1", Resources: []domain.Resource{{ID: "first", Source: "one"}, {ID: "second", Source: "two"}}}}
	m, _ := d.Update(key("z"))
	d = m.(dashboard)
	if d.action != "pick-remove" {
		t.Fatal("remove picker did not open")
	}
	m, _ = d.Update(tea.KeyMsg{Type: tea.KeyDown})
	d = m.(dashboard)
	if d.removeCursor != 1 || !strings.Contains(d.View(), "two") {
		t.Fatal("second repo was not selected")
	}
	m, _ = d.Update(key("esc"))
	d = m.(dashboard)
	if d.action != "" {
		t.Fatal("remove picker did not cancel")
	}
}

func TestWizardSelectsTwoRepositoriesAndEditsBranchBaseDestination(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	s, err := app.Open(cfg, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := filepath.Join(root, "sources", "one")
	second := filepath.Join(root, "sources", "two")
	for _, src := range []string{first, second} {
		if err := os.MkdirAll(src, 0700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "init", "-q", src).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
	}
	first, err = config.CanonicalPath(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err = config.CanonicalPath(second)
	if err != nil {
		t.Fatal(err)
	}
	w := newWizard(context.Background(), s, app.NewRequest{Key: "ISSUE-2"}, nil, "", "")
	w.goNext()
	m, _ := w.Update(discovered{result: discovery.Result{Repositories: []discovery.Repository{{Name: "one", DisplayName: "one", Path: first}, {Name: "two", DisplayName: "two", Path: second}}}})
	w = m.(wizard)
	w.draft.Repositories = []string{first, second}
	w.goNext()
	if len(w.choices()) != 2 {
		t.Fatalf("selected %d repos", len(w.choices()))
	}
	if w.step != 2 {
		t.Fatalf("worktree editing step: %d", w.step)
	}
	w.draft.Branch = "issue-2-one"
	w.draft.Base = "main"
	destination, err := config.CanonicalPath(filepath.Join(root, "workspaces", "ISSUE-2", "nested", "one"))
	if err != nil {
		t.Fatal(err)
	}
	w.draft.Destination = destination
	m, reviewCmd := w.Update(key("ctrl+n"))
	w = m.(wizard)
	if reviewCmd == nil {
		t.Fatal("review preflight did not run asynchronously")
	}
	m, _ = w.Update(reviewCmd())
	w = m.(wizard)
	if !w.plan.Safe() {
		t.Fatalf("review blockers: %v", w.plan.Blockers)
	}
	choice := w.plan.Add[0]
	if choice.Branch != "issue-2-one" || choice.Base != "main" || choice.Path != destination {
		t.Fatalf("edited choice lost: %+v", choice)
	}
	if _, err := os.Stat(w.plan.Workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("review modified destination: %v", err)
	}
}
