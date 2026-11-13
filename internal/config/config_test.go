package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXDGPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/test-config")
	t.Setenv("XDG_STATE_HOME", "/tmp/test-state")
	path, err := ConfigPath()
	if err != nil || path != "/tmp/test-config/workdrive/config.toml" {
		t.Fatalf("config path %q: %v", path, err)
	}
	path, err = StatePath()
	if err != nil || path != "/tmp/test-state/workdrive" {
		t.Fatalf("state path %q: %v", path, err)
	}
}

func TestLoadAndProfileLayering(t *testing.T) {
	dir, err := CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "config.toml")
	root := filepath.Join(dir, "global")
	override := filepath.Join(dir, "profile")
	writeTestFile(t, global, `schema_version = 1
[workspace]
root = "`+root+`"
[profiles.development]
workspace_root = "`+filepath.Join(dir, "original-profile")+`"
[profiles.experimental]
workspace_root = "`+filepath.Join(dir, "experimental")+`"
[profiles.experimental.worktree]
branch_template = "exp/{{ .Workspace.Key }}"
`)
	if err := os.Mkdir(filepath.Join(dir, "profiles"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "profiles", "development.toml"), `[profiles.development]
workspace_root = "`+override+`"
[profiles.development.worktree]
path_template = "{{ .Workspace.Path }}/sub/{{ .Repo.Name }}"
`)
	cfg, err := Load(global)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace.Root != root || cfg.Worktree.Executable != "wt" {
		t.Fatalf("defaults/global not layered: %+v", cfg)
	}
	effective, err := cfg.Effective("")
	if err != nil {
		t.Fatal(err)
	}
	if effective.Workspace.Root != override || effective.Worktree.BranchTemplate != "{{ .Workspace.Key }}" || effective.Worktree.PathTemplate != "{{ .Workspace.Path }}/sub/{{ .Repo.Name }}" {
		t.Fatalf("profile not layered correctly: %+v", effective)
	}
	if cfg.Workspace.Root != root {
		t.Fatal("Effective mutated the base configuration")
	}
	workspace, err := effective.WorkspacePath("scratch-api")
	if err != nil || workspace != filepath.Join(override, "scratch-api") {
		t.Fatalf("workspace path %q: %v", workspace, err)
	}
	worktree, err := effective.WorktreePath(workspace, "auth-service")
	if err != nil || worktree != filepath.Join(workspace, "sub", "auth-service") {
		t.Fatalf("worktree path %q: %v", worktree, err)
	}
	if path, err := effective.WorktreePath(workspace, "auth service"); err != nil || path != filepath.Join(workspace, "sub", "auth service") {
		t.Fatalf("repository name containing spaces %q: %v", path, err)
	}
	effective.Worktree.PathTemplate = "{{ .Workspace.Path }}/{{ .Workspace.Key }}/{{ .Repo.Name }}"
	if _, err := effective.WorktreePath(workspace, "auth-service"); err == nil {
		t.Fatal("silently dropped missing workspace key")
	}
	if path, err := effective.WorktreePathWithContext("scratch-api", "Scratch API", workspace, "auth-service", "/repo"); err != nil || path != filepath.Join(workspace, "scratch-api", "auth-service") {
		t.Fatalf("full worktree context %q: %v", path, err)
	}
	effective.Workspace.DirectoryTemplate = "{{ .Workspace.Name }}"
	if _, err := effective.WorkspacePath("scratch-api"); err == nil {
		t.Fatal("silently dropped workspace name")
	}
	if path, err := effective.WorkspacePathWithName("scratch-api", "human-title"); err != nil || path != filepath.Join(override, "human-title") {
		t.Fatalf("workspace title template %q: %v", path, err)
	}
	branch, err := effective.Branch("scratch-api", "Scratch API", workspace, "auth-service", "/repo")
	if err != nil || branch != "scratch-api" {
		t.Fatalf("branch %q: %v", branch, err)
	}
	effective.Worktree.BranchTemplate = "{{ .Workspace.Key }}/{{ .Repo.Name }}"
	branch, err = effective.Branch("scratch-api", "Scratch API", workspace, "auth-service", "/repo")
	if err != nil || branch != "scratch-api/auth-service" {
		t.Fatalf("repo-name branch %q: %v", branch, err)
	}
	other, err := cfg.Effective("experimental")
	if err != nil || other.Worktree.BranchTemplate != "exp/{{ .Workspace.Key }}" {
		t.Fatalf("second profile: %+v, %v", other, err)
	}
}

func TestRestrictedTemplatesAndPaths(t *testing.T) {
	ctx := TemplateContext{Workspace: TemplateWorkspace{Key: "issue-4", Path: "/tmp/ws/issue-4"}, Repo: TemplateRepo{Name: "api"}}
	got, err := RenderTemplate("{{.Workspace.Path}}/{{ .Repo.Name }}", ctx)
	if err != nil || got != "/tmp/ws/issue-4/api" {
		t.Fatalf("render %q: %v", got, err)
	}
	for _, input := range []string{"", "{{ printf \"oops\" }}", "{{ .Workspace.Key | print }}", "{{ if true }}x{{ end }}", "{{.Workspace.Secret}}", "{{ .Repo.Name", "plain }}"} {
		if _, err := RenderTemplate(input, ctx); err == nil {
			t.Errorf("accepted unsafe template %q", input)
		}
	}
	root, err := CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nonexistent"), filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalPath(filepath.Join(root, "broken", "child")); err == nil {
		t.Fatal("treated broken symlink as a missing path")
	}
	for _, candidate := range []string{root, root + "/../escape", filepath.Join(root, "link", "escape"), filepath.Join(root, "broken", "child"), outside} {
		if _, err := ValidateDestination(root, candidate); err == nil {
			t.Errorf("accepted unsafe destination %q", candidate)
		}
	}
	if path, err := ValidateDestination(root, filepath.Join(root, "new", "child")); err != nil || path != filepath.Join(root, "new", "child") {
		t.Fatalf("valid destination %q: %v", path, err)
	}
	cfg := Defaults()
	cfg.Workspace.Root = root
	for _, input := range []string{"/tmp/other", "../other"} {
		cfg.Workspace.DirectoryTemplate = input
		if _, err := cfg.WorkspacePath("safe"); err == nil {
			t.Errorf("accepted directory template %q", input)
		}
	}
	cfg.Worktree.PathTemplate = "{{ .Workspace.Path }}/../other"
	if _, err := cfg.WorktreePath(filepath.Join(root, "safe"), "api"); err == nil {
		t.Fatal("accepted worktree traversal")
	}
	if err := CheckNewDestination(root); err == nil {
		t.Fatal("adopted existing directory")
	}
	if err := CheckNewDestination(filepath.Join(root, "new")); err != nil {
		t.Fatal(err)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "examples", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace.DefaultProfile != "development" || len(cfg.Repositories.Sources) != 1 {
		t.Fatalf("unexpected example config: %+v", cfg)
	}
}

func TestConfigValidationAndExplicitMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")
	if _, err := Load(path); err == nil {
		t.Fatal("explicit missing config accepted")
	}
	writeTestFile(t, path, "[worktree]\ndelete_branch_on_cleanup = true\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "preserved") {
		t.Fatalf("unsafe branch deletion allowed: %v", err)
	}
	writeTestFile(t, path, "invalid_key = true\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown field accepted: %v", err)
	}
	for _, pattern := range []string{"../foo", "**broken", "/absolute", "[broken", `a\b`} {
		if err := ValidateExclude(pattern); err == nil {
			t.Errorf("accepted unsafe glob %q", pattern)
		}
	}
	if err := ValidateBranch("bad branch"); err == nil {
		t.Fatal("accepted invalid Git branch")
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
