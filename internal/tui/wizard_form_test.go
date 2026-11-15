package tui

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/config"
)

type wizardSnapshot struct {
	step, selected int
	field, view    string
	ready          bool
	plan           app.Plan
}

type wizardTestModel struct {
	wizard
	updates chan wizardSnapshot
}

func (m wizardTestModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.wizard.Update(msg)
	m.wizard = model.(wizard)
	snapshot := wizardSnapshot{step: m.step, selected: len(m.draft.Repositories), view: m.View(), ready: m.form != nil, plan: m.plan}
	if m.form != nil && m.form.GetFocusedField() != nil {
		snapshot.field = m.form.GetFocusedField().GetKey()
	}
	select {
	case m.updates <- snapshot:
	default:
	}
	return m, cmd
}

// Exercise Huh through a real event loop so its navigation, batches and focus
// commands run exactly as they do in the dashboard and standalone wizard.
func TestHuhWizardKeyboardFlow(t *testing.T) {
	for _, withRepo := range []bool{false, true} {
		name := "zero repositories"
		if withRepo {
			name = "repository selection and overrides"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			cfg := config.Defaults()
			cfg.Workspace.Root = filepath.Join(root, "workspaces")
			cfg.Repositories.Sources = nil
			cfg.Worktree.BaseRef = "main"
			if withRepo {
				source := filepath.Join(root, "sources", "api")
				cfg.Repositories.Sources = []config.Source{{Name: "sources", Path: filepath.Dir(source), MaxDepth: 1}}
				for _, args := range [][]string{
					{"init", "-q", "-b", "main", source},
					{"-C", source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-q", "--allow-empty", "-m", "initial"},
				} {
					if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
						t.Fatalf("git: %v: %s", err, out)
					}
				}
			}
			s, err := app.Open(cfg, filepath.Join(root, "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			updates := make(chan wizardSnapshot, 64)
			w := newWizard(context.Background(), s, app.NewRequest{Key: "TASK-1"}, nil, "", "")
			program := tea.NewProgram(wizardTestModel{wizard: w, updates: updates}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
			done := make(chan error, 1)
			go func() {
				_, err := program.Run()
				done <- err
			}()
			t.Cleanup(func() { program.Kill(); program.Wait() })
			wait := func(label string, match func(wizardSnapshot) bool) wizardSnapshot {
				t.Helper()
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				var last wizardSnapshot
				for {
					select {
					case snapshot := <-updates:
						last = snapshot
						if match(snapshot) {
							return snapshot
						}
					case <-timer.C:
						t.Fatalf("waiting for %s; step=%d field=%s:\n%s", label, last.step, last.field, last.view)
					case err := <-done:
						t.Fatalf("program exited while waiting for %s: %v", label, err)
					}
				}
			}
			field := func(name string) wizardSnapshot {
				t.Helper()
				return wait(name, func(s wizardSnapshot) bool { return s.field == name })
			}
			program.Send(tea.WindowSizeMsg{Width: 110, Height: 36})
			field("key")
			program.Send(key("tab"))
			if snapshot := field("name"); !strings.Contains(snapshot.view, "TASK-1") {
				t.Fatal("name is missing its workspace-key hint")
			}
			program.Send(key("API playground"))
			program.Send(key("tab"))
			snapshot := field("path")
			if !strings.Contains(snapshot.view, "workspaces/TASK-1") {
				t.Fatalf("workspace path is missing its hint:\n%s", snapshot.view)
			}
			program.Send(key("tab"))
			if snapshot := field("profile"); !strings.Contains(snapshot.view, "development") {
				t.Fatal("profile is missing its default hint")
			}
			program.Send(key("enter"))
			wait("repository discovery", func(s wizardSnapshot) bool { return s.step == 1 && s.ready })
			if withRepo {
				program.Send(key("/"))
				program.Send(key("api"))
				program.Send(key("enter")) // Accept the filter without submitting.
				program.Send(key("space"))
				wait("repository selected", func(s wizardSnapshot) bool { return s.selected == 1 })
				program.Send(key("ctrl+n"))
				if snapshot := field("branch"); !strings.Contains(snapshot.view, "TASK-1") || !strings.Contains(snapshot.view, "main") || !strings.Contains(snapshot.view, "TASK-1/api") {
					t.Fatalf("worktree fields are missing defaults:\n%s", snapshot.view)
				}
				program.Send(key("custom-api"))
				program.Send(key("tab"))
				field("base")
				program.Send(key("main"))
				program.Send(key("tab"))
				field("destination")
			} else {
				program.Send(key("enter"))
				wait("empty worktree form", func(s wizardSnapshot) bool { return s.step == 2 && s.ready })
			}
			program.Send(key("enter"))
			snapshot = wait("safe review", func(s wizardSnapshot) bool { return s.step == 3 && s.plan.Workspace != nil })
			if !snapshot.plan.Safe() || snapshot.plan.Workspace.Name != "API playground" {
				t.Fatalf("unexpected review plan: %+v", snapshot.plan)
			}
			if withRepo && (len(snapshot.plan.Add) != 1 || snapshot.plan.Add[0].Branch != "custom-api" || snapshot.plan.Add[0].Base != "main" || snapshot.plan.Add[0].Path != "") {
				t.Fatalf("lost overrides or saved a default as an override: %+v", snapshot.plan.Add)
			}
			if workspaces, _, err := s.List(); err != nil || len(workspaces) != 0 {
				t.Fatalf("form navigation created a workspace: %v %v", workspaces, err)
			}
			program.Send(key("esc"))
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("wizard did not exit on Esc")
			}
		})
	}
}
