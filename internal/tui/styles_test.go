package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/discovery"
	"github.com/usingcoding/workdrive/internal/domain"
)

func TestViewsPreserveTextAcrossColorProfiles(t *testing.T) {
	profile, dark := lipgloss.ColorProfile(), lipgloss.HasDarkBackground()
	t.Cleanup(func() {
		lipgloss.SetColorProfile(profile)
		lipgloss.SetHasDarkBackground(dark)
	})
	d := newDashboard(context.Background(), nil)
	d.items = []*domain.Workspace{
		{Key: "TASK-1", Name: "Selected workspace", State: domain.StateActive, ActualStatus: domain.HealthUnknown},
		{Key: "TASK-2", Name: "Other workspace", State: domain.StateParked, ActualStatus: domain.HealthError},
	}
	d.warnings = []string{"missing worktree"}
	d.confirmation = "cleanup"
	d.pending = app.Plan{Workspace: d.items[0], Blockers: []string{"untracked files"}}
	w := newWizard(context.Background(), nil, app.NewRequest{Key: "TASK-1"}, nil, "", "")
	repos := w
	repos.step = 1
	repos.repositories = []discovery.Repository{{DisplayName: "api", Path: "/sources/api"}}
	repos.selected = map[string]app.RepositoryChoice{"/sources/api": {Source: "/sources/api"}}
	repos.order = []string{"/sources/api"}
	repos.form = repos.repositoryForm()
	views := []struct {
		name  string
		model interface{ View() string }
	}{
		{"dashboard", d},
		{"wizard identity", w},
		{"wizard repositories", repos},
		{"picker", newPicker("Select workspace", []string{"TASK-1"}, []string{"TASK-1"})},
		{"confirmation", confirmation{message: "Delete workspace?"}},
		{"notice", notice("Cleanup is blocked.")},
	}
	for _, view := range views {
		t.Run(view.name, func(t *testing.T) {
			lipgloss.SetColorProfile(termenv.Ascii)
			plain := view.model.View()
			if strings.Contains(plain, "\x1b[") {
				t.Fatal("no-color view contains ANSI escapes")
			}
			for _, dark := range []bool{true, false} {
				lipgloss.SetHasDarkBackground(dark)
				for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI} {
					lipgloss.SetColorProfile(profile)
					colored := view.model.View()
					if !strings.Contains(colored, "\x1b[") {
						t.Fatalf("no styles for profile %v, dark=%v", profile, dark)
					}
					if got := ansi.Strip(colored); got != plain {
						t.Fatalf("styling changed text or alignment:\ngot  %q\nwant %q", got, plain)
					}
				}
			}
		})
	}
}

func TestWithoutColorRestoresTerminalProfile(t *testing.T) {
	profile := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(profile) })
	lipgloss.SetColorProfile(termenv.TrueColor)
	restore := configureColors(WithoutColor(context.Background()))
	if got := helpLine("n new · x cleanup"); strings.Contains(got, "\x1b[") {
		t.Fatalf("--no-color left styles enabled: %q", got)
	}
	restore()
	if lipgloss.ColorProfile() != termenv.TrueColor {
		t.Fatal("terminal color profile was not restored")
	}
}

func TestSelectionAndControlsHaveDistinctStyles(t *testing.T) {
	profile, dark := lipgloss.ColorProfile(), lipgloss.HasDarkBackground()
	t.Cleanup(func() {
		lipgloss.SetColorProfile(profile)
		lipgloss.SetHasDarkBackground(dark)
	})
	lipgloss.SetColorProfile(termenv.TrueColor)
	for _, dark := range []bool{true, false} {
		lipgloss.SetHasDarkBackground(dark)
		if selected, normal := listRow("workspace", true), listRow("workspace", false); selected == normal || !strings.Contains(selected, ";48;2;") {
			t.Fatalf("selected row lacks a contrasting background: %q", selected)
		}
		help := helpLine("n new · x cleanup · z remove · q quit")
		for _, styled := range []string{accentStyle.Render("n"), dangerStyle.Render("x"), dangerStyle.Render("z"), mutedStyle.Render("cleanup")} {
			if !strings.Contains(help, styled) {
				t.Fatalf("help is missing distinct style %q: %q", styled, help)
			}
		}
		for _, status := range []struct {
			value string
			style lipgloss.Style
		}{
			{"active", successStyle}, {"healthy", successStyle},
			{"parked", mutedStyle}, {"unknown", mutedStyle},
			{"missing", warningStyle}, {"conflict", warningStyle},
			{"attention_required", warningStyle}, {"error", dangerStyle},
		} {
			if got := statusText(status.value); got != status.style.Render(status.value) {
				t.Errorf("wrong style for %s: %q", status.value, got)
			}
		}
	}
}
