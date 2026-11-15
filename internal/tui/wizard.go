package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
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
	draft          *wizardDraft
	form           *huh.Form
	identity       []*huh.Input
	destination    *huh.Input
	defaults       worktreeDefaults
	repositories   []discovery.Repository
	selected       map[string]app.RepositoryChoice
	existing       map[string]bool
	order          []string
	editIndex      int
	plan           app.Plan
	loading        bool
	warnings       []string
	status         string
	progress       int
	err            error
	done           bool
	width, height  int
}

func newWizard(ctx context.Context, s *app.Service, req app.NewRequest, prefill []string, base, addKey string) wizard {
	w := wizard{
		ctx: ctx, service: s, request: req, initialChoices: append([]app.RepositoryChoice(nil), req.Repos...),
		prefill: prefill, defaultBase: base, addKey: addKey, selected: map[string]app.RepositoryChoice{},
		existing: map[string]bool{}, width: 80, height: 24,
		draft: &wizardDraft{Key: req.Key, Name: req.Name, Path: req.Path, Profile: req.Profile},
	}
	if addKey != "" {
		w.step = 1
		w.loading = true
		existing, err := s.Find(addKey)
		if err != nil {
			w.status = err.Error()
		} else {
			w.request = app.NewRequest{Key: existing.Key, Name: existing.Name, Path: existing.Path, Profile: existing.Profile}
			for _, r := range existing.Resources {
				w.existing[r.Source] = true
			}
		}
	} else {
		w.form = w.identityForm()
	}
	return w
}
func (w wizard) Init() tea.Cmd {
	if w.step == 1 {
		return w.scanWith(false)
	}
	return w.form.Init()
}
func (w wizard) scanWith(fresh bool) tea.Cmd {
	profile := w.request.Profile
	return func() tea.Msg {
		if fresh {
			return discovered{result: w.service.RefreshDiscovery(w.ctx, profile)}
		}
		return discovered{result: w.service.Discover(w.ctx, profile)}
	}
}
func (w *wizard) updateRequest() {
	if w.addKey != "" {
		return
	}
	w.request.Key = strings.TrimSpace(w.draft.Key)
	w.request.Name = strings.TrimSpace(w.draft.Name)
	w.request.Path = strings.TrimSpace(w.draft.Path)
	w.request.Profile = strings.TrimSpace(w.draft.Profile)
}
func (w wizard) choices() []app.RepositoryChoice {
	var choices []app.RepositoryChoice
	for _, source := range w.order {
		if c, ok := w.selected[source]; ok {
			choices = append(choices, c)
		}
	}
	return choices
}
func (w *wizard) loadEdits() tea.Cmd {
	if w.editIndex >= len(w.order) {
		w.editIndex = 0
	}
	if len(w.order) > 0 {
		c := w.selected[w.order[w.editIndex]]
		w.draft.Branch, w.draft.Base, w.draft.Destination = c.Branch, c.Base, c.Path
		w.defaults = w.repositoryDefaults()
	}
	w.form = w.worktreeForm()
	return w.form.Init()
}
func (w *wizard) saveEdits() {
	if len(w.order) == 0 {
		return
	}
	source := w.order[w.editIndex]
	c := w.selected[source]
	c.Branch = strings.TrimSpace(w.draft.Branch)
	c.Base = strings.TrimSpace(w.draft.Base)
	c.Path = strings.TrimSpace(w.draft.Destination)
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
func (w *wizard) syncChoices() {
	wanted := make(map[string]bool, len(w.draft.Repositories))
	for _, path := range w.draft.Repositories {
		wanted[path] = true
	}
	var order []string
	for _, path := range w.order {
		if wanted[path] {
			order = append(order, path)
		} else {
			delete(w.selected, path)
		}
	}
	w.order = order
	for _, r := range w.repositories {
		if wanted[r.Path] && !w.existing[r.Path] {
			if _, ok := w.selected[r.Path]; !ok {
				w.selectRepo(r.Path, app.RepositoryChoice{Source: r.Path, Name: r.Name, Base: w.defaultBase})
			}
		}
	}
}
func (w *wizard) goNext() tea.Cmd {
	w.status = ""
	switch w.step {
	case 0:
		w.updateRequest()
		w.step = 1
		w.loading = true
		w.form = nil
		return w.scanWith(false)
	case 1:
		if w.loading {
			w.status = "Wait for repository discovery to finish"
			return nil
		}
		w.syncChoices()
		if w.addKey != "" && len(w.order) == 0 {
			w.status = "Select at least one new repository to add"
			w.form = w.repositoryForm()
			return w.form.Init()
		}
		w.step = 2
		w.editIndex = 0
		return w.loadEdits()
	case 2:
		w.saveEdits()
		w.step = 3
		w.form = nil
		w.updateRequest()
		w.request.Repos = w.choices()
		w.plan = app.Plan{}
		w.status = "Checking workspace and Git state…"
		return w.planCommand()
	}
	return nil
}
func (w *wizard) goBack() tea.Cmd {
	w.status = ""
	switch w.step {
	case 1:
		if w.addKey == "" {
			if !w.loading {
				w.syncChoices()
			}
			w.step = 0
			w.loading = false
			w.form = w.identityForm()
			return w.form.Init()
		}
	case 2:
		w.saveEdits()
		w.step = 1
		w.form = w.repositoryForm()
		return w.form.Init()
	case 3:
		w.step = 2
		return w.loadEdits()
	}
	return nil
}
func (w wizard) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		w.resize(msg.Width, msg.Height)
	case discovered:
		if w.step != 1 {
			return w, nil
		}
		w.loading = false
		w.repositories = msg.result.Repositories
		w.warnings = nil
		for _, warning := range msg.result.Warnings {
			w.warnings = append(w.warnings, warning.Message)
		}
		for _, choice := range w.initialChoices {
			if choice.Source != "" && !w.existing[choice.Source] {
				w.selectRepo(choice.Source, choice)
			}
		}
		w.initialChoices = nil
		for _, term := range w.prefill {
			r, err := discovery.Resolve(w.repositories, term)
			if err != nil {
				w.warnings = append(w.warnings, err.Error())
				continue
			}
			if !w.existing[r.Path] {
				w.selectRepo(r.Path, app.RepositoryChoice{Source: r.Path, Name: r.Name, Base: w.defaultBase})
			}
		}
		w.prefill = nil
		w.status = ""
		if msg.err != nil {
			w.status = msg.err.Error()
		}
		w.form = w.repositoryForm()
		return w, w.form.Init()
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
		if k == "ctrl+r" && w.step == 1 {
			if !w.loading {
				w.syncChoices()
			}
			w.loading = true
			w.form = nil
			return w, w.scanWith(true)
		}
		if k == "ctrl+b" {
			cmd := w.goBack()
			return w, cmd
		}
		if w.step == 1 && w.loading {
			if k == "enter" || k == "ctrl+n" || k == "tab" {
				w.status = "Wait for repository discovery to finish"
			}
			return w, nil
		}
		if k == "ctrl+n" && (w.step == 1 || w.step == 2) {
			cmd := w.goNext()
			return w, cmd
		}
		if w.step == 2 && (k == "alt+left" || k == "alt+right") && len(w.order) > 0 {
			w.saveEdits()
			if k == "alt+left" && w.editIndex > 0 {
				w.editIndex--
			} else if k == "alt+right" && w.editIndex+1 < len(w.order) {
				w.editIndex++
			}
			cmd := w.loadEdits()
			return w, cmd
		}
		if w.step == 3 {
			if (k == "enter" || k == "y") && w.plan.Workspace != nil && w.plan.Safe() {
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
				cmd := w.goBack()
				return w, cmd
			}
			return w, nil
		}
	}
	if w.form == nil {
		return w, nil
	}
	model, cmd := w.form.Update(message)
	w.form = model.(*huh.Form)
	if w.step == 0 {
		w.updateIdentityHints()
	}
	if w.form.State == huh.StateCompleted {
		if w.step == 2 && w.editIndex+1 < len(w.order) {
			w.saveEdits()
			w.editIndex++
			cmd := w.loadEdits()
			return w, cmd
		}
		cmd := w.goNext()
		return w, cmd
	}
	return w, cmd
}
func (w *wizard) selectRepo(path string, c app.RepositoryChoice) {
	c.Source = path
	if c.Name == "" {
		c.Name = filepath.Base(path)
	}
	if _, found := w.selected[path]; !found {
		w.order = append(w.order, path)
	}
	w.selected[path] = c
}
func (w wizard) View() string {
	var b strings.Builder
	labels := []string{"Identity", "Repositories", "Worktrees", "Review", "Applying", "Result"}
	fmt.Fprintf(&b, "%s · %s\n\n", accentStyle.Render("Workdrive "+map[bool]string{true: "Add repositories", false: "New workspace"}[w.addKey != ""]), accentStyle.Render(labels[w.step]))
	if w.form != nil {
		b.WriteString(w.form.View() + "\n")
	}
	if w.loading {
		b.WriteString(accentStyle.Render("Scanning local repositories…") + "\n")
	}
	switch w.step {
	case 1:
		b.WriteString(helpLine("Ctrl+N next · Ctrl+R refresh"))
	case 2:
		b.WriteString(helpLine("Alt+←/→ previous/next repo · Ctrl+N review"))
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
	for _, v := range w.warnings[:min(3, len(w.warnings))] {
		fmt.Fprintf(&b, "%s\n", warningStyle.Render("Warning: "+v))
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
