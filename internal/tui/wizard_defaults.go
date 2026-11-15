package tui

import (
	"strings"

	"github.com/usingcoding/workdrive/internal/config"
)

// Form values have stable addresses while Bubble Tea copies the wizard model.
// Empty optional values stay empty; defaults are hints, not saved overrides.
type wizardDraft struct {
	Key, Name, Path, Profile  string
	Branch, Base, Destination string
	Repositories              []string
}

type workspaceDefaults struct {
	Name, Path, Profile string
}

type worktreeDefaults struct {
	Branch, Base, Destination string
}

func (w wizard) config() *config.Config {
	if w.service != nil && w.service.Config != nil {
		return w.service.Config
	}
	return config.Defaults()
}

func (w wizard) identityDefaults() workspaceDefaults {
	defaults := workspaceDefaults{Name: strings.TrimSpace(w.draft.Key), Profile: w.config().Workspace.DefaultProfile}
	cfg, err := w.config().Effective(strings.TrimSpace(w.draft.Profile))
	if err != nil {
		return defaults
	}
	name := strings.TrimSpace(w.draft.Name)
	if name == "" {
		name = defaults.Name
	}
	defaults.Path, _ = cfg.WorkspacePathWithName(strings.TrimSpace(w.draft.Key), name)
	return defaults
}

func (w wizard) repositoryDefaults() worktreeDefaults {
	var defaults worktreeDefaults
	if w.editIndex >= len(w.order) {
		return defaults
	}
	cfg, err := w.config().Effective(w.request.Profile)
	if err != nil {
		return defaults
	}
	choice := w.selected[w.order[w.editIndex]]
	name, path := w.request.Name, w.request.Path
	if name == "" {
		name = w.request.Key
	}
	if path == "" {
		path, err = cfg.WorkspacePathWithName(w.request.Key, name)
	} else {
		path, err = config.ValidateDestination(cfg.Workspace.Root, path)
	}
	if err != nil {
		return defaults
	}
	defaults.Branch, _ = cfg.Branch(w.request.Key, name, path, choice.Name, choice.Source)
	defaults.Destination, _ = cfg.WorktreePathWithContext(w.request.Key, name, path, choice.Name, choice.Source)
	defaults.Base = cfg.Worktree.BaseRef
	return defaults
}
