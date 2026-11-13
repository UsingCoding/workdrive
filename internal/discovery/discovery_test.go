package discovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usingcoding/workdrive/internal/config"
)

func TestScanNestedGitFilesDuplicatesAndExclusions(t *testing.T) {
	base, err := config.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root1 := filepath.Join(base, "projects")
	root2 := filepath.Join(base, "other")
	first := filepath.Join(root1, "alpha")
	inner := filepath.Join(root1, "group", "inner")
	second := filepath.Join(root2, "alpha")
	for _, dir := range []string{first, inner, second, filepath.Join(root1, "workspaces")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "init", "-q")
		runGit(t, dir, "config", "user.email", "test@example.com")
		runGit(t, dir, "config", "user.name", "Test")
		runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "initial")
	}
	linked := filepath.Join(root2, "linked")
	runGit(t, first, "worktree", "add", "-q", "--detach", linked)
	workspaceRepo := filepath.Join(root1, "workspaces", "generated")
	runGit(t, first, "worktree", "add", "-q", "--detach", workspaceRepo)
	if err := os.Symlink(inner, filepath.Join(root2, "alias")); err != nil {
		t.Fatal(err)
	}
	cfg := config.RepositoryConfig{Sources: []config.Source{
		{Name: "projects", Path: root1, MaxDepth: 3},
		{Name: "other", Path: root2, MaxDepth: 2, Exclude: []string{"ignored/**"}},
		{Name: "repeated", Path: root1, MaxDepth: 3},
		{Name: "missing", Path: filepath.Join(base, "not-found"), MaxDepth: 1},
	}}
	result := Scan(context.Background(), cfg, filepath.Join(root1, "workspaces"))
	if len(result.Repositories) != 4 {
		t.Fatalf("wanted 4 canonical sources, got %+v (warnings: %+v)", result.Repositories, result.Warnings)
	}
	if len(result.Warnings) != 1 || result.Warnings[0].Root != "missing" {
		t.Fatalf("missing root warning expected, got %+v", result.Warnings)
	}
	seen := map[string]Repository{}
	for _, repo := range result.Repositories {
		seen[repo.Path] = repo
	}
	if _, ok := seen[workspaceRepo]; ok {
		t.Fatal("scanned generated workspace worktree")
	}
	if seen[linked].Name != "linked" || seen[inner].Name != "inner" {
		t.Fatalf("missed nested or .git-file repo: %+v", result.Repositories)
	}
	if seen[first].DisplayName == seen[second].DisplayName || !strings.Contains(seen[first].DisplayName, "projects") {
		t.Fatalf("duplicate labels not disambiguated: %+v", result.Repositories)
	}
	if _, err := Resolve(result.Repositories, "alpha"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous bare name accepted: %v", err)
	}
	for _, input := range []string{first, seen[first].DisplayName} {
		if repo, err := Resolve(result.Repositories, input); err != nil || repo.Path != first {
			t.Fatalf("resolve %q: %+v, %v", input, repo, err)
		}
	}
}

func TestScanDepthAndGlobExclusions(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"top", "parent/deep", "node_modules/package", ".cache/stuff", "keep/hidden"} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		runGit(t, path, "init", "-q")
	}
	source := config.Source{Name: "root", Path: root, MaxDepth: 1, Exclude: []string{"**/node_modules/**", "**/.cache/**"}}
	result := Scan(context.Background(), config.RepositoryConfig{Sources: []config.Source{source}})
	if len(result.Repositories) != 1 || result.Repositories[0].Name != "top" {
		t.Fatalf("depth/exclusions failed: %+v", result.Repositories)
	}
	source.MaxDepth = 3
	result = Scan(context.Background(), config.RepositoryConfig{Sources: []config.Source{source}})
	if len(result.Repositories) != 3 {
		t.Fatalf("expected top/deep/hidden, got %+v", result.Repositories)
	}
	for _, repo := range result.Repositories {
		if strings.Contains(repo.Path, "node_modules") || strings.Contains(repo.Path, ".cache") {
			t.Fatalf("excluded repo discovered: %+v", repo)
		}
	}
}

func TestMatchGlobSegments(t *testing.T) {
	for _, sample := range []struct {
		pattern, path string
		want          bool
	}{
		{"workspaces/**", "workspaces", true},
		{"workspaces/**", "workspaces/a/b", true},
		{"**/node_modules/**", "node_modules/pkg", true},
		{"**/node_modules/**", "a/node_modules", true},
		{"**/node_modules/**", "a/not-node_modules/pkg", false},
		{"a/*", "a/b/c", false},
	} {
		if got := match(sample.pattern, sample.path); got != sample.want {
			t.Errorf("match(%q,%q) = %v, want %v", sample.pattern, sample.path, got, sample.want)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}
