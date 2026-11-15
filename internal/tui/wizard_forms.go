package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/ansi"
	"github.com/usingcoding/workdrive/internal/domain"
)

func wizardTheme() *huh.Theme {
	theme := huh.ThemeBase()
	theme.Focused.Base = theme.Focused.Base.BorderForeground(accentStyle.GetForeground())
	theme.Blurred.Base = theme.Blurred.Base.BorderForeground(mutedStyle.GetForeground())
	for _, styles := range []*huh.FieldStyles{&theme.Focused, &theme.Blurred} {
		styles.Title = textStyle.Bold(true)
		styles.Description = mutedStyle
		styles.ErrorIndicator = dangerStyle
		styles.ErrorMessage = dangerStyle
		styles.TextInput.Text = textStyle
		styles.TextInput.Placeholder = mutedStyle
		styles.TextInput.Prompt = accentStyle
		styles.TextInput.Cursor = accentStyle
		styles.SelectSelector = accentStyle.SetString("> ")
		styles.MultiSelectSelector = accentStyle.SetString("> ")
		styles.SelectedOption = successStyle
		styles.SelectedPrefix = successStyle.SetString("[x] ")
		styles.UnselectedOption = textStyle
		styles.UnselectedPrefix = mutedStyle.SetString("[ ] ")
		styles.NoteTitle = accentStyle
		styles.Next = accentStyle
	}
	theme.Focused.Title = accentStyle
	theme.Help.ShortKey, theme.Help.FullKey = accentStyle, accentStyle
	theme.Help.ShortDesc, theme.Help.FullDesc = mutedStyle, mutedStyle
	theme.Help.ShortSeparator, theme.Help.FullSeparator = mutedStyle, mutedStyle
	return theme
}

func (w wizard) newForm(group *huh.Group) *huh.Form {
	keys := huh.NewDefaultKeyMap()
	// Ctrl+N advances a wizard step rather than moving the selection down.
	keys.MultiSelect.Down.SetKeys("down", "j", "ctrl+j")
	return huh.NewForm(group).WithTheme(wizardTheme()).WithKeyMap(keys).
		WithWidth(min(90, max(20, w.width-4))).WithHeight(min(20, max(6, w.height-8)))
}

func (w *wizard) resize(width, height int) {
	w.width, w.height = width, height
	if w.form != nil {
		w.form.WithWidth(min(90, max(20, width-4))).WithHeight(min(20, max(6, height-8)))
	}
	if w.step == 2 && w.destination != nil {
		w.destination.Placeholder(w.pathHint(w.defaults.Destination))
	}
}

func (w *wizard) identityForm() *huh.Form {
	var profiles []string
	for name := range w.config().Profiles {
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)
	w.identity = []*huh.Input{
		huh.NewInput().Key("key").Title("Workspace key").
			Description("Required · letters, numbers, _, - and .").Placeholder("scratch-api").
			Value(&w.draft.Key).Validate(func(value string) error { return domain.ValidateKey(strings.TrimSpace(value)) }),
		huh.NewInput().Key("name").Title("Name").Description("Optional · defaults to the workspace key").Value(&w.draft.Name),
		huh.NewInput().Key("path").Title("Workspace path").Description("Optional · generated from the key, name and profile").Value(&w.draft.Path),
		huh.NewInput().Key("profile").Title("Profile").Description("Optional · configured profiles: " + strings.Join(profiles, ", ")).
			Suggestions(profiles).Value(&w.draft.Profile).Validate(func(value string) error {
			_, err := w.config().Effective(strings.TrimSpace(value))
			return err
		}),
	}
	w.updateIdentityHints()
	return w.newForm(huh.NewGroup(w.identity[0], w.identity[1], w.identity[2], w.identity[3]))
}

func (w wizard) pathHint(path string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+"/") {
		path = "~" + strings.TrimPrefix(path, home)
	}
	width := max(8, min(90, max(20, w.width-4))-6)
	if ansi.StringWidth(path) > width {
		return ansi.TruncateLeft(path, ansi.StringWidth(path)-width+1, "…")
	}
	return path
}

func (w wizard) updateIdentityHints() {
	if len(w.identity) != 4 {
		return
	}
	defaults := w.identityDefaults()
	name, path := defaults.Name, defaults.Path
	if name == "" {
		name = "Same as workspace key"
	}
	if path == "" {
		path = "Generated after choosing a valid key and profile"
	}
	w.identity[1].Placeholder(name)
	w.identity[2].Placeholder(w.pathHint(path))
	w.identity[3].Placeholder(defaults.Profile)
}

func (w *wizard) repositoryForm() *huh.Form {
	w.draft.Repositories = append([]string(nil), w.order...)
	var options []huh.Option[string]
	for _, r := range w.repositories {
		if !w.existing[r.Path] {
			// Paths make labels unique without repeating a disambiguated display name.
			options = append(options, huh.NewOption(r.Name+" — "+r.Path, r.Path))
		}
	}
	if len(options) == 0 {
		message := "No matching repositories. You can create a workspace without repositories, or rescan with Ctrl+R."
		if w.addKey != "" {
			message = "No new repositories available. Rescan with Ctrl+R or cancel with Esc."
		}
		return w.newForm(huh.NewGroup(huh.NewNote().Title("Repositories").Description(message).Next(true).NextLabel("Continue")))
	}
	return w.newForm(huh.NewGroup(huh.NewMultiSelect[string]().Key("repositories").Title("Repositories").
		Description("Select repositories · / filters by name or path · an empty selection is allowed for new workspaces").
		Options(options...).Filterable(true).Value(&w.draft.Repositories)))
}

func (w *wizard) worktreeForm() *huh.Form {
	w.destination = nil
	if len(w.order) == 0 {
		return w.newForm(huh.NewGroup(huh.NewNote().Title("No worktrees selected").
			Description("Continue to review the workspace before creating it.").Next(true).NextLabel("Review workspace")))
	}
	choice := w.selected[w.order[w.editIndex]]
	base := w.defaults.Base
	if base == "default" {
		base = "default (Worktrunk's default branch)"
	}
	w.destination = huh.NewInput().Key("destination").Title("Destination").Description("Optional · generated inside this workspace").
		Placeholder(w.pathHint(w.defaults.Destination)).Value(&w.draft.Destination)
	return w.newForm(huh.NewGroup(
		huh.NewInput().Key("branch").Title("Branch").Description("Optional · leave blank to use the profile default").
			Placeholder(w.defaults.Branch).Value(&w.draft.Branch),
		huh.NewInput().Key("base").Title("Base ref").Description("Optional · Git ref used when creating a branch").
			Placeholder(base).Value(&w.draft.Base),
		w.destination,
	).Title(fmt.Sprintf("Repository %d/%d: %s", w.editIndex+1, len(w.order), choice.Name)).Description(choice.Source))
}
