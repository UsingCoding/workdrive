package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/config"
)

func TestWizardPathHintsKeepShortPathsAndLongSuffixes(t *testing.T) {
	w := wizard{width: 80}
	short := "/workspaces/TASK-1"
	if got := w.pathHint(short); got != short {
		t.Fatalf("short path hint changed: %q", got)
	}
	for _, prefix := range []string{strings.Repeat("long-directory/", 12), strings.Repeat("界", 60)} {
		got := w.pathHint("/" + prefix + "/TASK-1/api")
		if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "/TASK-1/api") || ansi.StringWidth(got) > 70 {
			t.Fatalf("long path hint lost its suffix or overflowed: %q", got)
		}
	}
}

func TestWizardIdentityHintsFollowKeyNameAndProfile(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	cfg.Workspace.DirectoryTemplate = "{{ .Workspace.Key }}/{{ .Workspace.Name }}"
	cfg.Profiles["release"] = config.Profile{WorkspaceRoot: filepath.Join(root, "releases")}
	w := newWizard(context.Background(), &app.Service{Config: cfg}, app.NewRequest{Key: "TASK-1"}, nil, "", "")
	check := func(key, name, profile string) {
		t.Helper()
		effective, err := cfg.Effective(profile)
		if err != nil {
			t.Fatal(err)
		}
		path, err := effective.WorkspacePathWithName(key, name)
		if err != nil {
			t.Fatal(err)
		}
		hints := w.identityDefaults()
		if hints.Name != key || hints.Profile != cfg.Workspace.DefaultProfile || hints.Path != path {
			t.Fatalf("unexpected hints: %+v; want name %s, profile %s, path %s", hints, key, cfg.Workspace.DefaultProfile, path)
		}
	}
	check("TASK-1", "TASK-1", "")
	w.draft.Key = "TASK-2"
	check("TASK-2", "TASK-2", "")
	w.draft.Name = "API playground"
	check("TASK-2", "API playground", "")
	w.draft.Profile = "release"
	check("TASK-2", "API playground", "release")
	if w.draft.Path != "" || w.request.Name != "" || w.request.Profile != "" {
		t.Fatal("rendering hints saved defaults into optional fields")
	}
}

func TestWizardWorktreeHintsUseTemplatesAndExplicitWorkspacePath(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	cfg.Profiles["release"] = config.Profile{
		Worktree: config.WorktreeOverride{
			BranchTemplate: "{{ .Workspace.Key }}/{{ .Repo.Name }}",
			PathTemplate:   "{{ .Workspace.Path }}/trees/{{ .Repo.Name }}",
			BaseRef:        "origin/release",
		},
	}
	path, err := config.CanonicalPath(filepath.Join(cfg.Workspace.Root, "custom"))
	if err != nil {
		t.Fatal(err)
	}
	w := newWizard(context.Background(), &app.Service{Config: cfg}, app.NewRequest{Key: "TASK-1", Path: path, Profile: "release"}, nil, "", "")
	w.selectRepo(filepath.Join(root, "sources", "api"), app.RepositoryChoice{})
	hints := w.repositoryDefaults()
	if hints.Branch != "TASK-1/api" || hints.Base != "origin/release" || hints.Destination != filepath.Join(path, "trees", "api") {
		t.Fatalf("incorrect worktree hints: %+v", hints)
	}
	w.step = 2
	w.loadEdits()
	if w.draft.Branch != "" || w.draft.Base != "" || w.draft.Destination != "" {
		t.Fatal("profile defaults were saved as explicit overrides")
	}
}

func TestAddWizardKeepsExistingWorkspaceContextForHints(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	cfg.Repositories.Sources = nil
	cfg.Profiles["release"] = config.Profile{
		WorkspaceRoot: filepath.Join(root, "releases"),
		Worktree:      config.WorktreeOverride{BranchTemplate: "{{ .Workspace.Key }}/{{ .Repo.Name }}", BaseRef: "release"},
	}
	s, err := app.Open(cfg, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	plan, err := s.PlanNew(app.NewRequest{Key: "TASK-1", Name: "Release workspace", Profile: "release", Path: filepath.Join(root, "releases", "custom")})
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.Create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	w := newWizard(context.Background(), s, app.NewRequest{}, nil, "", created.Key)
	w.selectRepo(filepath.Join(root, "sources", "api"), app.RepositoryChoice{})
	w.step = 2
	w.loadEdits()
	w.updateRequest()
	if w.request.Key != created.Key || w.request.Name != created.Name || w.request.Path != created.Path || w.request.Profile != "release" {
		t.Fatalf("add lost its existing workspace context: %+v", w.request)
	}
	if w.defaults.Branch != "TASK-1/api" || w.defaults.Base != "release" || w.defaults.Destination != filepath.Join(created.Path, "api") {
		t.Fatalf("incorrect add defaults: %+v", w.defaults)
	}
}
