package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/domain"
)

type listed struct {
	items    []*domain.Workspace
	warnings []string
	err      error
}
type inspected struct {
	workspace *domain.Workspace
	warnings  []string
	err       error
}
type prepared struct {
	plan app.Plan
	err  error
}
type operationDone struct{ err error }
type shellDone struct{ err error }
type dashboard struct {
	ctx          context.Context
	service      *app.Service
	items        []*domain.Workspace
	warnings     []string
	cursor       int
	filter       string
	query        input
	search       bool
	detail       bool
	help         bool
	inspected    *domain.Workspace
	loading      bool
	status       string
	height       int
	wizard       *wizard
	pending      app.Plan
	confirmation string
	action       string
	removeCursor int
	busy         bool
	progress     int
	cancel       context.CancelFunc
}

func newDashboard(ctx context.Context, s *app.Service) dashboard {
	query := newInput()
	query.Prompt = "Search: "
	query.Placeholder = "key, title, path"
	return dashboard{ctx: ctx, service: s, filter: "all", query: query, height: 24}
}
func (d dashboard) Init() tea.Cmd { return d.load() }
func (d dashboard) load() tea.Cmd {
	return func() tea.Msg {
		ws, warnings, err := d.service.List()
		return listed{items: ws, warnings: warnings, err: err}
	}
}
func (d dashboard) visible() []*domain.Workspace {
	var out []*domain.Workspace
	q := d.query.Value()
	for _, w := range d.items {
		if d.filter != "all" && string(w.State) != d.filter {
			continue
		}
		if !matchQuery(w.Key+" "+w.Name+" "+w.Path, q) {
			continue
		}
		out = append(out, w)
	}
	return out
}
func (d dashboard) current() *domain.Workspace {
	list := d.visible()
	if d.cursor < 0 || d.cursor >= len(list) {
		return nil
	}
	return list[d.cursor]
}
func (d dashboard) prepare(kind string, key string, selector string) tea.Cmd {
	return func() tea.Msg {
		var p app.Plan
		var err error
		switch kind {
		case "cleanup":
			p, err = d.service.PlanCleanup(d.ctx, key)
		case "remove":
			p, err = d.service.PlanRemove(d.ctx, key, selector)
		}
		return prepared{plan: p, err: err}
	}
}
func (d *dashboard) refreshCurrent() tea.Cmd {
	w := d.current()
	if w == nil {
		return nil
	}
	key := w.Key
	return func() tea.Msg {
		ws, warnings, err := d.service.Inspect(d.ctx, key)
		return inspected{workspace: ws, warnings: warnings, err: err}
	}
}
func (d dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if d.wizard != nil {
		m, cmd := d.wizard.Update(msg)
		w := m.(wizard)
		d.wizard = &w
		if w.done {
			d.wizard = nil
			d.status = w.status
			return d, d.load()
		}
		return d, cmd
	}
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		d.height = v.Height
		return d, nil
	case listed:
		d.loading = false
		d.items = v.items
		d.warnings = v.warnings
		if v.err != nil {
			d.status = v.err.Error()
		}
		if d.cursor >= len(d.visible()) {
			d.cursor = max(0, len(d.visible())-1)
		}
		return d, nil
	case inspected:
		if v.err != nil {
			d.status = v.err.Error()
		} else {
			d.inspected = v.workspace
			d.warnings = v.warnings
		}
		return d, nil
	case prepared:
		d.loading = false
		if v.err != nil {
			d.status = v.err.Error()
			return d, nil
		}
		d.pending = v.plan
		d.confirmation = d.action
		if !v.plan.Safe() {
			d.status = "Operation blocked: " + strings.Join(v.plan.Blockers, "; ")
		}
		return d, nil
	case progressTick:
		if d.busy {
			d.progress++
			return d, nextProgress()
		}
		return d, nil
	case operationDone:
		d.busy = false
		d.cancel = nil
		if v.err != nil {
			d.status = "Operation stopped: " + v.err.Error()
		} else {
			d.status = "Operation completed"
		}
		d.inspected = nil
		return d, d.load()
	case shellDone:
		if v.err != nil {
			d.status = v.err.Error()
		}
		return d, d.load()
	case tea.KeyMsg:
		k := v.String()
		if d.busy {
			if k == "esc" || k == "ctrl+c" {
				if d.cancel != nil {
					d.cancel()
				}
				d.status = "Cancel requested; waiting for safe completion"
			}
			return d, nil
		}
		if d.confirmation != "" {
			if k == "esc" || k == "n" || k == "N" {
				d.confirmation = ""
				d.pending = app.Plan{}
				return d, nil
			}
			if (k == "y" || k == "Y") && d.pending.Safe() {
				plan := d.pending
				action := d.confirmation
				d.confirmation = ""
				d.pending = app.Plan{}
				ctx, cancel := context.WithCancel(d.ctx)
				d.cancel = cancel
				d.busy = true
				return d, tea.Batch(nextProgress(), func() tea.Msg {
					defer cancel()
					if action == "cleanup" {
						return operationDone{err: d.service.Cleanup(ctx, plan)}
					}
					return operationDone{err: d.service.Remove(ctx, plan)}
				})
			}
			return d, nil
		}
		if d.action == "pick-remove" {
			w := d.current()
			if w == nil || len(w.Resources) == 0 {
				d.action = ""
				return d, nil
			}
			switch k {
			case "esc":
				d.action = ""
				return d, nil
			case "up", "k":
				if d.removeCursor > 0 {
					d.removeCursor--
				}
				return d, nil
			case "down", "j":
				if d.removeCursor+1 < len(w.Resources) {
					d.removeCursor++
				}
				return d, nil
			case "enter":
				d.action = "remove"
				d.loading = true
				return d, d.prepare("remove", w.Key, w.Resources[d.removeCursor].ID)
			}
			return d, nil
		}
		if d.search {
			if k == "esc" || k == "enter" {
				d.search = false
				d.query.Blur()
				return d, nil
			}
			var cmd tea.Cmd
			old := d.query.Value()
			d.query, cmd = d.query.Update(msg)
			if old != d.query.Value() {
				d.cursor = 0
			}
			return d, cmd
		}
		if k == "q" || k == "ctrl+c" {
			return d, tea.Quit
		}
		if k == "?" {
			d.help = !d.help
			return d, nil
		}
		if k == "/" {
			d.search = true
			d.query.Focus()
			return d, nil
		}
		if k == "f" {
			switch d.filter {
			case "all":
				d.filter = "active"
			case "active":
				d.filter = "parked"
			default:
				d.filter = "all"
			}
			d.cursor = 0
			return d, nil
		}
		if k == "up" || k == "k" {
			if d.cursor > 0 {
				d.cursor--
			}
			d.inspected = nil
			return d, nil
		}
		if k == "down" || k == "j" {
			if d.cursor+1 < len(d.visible()) {
				d.cursor++
			}
			d.inspected = nil
			return d, nil
		}
		if k == "n" {
			w := newWizard(d.ctx, d.service, app.NewRequest{}, nil, "", "")
			d.wizard = &w
			return d, w.Init()
		}
		if k == "r" {
			d.loading = true
			return d, func() tea.Msg {
				_, err := d.service.Refresh(d.ctx)
				if err != nil {
					return listed{err: err}
				}
				return d.load()()
			}
		}
		w := d.current()
		if w == nil {
			return d, nil
		}
		switch k {
		case "d", "i":
			d.detail = !d.detail
			if d.detail {
				return d, d.refreshCurrent()
			}
			return d, nil
		case "a":
			wizard := newWizard(d.ctx, d.service, app.NewRequest{}, nil, "", w.Key)
			d.wizard = &wizard
			return d, wizard.Init()
		case "p":
			key := w.Key
			d.busy = true
			return d, tea.Batch(nextProgress(), func() tea.Msg { _, err := d.service.Park(key); return operationDone{err: err} })
		case "x":
			d.action = "cleanup"
			d.loading = true
			return d, d.prepare("cleanup", w.Key, "")
		case "z":
			if len(w.Resources) == 0 {
				d.status = "No repositories to remove"
				return d, nil
			}
			d.action = "pick-remove"
			d.removeCursor = 0
			return d, nil
		case "enter":
			key := w.Key
			workspace, warnings, err := d.service.Switch(key)
			if err != nil {
				d.status = err.Error()
				return d, nil
			}
			d.warnings = warnings
			shell := os.Getenv("SHELL")
			if shell == "" {
				shell = "/bin/sh"
			}
			command := exec.Command(shell, "-i")
			command.Dir = workspace.Path
			return d, tea.ExecProcess(command, func(err error) tea.Msg { return shellDone{err: err} })
		}
	}
	return d, nil
}
func (d dashboard) View() string {
	if d.wizard != nil {
		return d.wizard.View()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s   %s   %s active   %s parked   %s\n\n",
		accentStyle.Render("WORKDRIVE"), mutedStyle.Render(fmt.Sprintf("%d workspaces", len(d.items))),
		successStyle.Render(fmt.Sprint(d.count("active"))), mutedStyle.Render(fmt.Sprint(d.count("parked"))),
		accentStyle.Render("filter: "+d.filter))
	fmt.Fprintf(&b, "%s\n", d.query.View())
	if d.loading {
		b.WriteString(accentStyle.Render("Loading local state…") + "\n")
	}
	visible := d.visible()
	if len(visible) == 0 {
		b.WriteString(mutedStyle.Render("No matching workspaces.") + " " + helpLine("n create one"))
	}
	limit := d.height - 11
	if limit < 1 {
		limit = 1
	}
	if limit > len(visible) {
		limit = len(visible)
	}
	start := 0
	if d.cursor >= limit {
		start = d.cursor - limit + 1
	}
	for i := start; i < start+limit; i++ {
		w := visible[i]
		row := fmt.Sprintf("%-22s %-9s %-9s %2d repos  %s", w.Key, w.State, w.ActualStatus, len(w.Resources), w.Name)
		if i != d.cursor {
			row = fmt.Sprintf("%s %s %s %s  %s", textStyle.Bold(true).Render(fmt.Sprintf("%-22s", w.Key)),
				statusText(string(w.State))+strings.Repeat(" ", max(0, 9-len(w.State))),
				statusText(string(w.ActualStatus))+strings.Repeat(" ", max(0, 9-len(w.ActualStatus))),
				mutedStyle.Render(fmt.Sprintf("%2d repos", len(w.Resources))), w.Name)
		}
		b.WriteString(listRow(row, i == d.cursor) + "\n")
	}
	if d.detail {
		w := d.inspected
		if w == nil {
			w = d.current()
		}
		if w != nil {
			fmt.Fprintf(&b, "\n%s — %s\n%s %s\n%s %s · State: %s · Health: %s\n", accentStyle.Render(w.Key), textStyle.Render(w.Name), mutedStyle.Render("Path:"), textStyle.Render(w.Path), mutedStyle.Render("Profile:"), w.Profile, statusText(string(w.State)), statusText(string(w.ActualStatus)))
			for _, r := range w.Resources {
				fmt.Fprintf(&b, "  %s → %s\n    branch %s · %s\n", mutedStyle.Render(r.Source), textStyle.Render(r.Path), accentStyle.Render(r.Branch), statusText(string(r.ActualStatus)))
			}
		}
	}
	if d.action == "pick-remove" {
		if w := d.current(); w != nil {
			b.WriteString("\n" + warningStyle.Render("Choose worktree to remove:") + "\n")
			for i, r := range w.Resources {
				b.WriteString(listRow(fmt.Sprintf("%s (%s)", r.Source, r.Path), i == d.removeCursor) + "\n")
			}
			b.WriteString(helpLine("↑/↓ select · Enter preview · Esc cancel"))
		}
	}
	if d.confirmation != "" {
		fmt.Fprintf(&b, "\n%s\n", dangerStyle.Render(fmt.Sprintf("%s %s?", strings.ToUpper(d.confirmation), d.pending.Workspace.Key)))
		for _, r := range d.pending.Remove {
			fmt.Fprintf(&b, "Delete worktree: %s\nPreserve branch: %s\n", r.Path, r.Branch)
		}
		for _, blocker := range d.pending.Blockers {
			fmt.Fprintf(&b, "%s\n", dangerStyle.Render("BLOCKED: "+blocker))
		}
		for _, warning := range d.pending.Warnings {
			fmt.Fprintf(&b, "%s\n", warningStyle.Render("Warning: "+warning))
		}
		if d.pending.Safe() {
			b.WriteString(helpLine("y confirm · n cancel"))
		} else {
			b.WriteString(helpLine("Esc cancel (approval disabled)"))
		}
	}
	if d.busy {
		fmt.Fprintf(&b, "\n%s\n", accentStyle.Render(fmt.Sprintf("Operation in progress %s Esc requests cancellation.", strings.Repeat(".", 1+d.progress%3))))
	}
	if d.status != "" {
		fmt.Fprintf(&b, "\n%s\n", accentStyle.Render(d.status))
	}
	for _, warning := range d.warnings[:min(2, len(d.warnings))] {
		fmt.Fprintf(&b, "%s\n", warningStyle.Render("Warning: "+warning))
	}
	if d.help {
		b.WriteString("\n" + helpLine("↑↓/jk select · / search · f filter · d details · n new · a add · p park · x cleanup · z remove repo · Enter switch · r refresh · q quit"))
	}
	b.WriteString("\n" + helpLine("n new · Enter switch · d detail · a add · p park · x cleanup · z remove · r refresh · / search · f filter · ? help · q quit"))
	return b.String()
}
func (d dashboard) count(state string) int {
	n := 0
	for _, w := range d.items {
		if string(w.State) == state {
			n++
		}
	}
	return n
}
func Dashboard(ctx context.Context, s *app.Service) error {
	_, err := run(ctx, newDashboard(ctx, s))
	return err
}
