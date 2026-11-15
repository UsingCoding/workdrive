package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/manifest"
	"github.com/usingcoding/workdrive/internal/registry"
	"github.com/usingcoding/workdrive/internal/shellintegration"
)

func runCompletion(t *testing.T, configPath, token string, args ...string) ([]string, cobra.ShellCompDirective) {
	t.Helper()
	var out, errs bytes.Buffer
	c := &command{out: &out, errOut: &errs, integration: shellintegration.NewInvocation("/must-not-open-handoff", token)}
	root := c.root()
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs(append([]string{"__complete", "--config", configPath}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("completion %v: %v; out %q err %q", args, err, out.String(), errs.String())
	}
	if c.service != nil {
		c.service.Close()
		t.Fatal("completion opened ordinary service")
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, ":") {
		t.Fatalf("nonprotocol stdout: %q", out.String())
	}
	directive, err := strconv.Atoi(strings.TrimPrefix(last, ":"))
	if err != nil {
		t.Fatal(err)
	}
	return lines[:len(lines)-1], cobra.ShellCompDirective(directive)
}

func valuesOnly(candidates []string) []string {
	var result []string
	for _, candidate := range candidates {
		value, _, _ := strings.Cut(candidate, "\t")
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func assertCompletion(t *testing.T, configPath, token string, want []string, args ...string) {
	t.Helper()
	candidates, directive := runCompletion(t, configPath, token, args...)
	if directive != semanticCompletion {
		t.Fatalf("%v: filename fallback not disabled: %v", args, directive)
	}
	got := valuesOnly(candidates)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v: got %v want %v (full: %q)", args, got, want, candidates)
	}
}

type completionFixture struct {
	configPath, root, sources string
	workspaces                map[string]*domain.Workspace
	index                     *registry.Registry
}

func completionSetup(t *testing.T) completionFixture {
	t.Helper()
	root, err := config.CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "unused-config"))
	workspaceRoot, sources := filepath.Join(root, "workspaces"), filepath.Join(root, "sources")
	configPath := filepath.Join(root, "config.toml")
	body := fmt.Sprintf("schema_version = 1\n[workspace]\nroot = %s\n[repositories]\nrefresh_on_start = true\n[[repositories.sources]]\nname = 'sources'\npath = %s\nmax_depth = 4\nexclude = ['excluded/**']\n[profiles.zeta]\n[profiles.alternate]\nworkspace_root = %s\n", quoteTOML(workspaceRoot), quoteTOML(sources), quoteTOML(filepath.Join(root, "alternate")))
	if err := os.WriteFile(configPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	index, err := registry.Open(filepath.Join(stateDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { index.Close() })
	f := completionFixture{configPath: configPath, root: root, sources: sources, workspaces: map[string]*domain.Workspace{}, index: index}
	for _, key := range []string{"active", "parked", "attention", "stale", "reused"} {
		w, err := domain.NewWorkspace(key, key+" title", filepath.Join(workspaceRoot, key), "development", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		w.State = domain.StateActive
		if key == "parked" {
			w.State = domain.StateParked
		} else if key == "attention" {
			w.State = domain.StateAttentionRequired
		}
		if err := os.MkdirAll(w.Path, 0700); err != nil {
			t.Fatal(err)
		}
		if key != "stale" {
			if err := manifest.Write(w); err != nil {
				t.Fatal(err)
			}
		}
		if err := index.Upsert(w); err != nil {
			t.Fatal(err)
		}
		if key == "reused" {
			w.ID = "new-owner"
			if err := manifest.Write(w); err != nil {
				t.Fatal(err)
			}
		}
		f.workspaces[key] = w
	}
	return f
}

func TestCompletionWorkspaceAndStaticMatrix(t *testing.T) {
	f := completionSetup(t)
	previous := f.workspaces["parked"]
	token, err := shellintegration.EncodeIdentity(shellintegration.Identity{ID: previous.ID, Key: previous.Key, Path: previous.Path})
	if err != nil {
		t.Fatal(err)
	}
	assertCompletion(t, f.configPath, token, []string{"-", "active", "parked"}, "switch", "")
	assertCompletion(t, f.configPath, "invalid", []string{"active", "parked"}, "switch", "")
	for _, args := range [][]string{{"inspect", ""}, {"park", ""}, {"cleanup", ""}, {"refresh", ""}, {"resume", ""}, {"add", "repo", ""}, {"remove", "repo", ""}} {
		assertCompletion(t, f.configPath, token, []string{"active", "attention", "parked"}, args...)
	}
	assertCompletion(t, f.configPath, token, nil, "new", "")
	assertCompletion(t, f.configPath, token, nil, "new", "--name", "")
	assertCompletion(t, f.configPath, token, nil, "switch", "active", "")
	assertCompletion(t, f.configPath, token, []string{"active", "all", "parked"}, "list", "--state", "")
	assertCompletion(t, f.configPath, token, []string{"alternate", "development", "zeta"}, "--profile", "")
	assertCompletion(t, f.configPath, token, []string{"alternate", "development", "zeta"}, "new", "key", "--profile", "")
	assertCompletion(t, f.configPath, token, []string{"bash", "zsh"}, "config", "shell", "init", "")
	candidates, directive := runCompletion(t, "/nonexistent/config", "", "--config", "")
	if directive != cobra.ShellCompDirectiveFilterFileExt || !reflect.DeepEqual(candidates, []string{"toml"}) {
		t.Fatalf("config filename completion: %v %v", candidates, directive)
	}
	if _, err := os.Lstat(filepath.Join(stateDir(), "operations")); !os.IsNotExist(err) {
		t.Fatalf("completion created lifecycle state: %v", err)
	}
	if previous.State != domain.StateParked {
		t.Fatal("completion activated previous workspace")
	}
}

func TestCompletionLiteralDashAndPreviousIdentity(t *testing.T) {
	f := completionSetup(t)
	w := f.workspaces["parked"]
	token, err := shellintegration.EncodeIdentity(shellintegration.Identity{ID: w.ID, Key: w.Key, Path: w.Path})
	if err != nil {
		t.Fatal(err)
	}
	// A real -- boundary must not expose previous-workspace shorthand.
	assertCompletion(t, f.configPath, token, []string{"active", "parked"}, "switch", "--", "")
	literal, err := domain.NewWorkspace("-", "literal dash", filepath.Join(filepath.Dir(w.Path), "literal-dash"), "development", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	literal.State = domain.StateActive
	if err := os.Mkdir(literal.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(literal); err != nil {
		t.Fatal(err)
	}
	if err := f.index.Upsert(literal); err != nil {
		t.Fatal(err)
	}
	assertCompletion(t, f.configPath, "invalid", []string{"active", "parked"}, "switch", "")
	assertCompletion(t, f.configPath, "invalid", []string{"-", "active", "parked"}, "switch", "--", "")
	assertCompletion(t, f.configPath, token, []string{"-"}, "switch", "--", "-")
	// Cobra reserves a leading - prefix for flag-name completion. It never
	// dispatches that prefix to ValidArgsFunction, even with valid history.
	candidates, directive := runCompletion(t, f.configPath, token, "switch", "-")
	if directive != semanticCompletion || len(candidates) == 0 {
		t.Fatalf("Cobra flag prefix: %v %v", candidates, directive)
	}
	for _, candidate := range valuesOnly(candidates) {
		if !strings.HasPrefix(candidate, "-") || candidate == "-" {
			t.Fatalf("unexpected flag-prefix candidate: %q", candidate)
		}
	}
	stale, err := shellintegration.EncodeIdentity(shellintegration.Identity{ID: "old-identity", Key: w.Key, Path: w.Path})
	if err != nil {
		t.Fatal(err)
	}
	assertCompletion(t, f.configPath, stale, []string{"active", "parked"}, "switch", "")
	assertCompletion(t, f.configPath, stale, nil, "switch", "-", "--repo", "")
}

func TestCompletionRecordedSelectorsRoundTripAndFlagOrdering(t *testing.T) {
	f := completionSetup(t)
	w := f.workspaces["parked"]
	for i, source := range []string{"a/api", "b/api", "worker", "bad\nname"} {
		w.Resources = append(w.Resources, domain.Resource{ID: fmt.Sprintf("resource-%d", i), Type: "worktree", Driver: "worktrunk", Ownership: domain.OwnershipOwned,
			Source: filepath.Join(f.sources, source), Path: filepath.Join(w.Path, fmt.Sprintf("custom-%d", i))})
	}
	w.Name = "title\nwith\tcontrols\x1b"
	if err := manifest.Write(w); err != nil {
		t.Fatal(err)
	}
	token, err := shellintegration.EncodeIdentity(shellintegration.Identity{ID: w.ID, Key: w.Key, Path: w.Path})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"resource-0", "resource-1", "worker", "resource-3"}
	for _, args := range [][]string{
		{"switch", "parked", "--repo", ""},
		{"switch", "--print-path", "parked", "--repo", ""},
		{"switch", "--repo=worker", "parked", "--repo", ""},
		{"switch", "-", "--repo", ""},
		{"remove", "repo", "--repo=worker", "parked", "--repo", ""},
	} {
		assertCompletion(t, f.configPath, token, append([]string(nil), want...), args...)
	}
	assertCompletion(t, f.configPath, token, nil, "switch", "--repo", "")
	assertCompletion(t, f.configPath, token, nil, "switch", "unknown", "--repo", "")
	assertCompletion(t, f.configPath, token, []string{"worker"}, "switch", "parked", "--repo", "wor")
	assertCompletion(t, f.configPath, token, []string{"resource-2"}, "switch", "parked", "--repo", "resource-2")
	assertCompletion(t, f.configPath, token, []string{w.Resources[2].Source}, "switch", "parked", "--repo", f.sources+"/wor")
	assertCompletion(t, f.configPath, token, []string{w.Resources[2].Path}, "remove", "repo", "parked", "--repo", w.Path+"/custom-2")
	candidates, _ := runCompletion(t, f.configPath, token, "switch", "parked", "--repo", "")
	for _, candidate := range candidates {
		if strings.ContainsAny(candidate, "\r\n\x1b") || strings.Count(candidate, "\t") > 1 {
			t.Fatalf("unsafe Cobra protocol candidate: %q", candidate)
		}
	}
}

func TestCompletionSourceHintsRepeatAndEffectiveProfile(t *testing.T) {
	f := completionSetup(t)
	for _, name := range []string{"api", "worker", "nested/api", "excluded/hidden"} {
		if err := os.MkdirAll(filepath.Join(f.sources, name, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Exclude all workspace roots even when nested within a source root.
	overlay := filepath.Join(f.root, "profiles")
	if err := os.Mkdir(overlay, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "alternate.toml"), []byte("[profiles.alternate]\nworkspace_root = "+quoteTOML(filepath.Join(f.sources, "workspace-excluded"))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.sources, "workspace-excluded", "task", ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	// A recorded source without .git is still a cheap hint; execution validates it.
	w := f.workspaces["active"]
	w.Resources = []domain.Resource{{ID: "recorded", Type: "worktree", Driver: "worktrunk", Ownership: domain.OwnershipOwned,
		Source: filepath.Join(f.sources, "recorded"), Path: filepath.Join(w.Path, "recorded")}}
	if err := manifest.Write(w); err != nil {
		t.Fatal(err)
	}
	api, secondAPI := filepath.Join(f.sources, "api"), filepath.Join(f.sources, "nested/api")
	// A fake Git that would fail completion proves metadata hints never spawn it.
	t.Setenv("PATH", t.TempDir())
	assertCompletion(t, f.configPath, "", []string{api, secondAPI, "worker", "recorded"}, "new", "--repo", "")
	assertCompletion(t, f.configPath, "", []string{"worker"}, "new", "--repo", "wo")
	assertCompletion(t, f.configPath, "", []string{filepath.Join(f.sources, "worker")}, "new", "--repo", f.sources+"/w")
	assertCompletion(t, f.configPath, "", []string{api, secondAPI, "worker"}, "add", "repo", "active", "--repo", "")
	assertCompletion(t, f.configPath, "", []string{api, secondAPI, "recorded"}, "new", "--repo=worker", "key", "--repo", "")
	assertCompletion(t, f.configPath, "", []string{"recorded"}, "new", "--repo=api", "key", "--repo", "worker", "--repo", "")
	assertCompletion(t, f.configPath, "", nil, "add", "repo", "unknown", "--repo", "")
	// A target's unknown profile must not fall back to global/CLI defaults.
	w.Profile = "removed-profile"
	if err := manifest.Write(w); err != nil {
		t.Fatal(err)
	}
	assertCompletion(t, f.configPath, "", nil, "add", "repo", "--profile", "development", "active", "--repo", "")
}

func TestCompletionIncompleteSourceHintsUseAbsolutePaths(t *testing.T) {
	f := completionSetup(t)
	for i := 0; i < 105; i++ {
		if err := os.MkdirAll(filepath.Join(f.sources, fmt.Sprintf("repo-%03d", i), ".git"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	candidates, directive := runCompletion(t, f.configPath, "", "new", "--repo", "")
	if directive != semanticCompletion || len(candidates) == 0 || len(candidates) > 100 {
		t.Fatalf("bounded source completion: %d %v", len(candidates), directive)
	}
	for _, value := range valuesOnly(candidates) {
		if !filepath.IsAbs(value) {
			t.Fatalf("incomplete scan claimed a unique basename: %q", value)
		}
	}
	assertCompletion(t, f.configPath, "", []string{"default"}, "new", "--repo=repo-000", "--base", "")
}

func TestCompletionMissingStateAndMalformedConfigRemainReadOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "missing-state"))
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte("schema_version=1\n[repositories]\nsources=[]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assertCompletion(t, configPath, "", nil, "switch", "")
	assertCompletion(t, configPath, "", []string{"development"}, "--profile", "")
	if _, err := os.Lstat(filepath.Join(root, "missing-state")); !os.IsNotExist(err) {
		t.Fatalf("completion initialized absent state: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("invalid = ["), 0600); err != nil {
		t.Fatal(err)
	}
	assertCompletion(t, configPath, "", []string{"active", "all", "parked"}, "list", "--state", "")
	assertCompletion(t, configPath, "", []string{"bash", "zsh"}, "config", "shell", "init", "")
	assertCompletion(t, configPath, "", nil, "switch", "")
	assertCompletion(t, configPath, "", []string{"default"}, "new", "--base", "")
}

func TestCompletionDoesNotWriteFilesOrActivate(t *testing.T) {
	f := completionSetup(t)
	if err := os.MkdirAll(filepath.Join(f.sources, "hint", ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		t.Helper()
		files := map[string]string{}
		if err := filepath.WalkDir(f.root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			value := fmt.Sprintf("%v %v", info.Mode(), info.ModTime())
			if !entry.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				value += string(data)
			}
			files[path] = value
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return files
	}
	before := snapshot()
	for _, args := range [][]string{{"switch", ""}, {"new", "--repo", ""}, {"new", "--base", ""}, {"add", "repo", "parked", "--repo", ""}, {"switch", "parked", "--repo", ""}} {
		runCompletion(t, f.configPath, "", args...)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("completion changed source/workspace/config/state files")
	}
}

func TestCompletionBaseCommonLocalRefs(t *testing.T) {
	f := completionSetup(t)
	var paths []string
	for i, branches := range [][]string{{"common", "only-first"}, {"common", "only-second"}} {
		path := filepath.Join(f.sources, fmt.Sprintf("repo-%d", i))
		paths = append(paths, path)
		git := func(args ...string) {
			t.Helper()
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git: %s %v", out, err)
			}
		}
		git("init", "-q", "-b", "main", path)
		git("-C", path, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "initial")
		for _, branch := range branches {
			git("-C", path, "branch", branch)
		}
	}
	assertCompletion(t, f.configPath, "", []string{"common", "default", "main", "only-first"}, "new", "--repo", "repo-0", "key", "--base", "")
	assertCompletion(t, f.configPath, "", []string{"common", "default", "main"}, "new", "--repo="+paths[0], "key", "--repo", paths[1], "--base", "")
	assertCompletion(t, f.configPath, "", []string{"default"}, "new", "--repo=unknown", "--base", "")
}

type fakeCompletionRefs struct {
	mu    sync.Mutex
	refs  map[string][]string
	calls []string
	err   error
}

// LocalRefs is called concurrently by commonLocalRefs.
func (f *fakeCompletionRefs) LocalRefs(_ context.Context, source string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, source)
	return f.refs[source], f.err
}

func TestCommonLocalRefsInjectionAndFailure(t *testing.T) {
	reader := &fakeCompletionRefs{refs: map[string][]string{"one": {"common", "one"}, "two": {"two", "common"}}}
	if got := commonLocalRefs(context.Background(), reader, []string{"one", "one", "two"}); !reflect.DeepEqual(got, []string{"common"}) || len(reader.calls) != 2 {
		t.Fatalf("intersection/dedup: %v %v", got, reader.calls)
	}
	reader.err = errors.New("timeout")
	if got := commonLocalRefs(context.Background(), reader, []string{"one"}); got != nil {
		t.Fatalf("error retained refs: %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := commonLocalRefs(ctx, reader, []string{"one"}); got != nil {
		t.Fatalf("cancel retained refs: %v", got)
	}
}
