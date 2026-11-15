//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/testcontainers/testcontainers-go"
)

type action struct {
	Wait string `json:"wait"`
	Send string `json:"send"`
}

type command struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Dir     string            `json:"dir,omitempty"`
	Steps   []action          `json:"steps,omitempty"`
	Write   string            `json:"write,omitempty"`
	Data    string            `json:"data,omitempty"`
	Remove  string            `json:"remove,omitempty"`
}

type result struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Output   string `json:"output"`
	Error    string `json:"error"`
}

type fixture struct {
	t         *testing.T
	container testcontainers.Container
	root      string
	config    string
	env       map[string]string
	seeded    bool
}

var imageName = fmt.Sprintf("workdrive-e2e:run-%d", time.Now().UnixNano())
var imageReady bool

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if os.Getenv("DOCKER_HOST") == "" {
		endpoint, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
		if err == nil && strings.TrimSpace(string(endpoint)) != "" {
			t.Setenv("DOCKER_HOST", strings.TrimSpace(string(endpoint)))
		}
	}
	if host := os.Getenv("DOCKER_HOST"); strings.HasPrefix(host, "unix://") && host != "unix:///var/run/docker.sock" && os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE") == "" {
		t.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/var/run/docker.sock")
	}
	_, file, _, _ := runtime.Caller(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req := testcontainers.ContainerRequest{Image: imageName}
	if !imageReady {
		req.Image = ""
		req.FromDockerfile = testcontainers.FromDockerfile{
			Context: filepath.Join(filepath.Dir(file), "../.."), Dockerfile: "tests/e2e/Dockerfile",
			Repo: "workdrive-e2e", Tag: strings.TrimPrefix(imageName, "workdrive-e2e:"), KeepImage: true,
		}
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		t.Fatalf("start Docker E2E container (check Docker daemon): %v", err)
	}
	imageReady = true
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := container.Terminate(ctx); err != nil {
			t.Errorf("terminate container: %v", err)
		}
	})
	name := strings.ReplaceAll(t.Name(), "/", "-")
	f := &fixture{t: t, container: container, root: "/e2e/" + name}
	f.config = f.root + "/config.toml"
	f.env = map[string]string{
		"HOME": f.root + "/home", "XDG_CONFIG_HOME": f.root + "/config",
		"XDG_STATE_HOME": f.root + "/state", "TERM": "xterm-256color",
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
	}
	f.must(f.call(command{Command: "mkdir", Args: []string{"-p", f.root + "/home", f.root + "/sources", f.root + "/workspaces"}}))
	config := fmt.Sprintf("schema_version = 1\n[workspace]\nroot = %q\n[repositories]\n[[repositories.sources]]\nname = \"sources\"\npath = %q\nmax_depth = 3\n[worktree]\nbase_ref = \"main\"\n", f.root+"/workspaces", f.root+"/sources")
	f.write(f.config, config)
	return f
}

func (f *fixture) call(req command) result {
	f.t.Helper()
	if req.Env == nil {
		req.Env = f.env
	}
	data, err := json.Marshal(req)
	if err != nil {
		f.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	code, reader, err := f.container.Exec(ctx, []string{"/usr/local/bin/terminal", base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		f.t.Fatalf("container exec: %v", err)
	}
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, reader)
	if err != nil || code != 0 {
		f.t.Fatalf("terminal driver: exit %d, read %v: stdout=%s stderr=%s", code, err, stdout.String(), stderr.String())
	}
	var result result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		f.t.Fatalf("invalid terminal response: %v: stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if result.Error != "" {
		f.t.Fatalf("terminal driver: %s\n%s", result.Error, result.Output)
	}
	return result
}

func (f *fixture) must(r result) string {
	f.t.Helper()
	if r.ExitCode != 0 {
		f.t.Fatalf("expected success, exit %d: stdout=%s stderr=%s transcript=%s", r.ExitCode, r.Stdout, r.Stderr, r.Output)
	}
	return r.Stdout + r.Output
}

func (f *fixture) write(path, data string) {
	f.t.Helper()
	f.must(f.call(command{Write: path, Data: base64.StdEncoding.EncodeToString([]byte(data))}))
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	return f.must(f.call(command{Command: "git", Args: args}))
}

func (f *fixture) repo(name string) string {
	f.t.Helper()
	path := f.root + "/sources/" + name
	f.git("init", "-b", "main", path)
	f.write(path+"/README", name+" original\n")
	f.git("-C", path, "add", "README")
	f.git("-C", path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "initial")
	return path
}

func (f *fixture) cli(steps []action, args ...string) result {
	f.t.Helper()
	return f.call(command{Command: "/usr/local/bin/workdrive", Args: append([]string{"--config", f.config}, args...), Steps: steps})
}

func (f *fixture) create(key string, repos ...string) result {
	f.t.Helper()
	if len(repos) == 0 && !f.seeded {
		f.repo("discovery-fixture")
		f.seeded = true
	}
	args := []string{"new", key}
	for _, repo := range repos {
		args = append(args, "--repo", repo)
	}
	return f.cli([]action{
		{Wait: "Identity", Send: "\t\t\t\r"},
		{Wait: "Select repositories", Send: "\x0e"},
		{Wait: "Worktrees", Send: "\x0e"},
		{Wait: "Enter/y confirm and apply", Send: "y"},
		{Wait: "Operation completed successfully", Send: "\r"},
	}, args...)
}

func (f *fixture) list() []map[string]any {
	f.t.Helper()
	data := f.must(f.cli(nil, "list", "--json"))
	var items []map[string]any
	if err := json.Unmarshal([]byte(data), &items); err != nil {
		f.t.Fatalf("list JSON %q: %v", data, err)
	}
	return items
}

func (f *fixture) inspect(key string) map[string]any {
	f.t.Helper()
	data := f.must(f.cli(nil, "inspect", key, "--json"))
	var inspected struct {
		Workspace map[string]any `json:"workspace"`
	}
	if err := json.Unmarshal([]byte(data), &inspected); err != nil {
		f.t.Fatalf("inspect JSON %q: %v", data, err)
	}
	return inspected.Workspace
}

func resources(t *testing.T, workspace map[string]any) []any {
	t.Helper()
	for _, key := range []string{"resources", "Resources"} {
		if value, ok := workspace[key].([]any); ok {
			return value
		}
	}
	return nil
}

func TestCLIEndToEnd(t *testing.T) {
	t.Run("smoke-and-zero-repository-wizard", func(t *testing.T) {
		f := newFixture(t)
		f.must(f.cli(nil, "--help"))
		f.must(f.cli(nil, "config", "validate"))
		if got := len(f.list()); got != 0 {
			t.Fatalf("initial workspaces: %d", got)
		}
		if r := f.cli(nil, "new", "no-handoff", "--switch"); r.ExitCode == 0 || !strings.Contains(r.Stderr, "shell integration required") {
			t.Fatalf("unintegrated new --switch: %+v", r)
		}
		for _, args := range [][]string{{"new", "zero", "--name", "title"}, {"cleanup", "zero"}, {"remove", "repo", "zero", "--repo", "anything"}} {
			r := f.cli(nil, args...)
			if r.ExitCode == 0 || !strings.Contains(r.Stderr, "interactive terminal") {
				t.Fatalf("non-TTY %v: %+v", args, r)
			}
		}
		f.repo("discovery-fixture")
		f.seeded = true
		f.must(f.cli([]action{
			{Wait: "Identity", Send: "\t\t\t\r"},
			{Wait: "Select repositories", Send: "\x0e"},
			{Wait: "Worktrees", Send: "\x0e"},
			{Wait: "Enter/y confirm and apply", Send: "\x1b"},
		}, "new", "zero"))
		if len(f.list()) != 0 {
			t.Fatal("wizard cancellation created a workspace")
		}
		f.must(f.create("zero"))
		if len(f.list()) != 1 || len(resources(t, f.inspect("zero"))) != 0 {
			t.Fatal("zero-repository workspace not visible")
		}
		f.must(f.cli([]action{{Wait: "Confirm? [y/N]", Send: "n"}}, "cleanup", "zero"))
		if len(f.list()) != 1 {
			t.Fatal("canceled cleanup removed workspace")
		}
		f.must(f.cli([]action{{Wait: "Confirm? [y/N]", Send: "y"}}, "cleanup", "zero"))
		if len(f.list()) != 0 {
			t.Fatal("approved cleanup retained workspace")
		}
	})

	t.Run("worktrees-add-park-remove-cleanup", func(t *testing.T) {
		f := newFixture(t)
		one, two, three := f.repo("one"), f.repo("two"), f.repo("three")
		f.must(f.create("feature", one, two))
		if got := len(resources(t, f.inspect("feature"))); got != 2 {
			t.Fatalf("created worktrees: %d", got)
		}
		f.must(f.cli(nil, "add", "repo", "feature", "--repo", three))
		if got := len(resources(t, f.inspect("feature"))); got != 3 {
			t.Fatalf("added worktrees: %d", got)
		}
		for _, source := range []string{one, two, three} {
			worktree := f.root + "/workspaces/feature/" + filepath.Base(source)
			if branch := strings.TrimSpace(f.git("-C", worktree, "branch", "--show-current")); branch != "feature" {
				t.Fatalf("%s: branch %q", worktree, branch)
			}
			if !strings.Contains(f.git("-C", source, "worktree", "list", "--porcelain"), worktree) {
				t.Fatalf("worktree not registered: %s", worktree)
			}
			if data := f.must(f.call(command{Command: "cat", Args: []string{worktree + "/README"}})); data != filepath.Base(source)+" original\n" {
				t.Fatalf("worktree content %s: %q", worktree, data)
			}
		}
		f.must(f.cli(nil, "park", "feature"))
		for _, source := range []string{one, two, three} {
			if data := f.must(f.call(command{Command: "cat", Args: []string{f.root + "/workspaces/feature/" + filepath.Base(source) + "/README"}})); data != filepath.Base(source)+" original\n" {
				t.Fatalf("park changed worktree %s: %q", source, data)
			}
		}
		path := f.root + "/workspaces/feature"
		// An executable cannot cd its caller. Without initialization it must
		// explain setup, not implicitly nest a shell or activate the workspace.
		if r := f.cli(nil, "switch", "feature"); r.ExitCode == 0 || !strings.Contains(r.Stderr, "shell integration required") {
			t.Fatalf("unintegrated switch: %+v", r)
		}
		if state := f.inspect("feature")["state"]; state != "parked" {
			t.Fatalf("unintegrated switch activated workspace: %v", state)
		}
		if r := f.cli(nil, "switch", "feature", "--repo", "missing", "--print-path"); r.ExitCode == 0 || r.Stdout != "" {
			t.Fatalf("invalid recorded selector: %+v", r)
		}
		if state := f.inspect("feature")["state"]; state != "parked" {
			t.Fatalf("invalid selector activated workspace: %v", state)
		}
		if r := f.cli(nil, "switch", "feature", "--shell", "--print-path"); r.ExitCode == 0 {
			t.Fatal("accepted mutually exclusive navigation modes")
		}
		if out := f.must(f.cli(nil, "switch", "feature", "--repo", "one", "--print-path")); out != path+"/one\n" {
			t.Fatalf("recorded worktree print-path: %q", out)
		}
		if out := f.must(f.cli(nil, "switch", "feature", "--print-path")); out != path+"\n" {
			t.Fatalf("print-path: %q", out)
		}
		remove := []string{"remove", "repo", "feature", "--repo", three}
		f.must(f.cli([]action{{Wait: "Confirm? [y/N]", Send: "n"}}, remove...))
		if got := len(resources(t, f.inspect("feature"))); got != 3 {
			t.Fatalf("canceled remove changed resources: %d", got)
		}
		f.must(f.cli([]action{{Wait: "Confirm? [y/N]", Send: "y"}}, remove...))
		if got := len(resources(t, f.inspect("feature"))); got != 2 {
			t.Fatalf("approved remove left resources: %d", got)
		}
		f.must(f.cli([]action{{Wait: "Confirm? [y/N]", Send: "y"}}, "cleanup", "feature"))
		if len(f.list()) != 0 {
			t.Fatal("cleanup retained workspace")
		}
		for _, source := range []string{one, two, three} {
			f.git("-C", source, "show-ref", "--verify", "refs/heads/feature")
			if data := f.must(f.call(command{Command: "cat", Args: []string{source + "/README"}})); data != filepath.Base(source)+" original\n" {
				t.Fatalf("cleanup changed source %s: %q", source, data)
			}
			if strings.Contains(f.git("-C", source, "worktree", "list", "--porcelain"), path+"/") {
				t.Fatalf("cleanup left owned worktree at %s", source)
			}
		}
	})
	t.Run("refresh-rebuilds-only-owned-index", func(t *testing.T) {
		f := newFixture(t)
		f.must(f.create("indexed"))
		f.must(f.call(command{Remove: f.root + "/state/workdrive/registry.db"}))
		f.must(f.call(command{Command: "mkdir", Args: []string{"-p", f.root + "/workspaces/unknown"}}))
		f.must(f.cli(nil, "refresh"))
		if items := f.list(); len(items) != 1 || items[0]["key"] != "indexed" {
			t.Fatalf("refresh adopted unknown directories: %+v", items)
		}
		if result := f.cli(nil, "inspect", "unknown"); result.ExitCode == 0 {
			t.Fatal("unknown directory became owned")
		}
	})

	for _, scenario := range []struct {
		name   string
		modify func(*fixture, string, string)
	}{
		{"staged", func(f *fixture, source, tree string) {
			f.write(tree+"/README", "modified\n")
			f.git("-C", tree, "add", "README")
		}},
		{"unstaged", func(f *fixture, source, tree string) { f.write(tree+"/README", "modified\n") }},
		{"untracked", func(f *fixture, source, tree string) { f.write(tree+"/untracked", "keep\n") }},
		{"ignored", func(f *fixture, source, tree string) {
			f.write(source+"/.git/info/exclude", "ignored-file\n")
			f.write(tree+"/ignored-file", "keep\n")
		}},
		{"unknown-workspace-file", func(f *fixture, source, tree string) {
			f.write(f.root+"/workspaces/safety/notes.txt", "keep\n")
		}},
	} {
		t.Run("cleanup-blocks-"+scenario.name, func(t *testing.T) {
			f := newFixture(t)
			source := f.repo("source")
			f.must(f.create("safety", source))
			tree := f.root + "/workspaces/safety/source"
			scenario.modify(f, source, tree)
			f.must(f.cli([]action{{Wait: "Cleanup is blocked.", Send: "y"}}, "cleanup", "safety"))
			if len(f.list()) != 1 || !strings.Contains(f.git("-C", source, "worktree", "list", "--porcelain"), tree) {
				t.Fatal("blocked cleanup removed owned data")
			}
		})
	}

	t.Run("interrupted-create-resumes", func(t *testing.T) {
		f := newFixture(t)
		one, two := f.repo("one"), f.repo("two")
		wrapper := f.root + "/wt-wrapper"
		failure := f.root + "/fail-worktrunk"
		f.write(wrapper, fmt.Sprintf("#!/bin/sh\nif [ -e %q ]; then\n  case \"$*\" in *%q*switch*) printf 'injected failure\\n' >&2; exit 9;; esac\nfi\nexec /usr/local/bin/wt \"$@\"\n", failure, two))
		f.must(f.call(command{Command: "chmod", Args: []string{"755", wrapper}}))
		current := f.must(f.call(command{Command: "cat", Args: []string{f.config}}))
		f.write(f.config, current+fmt.Sprintf("executable = %q\n", wrapper))
		f.write(failure, "fail\n")
		steps := []action{
			{Wait: "Identity", Send: "\t\t\t\r"},
			{Wait: "Select repositories", Send: "\x0e"},
			{Wait: "Worktrees", Send: "\x0e"},
			{Wait: "Enter/y confirm and apply", Send: "y"},
			{Wait: "operation requires recovery:", Send: "\r"},
		}
		result := f.cli(steps, "new", "recover", "--repo", one, "--repo", two)
		if result.ExitCode == 0 {
			t.Fatalf("injected failure did not interrupt creation: %+v", result)
		}
		if state := f.inspect("recover")["state"]; state != "attention_required" {
			t.Fatalf("interrupted state: %v", state)
		}
		first := f.root + "/workspaces/recover/one"
		if !strings.Contains(f.git("-C", one, "worktree", "list", "--porcelain"), first) {
			t.Fatal("first completed worktree lost")
		}
		f.must(f.call(command{Remove: failure}))
		f.must(f.cli(nil, "resume", "recover"))
		if state := f.inspect("recover")["state"]; state != "active" {
			t.Fatalf("resumed state: %v", state)
		}
		if got := len(resources(t, f.inspect("recover"))); got != 2 {
			t.Fatalf("resumed worktrees: %d", got)
		}
	})
}
