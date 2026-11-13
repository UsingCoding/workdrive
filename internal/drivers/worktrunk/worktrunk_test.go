package worktrunk

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The fake executable is a wrapper around this test process. It records the
// exact argument vector, then uses real Git worktrees to exercise inspection.
func TestFakeWTProcess(t *testing.T) {
	if os.Getenv("WORKDRIVE_FAKE_WT") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	f, err := os.OpenFile(os.Getenv("WORKDRIVE_FAKE_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(f).Encode(args); err != nil {
		panic(err)
	}
	_ = f.Close()
	if reflect.DeepEqual(args, []string{"switch", "--help"}) {
		if os.Getenv("WORKDRIVE_FAKE_PATH_FLAG") == "1" {
			fmt.Println("--path <path>")
		} else {
			fmt.Println("--config-set <toml>")
		}
		os.Exit(0)
	}
	if os.Getenv("WORKDRIVE_FAKE_FAILURE") == "1" {
		fmt.Fprintln(os.Stderr, "hook blocked operation")
		os.Exit(2)
	}
	if len(args) < 4 || args[0] != "-C" {
		panic(fmt.Sprintf("invalid wt call %q", args))
	}
	source := args[1]
	args = args[2:]
	path, branch, base := "", "", ""
	create, remove := false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config-set":
			i++
			if !strings.HasPrefix(args[i], "worktree-path=") {
				panic(args[i])
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(args[i], "worktree-path=")), &path); err != nil {
				panic(err)
			}
		case "switch":
		case "remove":
			remove = true
		case "--path":
			i++
			path = args[i]
		case "--base":
			i++
			base = args[i]
		case "--create":
			create = true
		case "--format":
			i++
		case "--no-cd", "--foreground", "--no-delete-branch":
		default:
			branch = args[i]
		}
	}
	if remove {
		path = branch
	}
	if os.Getenv("WORKDRIVE_FAKE_WRONG_PATH") == "1" && !remove {
		path += "-different"
	}
	var gitArgs []string
	if remove {
		gitArgs = []string{"-C", source, "worktree", "remove", path}
	} else {
		gitArgs = []string{"-C", source, "worktree", "add"}
		if create {
			gitArgs = append(gitArgs, "-b", branch)
		}
		gitArgs = append(gitArgs, path)
		if create && base != "" {
			gitArgs = append(gitArgs, base)
		} else if !create {
			gitArgs = append(gitArgs, branch)
		}
	}
	cmd := exec.Command("git", gitArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "git %q: %s: %v\n", gitArgs, out, err)
		os.Exit(1)
	}
	fmt.Println("{}")
	os.Exit(0)
}

func command(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func tempRoot(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func repo(t *testing.T) string {
	t.Helper()
	path := filepath.Join(tempRoot(t), "source repo")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	command(t, "init", "-b", "main", path)
	command(t, "-C", path, "config", "user.name", "Test")
	command(t, "-C", path, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(path, "tracked"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, "-C", path, "add", "tracked")
	command(t, "-C", path, "commit", "-m", "initial")
	return path
}

func fake(t *testing.T) (*Driver, string) {
	t.Helper()
	wrapper := filepath.Join(tempRoot(t), "wt")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// strconv.Quote protects the test binary path when embedded in a script.
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+strconv.Quote(binary)+" -test.run=^TestFakeWTProcess$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(tempRoot(t), "vectors.jsonl")
	t.Setenv("WORKDRIVE_FAKE_WT", "1")
	t.Setenv("WORKDRIVE_FAKE_LOG", log)
	return New(wrapper), log
}

func vectors(t *testing.T, log string) [][]string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var call []string
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

func contains(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func TestCreateInspectRemoveAndArgumentVectors(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	d, log := fake(t)
	source := repo(t)
	// A quote, whitespace and a newline ensure destination is one argument and
	// the TOML override cannot be interpreted as extra configuration.
	path := filepath.Join(tempRoot(t), "the \"quoted\"\nworktree")
	r := Request{Source: source, Path: path, Branch: "feature/wip", Base: "main"}
	if err := d.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	state, err := d.Inspect(context.Background(), r)
	if err != nil || !state.Present || !state.Clean || len(state.Blockers) != 0 {
		t.Fatalf("inspect: %+v, %v", state, err)
	}
	if err := d.Create(context.Background(), r); err == nil {
		t.Fatal("create must not adopt existing worktree")
	}
	if err := d.Remove(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if command(t, "-C", source, "show-ref", "--verify", "refs/heads/feature/wip") == "" {
		t.Fatal("removed branch")
	}
	calls := vectors(t, log)
	if len(calls) != 3 { // help, switch, remove
		t.Fatalf("unexpected wt calls: %q", calls)
	}
	quoted, _ := json.Marshal(path)
	if !contains(calls[1], "worktree-path="+string(quoted)) || !contains(calls[1], "--create") || !contains(calls[1], "--base") {
		t.Fatalf("invalid create arguments: %q", calls[1])
	}
	if !reflect.DeepEqual(calls[2], []string{"-C", source, "remove", "--no-delete-branch", "--foreground", "--format", "json", path}) {
		t.Fatalf("unsafe remove vector: %q", calls[2])
	}
}

func TestCreateExistingBranchAndPathFlag(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	d, log := fake(t)
	t.Setenv("WORKDRIVE_FAKE_PATH_FLAG", "1")
	source := repo(t)
	command(t, "-C", source, "branch", "existing")
	r := Request{Source: source, Path: filepath.Join(tempRoot(t), "existing"), Branch: "existing", Base: "default"}
	if err := d.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	calls := vectors(t, log)
	if !contains(calls[1], "--path") || !contains(calls[1], r.Path) || contains(calls[1], "--create") || contains(calls[1], "--base") {
		t.Fatalf("invalid existing branch vector: %q", calls[1])
	}
}

func TestCreateRejectsConflictsAndWrongPath(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	d, log := fake(t)
	source := repo(t)
	command(t, "-C", source, "worktree", "add", "-b", "occupied", filepath.Join(tempRoot(t), "elsewhere"))
	r := Request{Source: source, Branch: "occupied", Path: filepath.Join(tempRoot(t), "mine")}
	if err := d.Create(context.Background(), r); err == nil || !strings.Contains(err.Error(), "checked out") {
		t.Fatalf("expected branch conflict: %v", err)
	}
	r.Branch = "free"
	if err := os.Mkdir(r.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := d.Create(context.Background(), r); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected destination collision: %v", err)
	}
	r.Path = filepath.Join(tempRoot(t), "wrong")
	t.Setenv("WORKDRIVE_FAKE_WRONG_PATH", "1")
	if err := d.Create(context.Background(), r); err == nil || !strings.Contains(err.Error(), "did not create the expected") {
		t.Fatalf("expected path mismatch: %v", err)
	}
	if len(vectors(t, log)) != 2 { // only wrong-path attempt reached wt
		t.Fatalf("unexpected calls: %q", vectors(t, log))
	}
}

func TestRemovalBlocksDirtyAndIgnoredFiles(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	d, log := fake(t)
	source := repo(t)
	r := Request{Source: source, Branch: "working", Path: filepath.Join(tempRoot(t), "working")}
	if err := d.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		prepare func()
		reset   func()
	}{
		{"unstaged", func() { os.WriteFile(filepath.Join(r.Path, "tracked"), []byte("modified"), 0600) }, func() { command(t, "-C", r.Path, "restore", "tracked") }},
		{"staged", func() {
			os.WriteFile(filepath.Join(r.Path, "tracked"), []byte("staged"), 0600)
			command(t, "-C", r.Path, "add", "tracked")
		}, func() {
			command(t, "-C", r.Path, "restore", "--staged", "tracked")
			command(t, "-C", r.Path, "restore", "tracked")
		}},
		{"untracked", func() { os.WriteFile(filepath.Join(r.Path, "untracked\nfile"), []byte("data"), 0600) }, func() { os.Remove(filepath.Join(r.Path, "untracked\nfile")) }},
		{"ignored", func() {
			os.WriteFile(filepath.Join(r.Path, ".gitignore"), []byte("cache/\n"), 0600)
			os.Mkdir(filepath.Join(r.Path, "cache"), 0700)
			os.WriteFile(filepath.Join(r.Path, "cache", "valuable"), []byte("data"), 0600)
		}, func() { os.RemoveAll(filepath.Join(r.Path, "cache")); os.Remove(filepath.Join(r.Path, ".gitignore")) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.prepare()
			state, err := d.Inspect(context.Background(), r)
			if err != nil || state.Clean || len(state.Blockers) == 0 {
				t.Fatalf("dirty status: %+v, %v", state, err)
			}
			if err := d.Remove(context.Background(), r); err == nil {
				t.Fatal("removed dirty worktree")
			}
			test.reset()
		})
	}
	if len(vectors(t, log)) != 2 { // help and switch; no remove
		t.Fatalf("unexpected wt remove: %q", vectors(t, log))
	}
}

func TestSourceIdentitySymlinkAndHookFailure(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	d, log := fake(t)
	source := repo(t)
	r := Request{Source: source, Branch: "branch", Path: filepath.Join(tempRoot(t), "owned")}
	if err := d.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	other := repo(t)
	r.Source = other
	state, err := d.Inspect(context.Background(), r)
	if err != nil || len(state.Blockers) == 0 || state.Present {
		t.Fatalf("foreign repo status: %+v, %v", state, err)
	}
	if err := d.Remove(context.Background(), r); err == nil {
		t.Fatal("removed foreign worktree")
	}
	r.Source = source
	ownedPath := r.Path
	command(t, "-C", r.Path, "switch", "-c", "different")
	state, err = d.Inspect(context.Background(), r)
	if err != nil || len(state.Blockers) == 0 || !state.Present {
		t.Fatalf("changed branch status: %+v, %v", state, err)
	}
	if err := d.Remove(context.Background(), r); err == nil {
		t.Fatal("removed worktree with a different branch")
	}
	command(t, "-C", r.Path, "switch", "branch")
	alias := filepath.Join(tempRoot(t), "alias")
	if err := os.Symlink(r.Path, alias); err != nil {
		t.Fatal(err)
	}
	r.Path = alias
	if err := d.Remove(context.Background(), r); err == nil {
		t.Fatal("accepted symlink destination")
	}
	r.Path = filepath.Join(alias, "nested")
	if err := d.Create(context.Background(), r); err == nil {
		t.Fatal("accepted symlink parent")
	}
	r.Path = filepath.Join(filepath.Dir(alias), "absent") // no registered worktree
	if err := d.Remove(context.Background(), r); err == nil {
		t.Fatal("removed unregistered path")
	}
	r.Path = ownedPath
	t.Setenv("WORKDRIVE_FAKE_FAILURE", "1")
	if err := d.Remove(context.Background(), r); err == nil || !strings.Contains(err.Error(), "hook blocked") {
		t.Fatalf("expected hook failure: %v", err)
	}
	state, err = d.Inspect(context.Background(), r)
	if err != nil || !state.Present {
		t.Fatalf("lost worktree after hook failure: %+v, %v", state, err)
	}
	if len(vectors(t, log)) != 3 {
		t.Fatalf("unexpected wt calls: %q", vectors(t, log))
	}
}

func TestRealWorktrunk(t *testing.T) {
	if os.Getenv("WORKDRIVE_REAL_WT") != "1" {
		t.Skip("set WORKDRIVE_REAL_WT=1 to run the isolated real-wt integration test")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	source := repo(t)
	r := Request{Source: source, Branch: "test-branch", Path: filepath.Join(tempRoot(t), "quoted \"worktree\" with spaces"), Base: "default"}
	d := New("wt")
	if err := d.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	command(t, "-C", source, "show-ref", "--verify", "refs/heads/test-branch")
}
