// Package tui provides the interactive workspace interface.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/discovery"
)

// ErrCancelled reports that the user dismissed a picker without a selection.
var ErrCancelled = errors.New("selection canceled")

func run(ctx context.Context, model tea.Model) (tea.Model, error) {
	return tea.NewProgram(model, tea.WithContext(ctx)).Run()
}

type picker struct {
	title          string
	values, labels []string
	input          input
	cursor         int
	selected       string
	cancelled      bool
	height         int
}

func newPicker(title string, values, labels []string) picker {
	input := newInput()
	input.Placeholder = "filter by key, title or path"
	input.Focus()
	return picker{title: title, values: values, labels: labels, input: input, height: 20}
}
func (p picker) Init() tea.Cmd { return nil }
func (p picker) matches() []int {
	var indices []int
	query := p.input.Value()
	for i, label := range p.labels {
		if matchQuery(label, query) {
			indices = append(indices, i)
		}
	}
	return indices
}
func (p picker) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		p.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			p.cancelled = true
			return p, tea.Quit
		case "enter":
			matches := p.matches()
			if len(matches) > 0 {
				if p.cursor >= len(matches) {
					p.cursor = len(matches) - 1
				}
				p.selected = p.values[matches[p.cursor]]
				return p, tea.Quit
			}
		case "up", "ctrl+p":
			if p.cursor > 0 {
				p.cursor--
			}
			return p, nil
		case "down", "ctrl+n":
			if p.cursor+1 < len(p.matches()) {
				p.cursor++
			}
			return p, nil
		}
	}
	var cmd tea.Cmd
	old := p.input.Value()
	p.input, cmd = p.input.Update(message)
	if old != p.input.Value() {
		p.cursor = 0
	}
	return p, cmd
}
func (p picker) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n", p.title, p.input.View())
	matches := p.matches()
	if len(matches) == 0 {
		b.WriteString("No matches\n")
	}
	limit := p.height - 5
	if limit < 1 {
		limit = 1
	}
	if limit > len(matches) {
		limit = len(matches)
	}
	start := 0
	if p.cursor >= limit {
		start = p.cursor - limit + 1
	}
	for _, i := range matches[start : start+limit] {
		marker := "  "
		if i == matches[p.cursor] {
			marker = "> "
		}
		fmt.Fprintf(&b, "%s%s\n", marker, p.labels[i])
	}
	b.WriteString("↑/↓ choose  enter select  esc cancel\n")
	return b.String()
}
func choose(ctx context.Context, p picker) (string, error) {
	result, err := run(ctx, p)
	if err != nil {
		return "", err
	}
	chosen := result.(picker)
	if chosen.cancelled {
		return "", ErrCancelled
	}
	return chosen.selected, nil
}
func PickRepository(ctx context.Context, repos []discovery.Repository) (discovery.Repository, error) {
	var values, labels []string
	for _, r := range repos {
		values = append(values, r.Path)
		labels = append(labels, r.DisplayName+" — "+r.Path)
	}
	selected, err := choose(ctx, newPicker("Select source repository", values, labels))
	if err != nil {
		return discovery.Repository{}, err
	}
	for _, r := range repos {
		if r.Path == selected {
			return r, nil
		}
	}
	return discovery.Repository{}, nil
}
func PickWorkspace(ctx context.Context, s *app.Service) (string, error) {
	ws, _, err := s.List()
	if err != nil {
		return "", err
	}
	var values, labels []string
	for _, w := range ws {
		values = append(values, w.Key)
		labels = append(labels, fmt.Sprintf("%-20s %-9s %s — %s", w.Key, w.State, w.Name, w.Path))
	}
	return choose(ctx, newPicker("Select workspace", values, labels))
}
func PickRepo(ctx context.Context, s *app.Service, key string) (string, error) {
	w, err := s.Find(key)
	if err != nil {
		return "", err
	}
	var values, labels []string
	for _, r := range w.Resources {
		values = append(values, r.ID)
		labels = append(labels, fmt.Sprintf("%s — %s (%s)", r.Source, r.Path, r.Branch))
	}
	return choose(ctx, newPicker("Select repository to remove from "+w.Key, values, labels))
}

type confirmation struct {
	message  string
	approved bool
	finished bool
}

func (m confirmation) Init() tea.Cmd { return nil }
func (m confirmation) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "y", "Y":
			m.approved = true
			m.finished = true
			return m, tea.Quit
		case "n", "N", "esc", "ctrl+c":
			m.finished = true
			return m, tea.Quit
		}
	}
	return m, nil
}
func (m confirmation) View() string { return m.message + "\nConfirm? [y/N] " }
func Confirm(ctx context.Context, message string) (bool, error) {
	result, err := run(ctx, confirmation{message: message})
	if err != nil {
		return false, err
	}
	return result.(confirmation).approved, nil
}

type notice string

func (m notice) Init() tea.Cmd { return nil }
func (m notice) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		return m, tea.Quit
	}
	return m, nil
}
func (m notice) View() string { return string(m) + "\nPress any key to close.\n" }

func PreviewCleanup(ctx context.Context, plan app.Plan) (bool, error) {
	if plan.Workspace == nil {
		return false, fmt.Errorf("cleanup plan has no workspace")
	}
	var b strings.Builder
	w := plan.Workspace
	fmt.Fprintf(&b, "Delete workspace %s (%s)\nPath: %s\n", w.Key, w.Name, w.Path)
	for _, r := range plan.Remove {
		fmt.Fprintf(&b, "Remove owned worktree: %s\n  source: %s\n  preserve branch: %s\n", r.Path, r.Source, r.Branch)
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", warning)
	}
	for _, blocker := range plan.Blockers {
		fmt.Fprintf(&b, "BLOCKED: %s\n", blocker)
	}
	if !plan.Safe() {
		_, err := run(ctx, notice(b.String()+"Cleanup is blocked."))
		return false, err
	}
	return Confirm(ctx, b.String()+"This cannot be undone. Original repositories and branches are preserved.")
}
