package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/discovery"
)

type discovered struct {
	result discovery.Result
	err    error
}
type applied struct{ err error }
type planned struct {
	plan app.Plan
	err  error
}
type progressTick struct{}

func nextProgress() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return progressTick{} })
}

type wizard struct {
	ctx            context.Context
	cancel         context.CancelFunc
	service        *app.Service
	addKey         string
	request        app.NewRequest
	initialChoices []app.RepositoryChoice
	prefill        []string
	defaultBase    string
	step           int // 0 identity, 1 repositories, 2 worktrees, 3 review, 4 applying, 5 result
	fields         []input
	focus          int
	search         input
	repositories   []discovery.Repository
	selected       map[string]app.RepositoryChoice
	existing       map[string]bool
	order          []string
	cursor         int
	editIndex      int
	edits          []input
	plan           app.Plan
	loading        bool
	warnings       []string
	status         string
	progress       int
	err            error
	done           bool
	height         int
}

func field(label, value string) input {
	t := newInput()
	t.Prompt = label + ": "
	t.SetValue(value)
	t.CharLimit = 2048
	return t
}
func newWizard(ctx context.Context, s *app.Service, req app.NewRequest, prefill []string, base, addKey string) wizard {
	search := newInput()
	search.Prompt = "Search repositories: "
	search.Placeholder = "name or path"
	w := wizard{ctx: ctx, service: s, request: req, initialChoices: append([]app.RepositoryChoice(nil), req.Repos...), prefill: prefill, defaultBase: base, addKey: addKey, selected: map[string]app.RepositoryChoice{}, existing: map[string]bool{}, height: 24, loading: addKey != "", search: search}
	w.fields = []input{field("Key", req.Key), field("Name", req.Name), field("Path", req.Path), field("Profile", req.Profile)}
	if addKey != "" {
		w.step = 1
		existing, err := s.Find(addKey)
		if err != nil {
			w.status = err.Error()
		} else {
			w.request.Profile = existing.Profile
			for _, r := range existing.Resources {
				w.existing[r.Source] = true
			}
		}
	}
	w.fields[w.focus].Focus()
	return w
}
func (w wizard) Init() tea.Cmd {
	if w.step == 0 {
		return nil
	}
	return w.scan()
}
func (w wizard) scan() tea.Cmd { return w.scanWith(false) }
func (w wizard) scanWith(fresh bool) tea.Cmd {
	profile := w.request.Profile
	if w.step == 0 {
		profile = strings.TrimSpace(w.fields[3].Value())
	}
	return func() tea.Msg {
		if fresh {
			return discovered{result: w.service.RefreshDiscovery(w.ctx, profile)}
		}
		return discovered{result: w.service.Discover(w.ctx, profile)}
	}
}
func (w *wizard) updateRequest() {
	w.request.Key = strings.TrimSpace(w.fields[0].Value())
	w.request.Name = strings.TrimSpace(w.fields[1].Value())
	w.request.Path = strings.TrimSpace(w.fields[2].Value())
	w.request.Profile = strings.TrimSpace(w.fields[3].Value())
}
func (w *wizard) choices() []app.RepositoryChoice {
	var choices []app.RepositoryChoice
	for _, source := range w.order {
		if c, ok := w.selected[source]; ok {
			choices = append(choices, c)
		}
	}
	return choices
}
func (w *wizard) loadEdits() {
	if w.editIndex >= len(w.order) {
		w.editIndex = 0
	}
	if len(w.order) == 0 {
		w.edits = nil
		return
	}
	c := w.selected[w.order[w.editIndex]]
	w.edits = []input{field("Branch", c.Branch), field("Base", c.Base), field("Destination", c.Path)}
	w.focus = 0
	w.edits[0].Focus()
}
func (w *wizard) saveEdits() {
	if len(w.edits) == 0 || len(w.order) == 0 {
		return
	}
	source := w.order[w.editIndex]
	c := w.selected[source]
	c.Branch = strings.TrimSpace(w.edits[0].Value())
	c.Base = strings.TrimSpace(w.edits[1].Value())
	c.Path = strings.TrimSpace(w.edits[2].Value())
	w.selected[source] = c
}
func (w wizard) planCommand() tea.Cmd {
	req, addKey, service, ctx := w.request, w.addKey, w.service, w.ctx
	return func() tea.Msg {
		if addKey != "" {
			plan, err := service.PlanAdd(ctx, addKey, req.Repos)
			return planned{plan: plan, err: err}
		}
		plan, err := service.PlanNew(req)
		return planned{plan: plan, err: err}
	}
}
func (w wizard) visible() []discovery.Repository {
	var out []discovery.Repository
	q := w.search.Value()
	for _, r := range w.repositories {
		if w.existing[r.Path] {
			continue
		}
		if matchQuery(r.DisplayName+" "+r.Path, q) {
			out = append(out, r)
		}
	}
	return out
}
func (w *wizard) goNext() {
	switch w.step {
	case 0:
		w.updateRequest()
		w.step = 1
		w.search.Focus()
		w.focus = 0
	case 1:
		w.step = 2
		w.search.Blur()
		w.loadEdits()
	case 2:
		w.step = 3
		w.saveEdits()
		w.updateRequest()
		w.request.Repos = w.choices()
		w.plan = app.Plan{}
		w.status = "Checking workspace and Git state…"
	}
}
func (w *wizard) goBack() {
	switch w.step {
	case 1:
		if w.addKey == "" {
			w.step = 0
			w.search.Blur()
			w.focus = 0
			w.fields[0].Focus()
		}
	case 2:
		w.saveEdits()
		w.step = 1
		w.search.Focus()
	case 3:
		w.step = 2
		w.loadEdits()
	}
}
func (w wizard) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		w.height = msg.Height
		return w, nil
	case discovered:
		w.loading = false
		w.repositories = msg.result.Repositories
		w.warnings = nil
		for _, warning := range msg.result.Warnings {
			w.warnings = append(w.warnings, warning.Message)
		}
		for _, choice := range w.initialChoices {
			if choice.Source != "" {
				w.selectRepo(choice.Source, choice)
			}
		}
		w.initialChoices = nil
		for _, term := range w.prefill {
			r, err := discovery.Resolve(w.repositories, term)
			if err != nil {
				w.warnings = append(w.warnings, err.Error())
				if strings.Contains(err.Error(), "ambiguous") && w.search.Value() == "" {
					w.search.SetValue(term)
				}
				continue
			}
			w.selectRepo(r.Path, app.RepositoryChoice{Source: r.Path, Name: r.Name, Base: w.defaultBase})
		}
		w.prefill = nil
		if msg.err != nil {
			w.status = msg.err.Error()
		}
		return w, nil
	case progressTick:
		if w.step == 4 {
			w.progress++
			return w, nextProgress()
		}
		return w, nil
	case planned:
		if w.step != 3 {
			return w, nil
		}
		w.plan = msg.plan
		w.status = ""
		if msg.err != nil {
			w.plan = app.Plan{}
			w.status = msg.err.Error()
		}
		return w, nil
	case applied:
		w.step = 5
		w.err = msg.err
		if msg.err != nil {
			w.status = msg.err.Error()
		} else {
			w.status = "Operation completed successfully"
		}
		return w, nil
	case tea.KeyMsg:
		k := msg.String()
		if w.step == 4 {
			if k == "esc" || k == "ctrl+c" {
				if w.cancel != nil {
					w.cancel()
					w.status = "Cancel requested; waiting for the current operation to finish safely"
				}
			}
			return w, nil
		}
		if k == "ctrl+c" || k == "esc" {
			w.done = true
			return w, tea.Quit
		}
		if w.step == 5 {
			if k == "enter" || k == "q" {
				w.done = true
				return w, tea.Quit
			}
			return w, nil
		}
		if k == "ctrl+r" {
			w.loading = true
			return w, w.scanWith(true)
		}
		if k == "ctrl+b" {
			w.goBack()
			return w, nil
		}
		switch w.step {
		case 0:
			if k == "tab" || k == "shift+tab" || k == "enter" {
				w.fields[w.focus].Blur()
				if k == "enter" && w.focus == 3 {
					w.goNext()
					w.loading = true
					return w, w.scan()
				}
				if k == "shift+tab" {
					w.focus = (w.focus + 3) % 4
				} else {
					w.focus = (w.focus + 1) % 4
				}
				w.fields[w.focus].Focus()
				return w, nil
			}
		case 1:
			if k == "ctrl+n" || k == "tab" {
				if w.loading {
					w.status = "Wait for repository discovery to finish"
					return w, nil
				}
				w.goNext()
				return w, nil
			}
			if k == "up" || k == "ctrl+p" {
				if w.cursor > 0 {
					w.cursor--
				}
				return w, nil
			}
			if k == "down" || k == "ctrl+j" {
				if w.cursor+1 < len(w.visible()) {
					w.cursor++
				}
				return w, nil
			}
			if k == " " || k == "space" {
				visible := w.visible()
				if w.cursor < len(visible) {
					r := visible[w.cursor]
					if _, exists := w.selected[r.Path]; exists {
						delete(w.selected, r.Path)
						for i, p := range w.order {
							if p == r.Path {
								w.order = append(w.order[:i], w.order[i+1:]...)
								break
							}
						}
					} else {
						w.selectRepo(r.Path, app.RepositoryChoice{Source: r.Path, Name: r.Name, Base: w.defaultBase})
					}
				}
				return w, nil
			}
			if k == "enter" {
				if w.loading {
					w.status = "Wait for repository discovery to finish"
					return w, nil
				}
				w.goNext()
				return w, nil
			}
		case 2:
			if k == "ctrl+n" || (k == "enter" && len(w.edits) == 0) {
				w.goNext()
				if w.addKey != "" && len(w.request.Repos) == 0 {
					w.status = "Select at least one new repository to add"
					return w, nil
				}
				return w, w.planCommand()
			}
			if k == "tab" || k == "shift+tab" || k == "enter" {
				if len(w.edits) > 0 {
					w.edits[w.focus].Blur()
					if k == "enter" && w.focus == 2 {
						w.saveEdits()
						if w.editIndex+1 >= len(w.order) {
							w.goNext()
							return w, w.planCommand()
						}
						w.editIndex++
						w.loadEdits()
						return w, nil
					}
					if k == "shift+tab" {
						w.focus = (w.focus + 2) % 3
					} else {
						w.focus = (w.focus + 1) % 3
					}
					w.edits[w.focus].Focus()
				}
				return w, nil
			}
			if k == "alt+left" {
				w.saveEdits()
				if w.editIndex > 0 {
					w.editIndex--
					w.loadEdits()
				}
				return w, nil
			}
			if k == "alt+right" {
				w.saveEdits()
				if w.editIndex+1 < len(w.order) {
					w.editIndex++
					w.loadEdits()
				}
				return w, nil
			}
		case 3:
			if k == "enter" || k == "y" {
				if w.plan.Workspace == nil || !w.plan.Safe() {
					return w, nil
				}
				w.step = 4
				ctx, cancel := context.WithCancel(w.ctx)
				w.cancel = cancel
				plan := w.plan
				return w, tea.Batch(nextProgress(), func() tea.Msg {
					defer cancel()
					if plan.Kind == "add" {
						_, err := w.service.Add(ctx, plan)
						return applied{err: err}
					}
					_, err := w.service.Create(ctx, plan)
					return applied{err: err}
				})
			}
			if k == "b" {
				w.goBack()
				return w, nil
			}
		}
	}
	var cmd tea.Cmd
	switch w.step {
	case 0:
		w.fields[w.focus], cmd = w.fields[w.focus].Update(message)
	case 1:
		old := w.search.Value()
		w.search, cmd = w.search.Update(message)
		if old != w.search.Value() {
			w.cursor = 0
		}
	case 2:
		if len(w.edits) > 0 {
			w.edits[w.focus], cmd = w.edits[w.focus].Update(message)
		}
	}
	return w, cmd
}
func (w *wizard) selectRepo(path string, c app.RepositoryChoice) {
	if _, found := w.selected[path]; !found {
		w.order = append(w.order, path)
	}
	w.selected[path] = c
}
func (w wizard) View() string {
	var b strings.Builder
	labels := []string{"Identity", "Repositories", "Worktrees", "Review", "Applying", "Result"}
	fmt.Fprintf(&b, "%s · %s\n\n", accentStyle.Render("Workdrive "+map[bool]string{true: "Add repositories", false: "New workspace"}[w.addKey != ""]), accentStyle.Render(labels[w.step]))
	switch w.step {
	case 0:
		for i, t := range w.fields {
			marker := "  "
			if i == w.focus {
				marker = accentStyle.Render("> ")
			}
			fmt.Fprintf(&b, "%s%s\n", marker, t.View())
		}
		b.WriteString("\n" + helpLine("Tab next field · Enter on Profile continues"))
	case 1:
		fmt.Fprintf(&b, "%s · %d selected\n", w.search.View(), len(w.choices()))
		if w.loading {
			b.WriteString(accentStyle.Render("Scanning local repositories…") + "\n")
		}
		visible := w.visible()
		if len(visible) == 0 {
			b.WriteString("No matching repositories (zero repositories is allowed).\n")
		}
		limit := w.height - 10
		if limit < 1 {
			limit = 1
		}
		if limit > len(visible) {
			limit = len(visible)
		}
		start := 0
		if w.cursor >= limit {
			start = w.cursor - limit + 1
		}
		for i := start; i < start+limit; i++ {
			r := visible[i]
			mark := " "
			if _, ok := w.selected[r.Path]; ok {
				mark = "x"
			}
			row := fmt.Sprintf("[%s] %s — %s", mark, r.DisplayName, r.Path)
			if mark == "x" && i != w.cursor {
				row = successStyle.Render(row)
			}
			b.WriteString(listRow(row, i == w.cursor) + "\n")
		}
		b.WriteString("\n" + helpLine("Space toggle · ↑/↓ move · Enter next · Ctrl+R refresh"))
	case 2:
		if len(w.order) == 0 {
			b.WriteString("No worktrees selected. Press Enter to review the workspace.\n")
		} else {
			c := w.selected[w.order[w.editIndex]]
			fmt.Fprintf(&b, "Repository %d/%d: %s\nSource: %s\n", w.editIndex+1, len(w.order), filepath.Base(c.Source), c.Source)
			for i, t := range w.edits {
				pointer := "  "
				if w.focus == i {
					pointer = accentStyle.Render("> ")
				}
				fmt.Fprintf(&b, "%s%s\n", pointer, t.View())
			}
			b.WriteString(mutedStyle.Render("Leave Branch / Destination empty for profile defaults. Base is a Git ref.") + "\n" + helpLine("Alt+←/→ previous/next repo · Enter on Destination next"))
		}
		b.WriteString(helpLine("Ctrl+N review"))
	case 3:
		if w.plan.Workspace != nil {
			p := w.plan
			fmt.Fprintf(&b, "Workspace: %s — %s\nPath: %s\nProfile: %s\nOperation: %s\n", p.Workspace.Key, p.Workspace.Name, p.Workspace.Path, p.Workspace.Profile, p.Kind)
			cfg, cfgErr := w.service.Config.Effective(p.Workspace.Profile)
			for i, c := range p.Add {
				branch, destination, base := c.Branch, c.Path, c.Base
				if cfgErr == nil {
					name := c.Name
					if name == "" {
						name = filepath.Base(c.Source)
					}
					if branch == "" {
						branch, _ = cfg.Branch(p.Workspace.Key, p.Workspace.Name, p.Workspace.Path, name, c.Source)
					}
					if destination == "" {
						destination, _ = cfg.WorktreePathWithContext(p.Workspace.Key, p.Workspace.Name, p.Workspace.Path, name, c.Source)
					}
					if base == "" {
						base = cfg.Worktree.BaseRef
					}
				}
				if p.Kind == "create" && i < len(p.Workspace.Resources) {
					branch, destination, base = p.Workspace.Resources[i].Branch, p.Workspace.Resources[i].Path, p.Workspace.Resources[i].Base
				}
				fmt.Fprintf(&b, "  Source: %s\n  Branch: %s  Base: %s  Destination: %s\n", c.Source, branch, base, destination)
			}
			if p.Kind == "add" {
				fmt.Fprintf(&b, "Existing worktrees remain unchanged: %d\n", len(p.Workspace.Resources))
			}
			for _, v := range p.Warnings {
				fmt.Fprintf(&b, "%s\n", warningStyle.Render("Warning: "+v))
			}
			for _, v := range p.Blockers {
				fmt.Fprintf(&b, "%s\n", dangerStyle.Render("BLOCKED: "+v))
			}
			if p.Safe() {
				b.WriteString("\n" + helpLine("Enter/y confirm and apply · b/Ctrl+B back"))
			} else {
				b.WriteString("\n" + dangerStyle.Render("Cannot apply while blocked") + " · " + helpLine("b/Ctrl+B back"))
			}
		}
	case 4:
		fmt.Fprintf(&b, "%s\n%s\n", accentStyle.Render(fmt.Sprintf("Applying changes in the background %s", strings.Repeat(".", 1+w.progress%3))), mutedStyle.Render("Esc requests cancellation; wait for the operation journal to settle."))
	case 5:
		b.WriteString(helpLine("Enter to close."))
	}
	if w.status != "" {
		style := accentStyle
		if w.step == 5 {
			style = successStyle
			if w.err != nil {
				style = dangerStyle
			}
		}
		fmt.Fprintf(&b, "\n%s\n", style.Render(w.status))
	}
	if len(w.warnings) > 0 {
		for _, v := range w.warnings[:min(3, len(w.warnings))] {
			fmt.Fprintf(&b, "%s\n", warningStyle.Render("Warning: "+v))
		}
	}
	b.WriteString(helpLine("Ctrl+B back · Esc cancel"))
	return b.String()
}
func NewWizard(ctx context.Context, s *app.Service, req app.NewRequest, repos []string, base string) error {
	m, err := run(ctx, newWizard(ctx, s, req, repos, base, ""))
	if err != nil {
		return err
	}
	return m.(wizard).err
}
func AddRepos(ctx context.Context, s *app.Service, key string) error {
	m, err := run(ctx, newWizard(ctx, s, app.NewRequest{}, nil, "", key))
	if err != nil {
		return err
	}
	return m.(wizard).err
}
