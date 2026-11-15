//go:build e2e

package e2e

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// Run the actual interactive shell in the existing container PTY. Commands are
// passed as a script, not typed, so marker waits cannot match echoed input.
func (f *fixture) shell(shell, script string, steps []action) result {
	f.t.Helper()
	args := []string{"--noprofile", "--norc", "-i", "-c"}
	setup := ""
	if shell == "zsh" {
		args = []string{"-f", "-i", "-c"}
		setup = "autoload -Uz compinit; compinit -D -u || exit 10\n"
	}
	setup += fmt.Sprintf(`
export SHELL=/bin/%s
E2E_SHELL=${SHELL##*/}
original_pid=$$
original_tty=$(stty -g)
trap 'code=$?; builtin printf "\nE2E_SHELL_DONE\n"; exit "$code"' EXIT
wd() { workdrive --config %s "$@"; }
check_cwd() {
  if [ "$PWD" != "$1" ] || [ "$$" != "$original_pid" ]; then
    printf 'wrong shell/cwd: pid=%%s expected=%%s cwd=<%%s> expected=<%%s>\n' "$$" "$original_pid" "$PWD" "$1" >&2
    exit 21
  fi
}
check_tty() { [ "$(stty -g)" = "$original_tty" ] || exit 22; }
eval "$(command workdrive --config %s config shell init %s)" || exit 11
eval "$(command workdrive --config %s config shell init %s)" || exit 12
`, shell, shellQuote(f.config), shellQuote(f.config), shell, shellQuote(f.config), shell)
	setup += script + "\ncheck_tty\nexit 0\n"
	steps = append(steps, action{Wait: "E2E_SHELL_DONE"})
	return f.call(command{Command: "/bin/" + shell, Args: append(args, setup), Dir: f.root, Steps: steps})
}

func TestShellNavigationEndToEnd(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			f := newFixture(t)
			one := f.repo("one")
			f.must(f.create("alpha", one))
			f.must(f.create("beta"))
			f.must(f.create("-"))
			f.must(f.cli(nil, "park", "beta"))
			a, b := f.root+"/workspaces/alpha", f.root+"/workspaces/beta"
			f.write(a+"/one/README", "dirty but navigable\n")
			script := fmt.Sprintf(`
if wd switch -; then exit 29; fi
check_cwd %s
wd switch alpha || exit 30
check_cwd %s
wd switch beta || exit 31
check_cwd %s
# Reinitializing must not reset an already established previous workspace.
eval "$(command workdrive config shell init "$E2E_SHELL")" || exit 49
completed=$(wd __completeNoDesc switch - --repo '') || exit 47
[ "$completed" = "$(printf 'one\n:4')" ] || { printf 'unexpected completion: <%%s>\n' "$completed" >&2; exit 48; }
wd switch - || exit 32
check_cwd %s
# Moving inside alpha must not rotate beta out of previous-workspace history.
wd switch alpha --repo one || exit 33
check_cwd %s
wd switch - || exit 34
check_cwd %s
wd switch - --repo one || exit 35
check_cwd %s
# Printing beta's path may activate it, but must not navigate or change history.
wd switch beta --print-path > "$HOME/printed-path" || exit 36
printed=$(command cat "$HOME/printed-path") || exit 36
[ "$printed" = %s ] || exit 37
check_cwd %s
wd switch - || exit 38
check_cwd %s
# Actual cwd, including a manual cd into a worktree, determines the origin.
builtin cd -- %s || exit 39
wd switch beta || exit 40
wd switch - || exit 41
check_cwd %s
# An outside-workspace origin retains the shell's previous token (beta).
builtin cd -- %s || exit 42
wd switch alpha || exit 43
wd switch - || exit 44
check_cwd %s
# A new shell must not inherit the parent's non-exported history.
/bin/%s -c 'eval "$(command workdrive config shell init %s)"; if workdrive --config "$1" switch -; then exit 1; fi' e2e %s || exit 45
# A literal '-' workspace remains addressable, distinct from history.
wd switch -- - || exit 46
check_cwd %s
`, shellQuote(f.root), shellQuote(a), shellQuote(b), shellQuote(a), shellQuote(a+"/one"), shellQuote(b), shellQuote(a+"/one"), shellQuote(b), shellQuote(a+"/one"), shellQuote(b), shellQuote(a+"/one"), shellQuote(a), shellQuote(f.root), shellQuote(b), shell, shell, shellQuote(f.config), shellQuote(f.root+"/workspaces/-"))
			f.must(f.shell(shell, script, nil))
		})
	}
}

func TestShellPickerDashboardEndToEnd(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			f := newFixture(t)
			f.must(f.create("picked"))
			script := fmt.Sprintf(`
wd switch || exit 30
check_cwd %s
check_tty
builtin cd -- %s || exit 31
wd || exit 32
check_cwd %s
check_tty
builtin cd -- %s || exit 33
# Embedded n creation returns to the dashboard; it is not new --switch.
wd || exit 34
check_cwd %s
`, shellQuote(f.root+"/workspaces/picked"), shellQuote(f.root), shellQuote(f.root+"/workspaces/picked"), shellQuote(f.root), shellQuote(f.root))
			f.must(f.shell(shell, script, []action{
				{Wait: "Select workspace", Send: "\r"},
				{Wait: "picked", Send: "\r"},
				{Wait: "Enter switch", Send: "n"},
				{Wait: "Identity", Send: "embedded\t\t\t\r"},
				{Wait: f.root + "/sources/discovery-fixture", Send: "\x0e"},
				{Wait: "Worktrees", Send: "\x0e"},
				{Wait: "Enter/y confirm and apply", Send: "y"},
				{Wait: "Operation completed successfully", Send: "\r"},
				{Wait: "Enter switch", Send: "q"},
			}))
			if len(f.list()) != 2 {
				t.Fatal("embedded creation did not return a created workspace")
			}
		})
	}
}

func TestShellNewSwitchEndToEnd(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			f := newFixture(t)
			f.repo("discovery-fixture")
			script := fmt.Sprintf(`
wd new initial --switch || exit 30
check_cwd %s
builtin cd -- %s || exit 31
wd new canceled --switch || exit 32
check_cwd %s
[ ! -e %s ] || exit 33
`, shellQuote(f.root+"/workspaces/edited"), shellQuote(f.root), shellQuote(f.root), shellQuote(f.root+"/workspaces/canceled"))
			steps := []action{
				{Wait: "Identity", Send: "\x15edited\t\t\t\r"},
				{Wait: f.root + "/sources/discovery-fixture", Send: "\x0e"},
				{Wait: "Worktrees", Send: "\x0e"},
				{Wait: "Enter/y confirm and apply", Send: "y"},
				{Wait: "Enter switch to workspace", Send: "\r"},
				{Wait: "Identity", Send: "\t\t\t\r"},
				{Wait: f.root + "/sources/discovery-fixture", Send: "\x0e"},
				{Wait: "Worktrees", Send: "\x0e"},
				{Wait: "Enter/y confirm and apply", Send: "\x1b"},
			}
			for i, dismiss := range []string{"q", "\x1b", "\x03"} {
				script += fmt.Sprintf("wd new dismissed-%d --switch || exit 34\ncheck_cwd %s\n", i, shellQuote(f.root))
				steps = append(steps,
					action{Wait: "Identity", Send: "\t\t\t\r"},
					action{Wait: f.root + "/sources/discovery-fixture", Send: "\x0e"},
					action{Wait: "Worktrees", Send: "\x0e"},
					action{Wait: "Enter/y confirm and apply", Send: "y"},
					action{Wait: "Enter switch to workspace", Send: dismiss})
			}
			f.must(f.shell(shell, script, steps))
			if got := len(f.list()); got != 4 {
				t.Fatalf("successful/dismissed creations: %d, want 4", got)
			}
		})
	}
}

func TestExplicitNestedShellEndToEnd(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			f := newFixture(t)
			f.must(f.create("nested"))
			// An rc file marks the actual nested interactive shell, not the parent.
			rc := ".bashrc"
			if shell == "zsh" {
				rc = ".zshrc"
			}
			f.write(f.root+"/home/"+rc, "printf '\\nE2E_NESTED_READY\\n'\n")
			script := fmt.Sprintf("export E2E_PARENT_PID=$$\nwd switch nested --shell || exit 30\ncheck_cwd %s\n", shellQuote(f.root))
			f.must(f.shell(shell, script, []action{{Wait: "E2E_NESTED_READY", Send: fmt.Sprintf("[ \"$PWD\" = %s ] && [ \"$$\" != \"$E2E_PARENT_PID\" ]; code=$?; exit $code\r", shellQuote(f.root+"/workspaces/nested"))}}))
		})
	}
}

func (f *fixture) completion(args ...string) ([]string, int) {
	f.t.Helper()
	r := f.cli(nil, append([]string{"__complete"}, args...)...)
	f.must(r)
	lines := strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], ":") {
		f.t.Fatalf("missing Cobra directive: %+v", r)
	}
	directive, err := strconv.Atoi(strings.TrimPrefix(lines[len(lines)-1], ":"))
	if err != nil {
		f.t.Fatalf("invalid Cobra directive: %+v", r)
	}
	values := make([]string, 0, len(lines)-1)
	for _, line := range lines[:len(lines)-1] {
		value, _, _ := strings.Cut(line, "\t")
		values = append(values, value)
	}
	return values, directive
}

func (f *fixture) completeWant(want []string, directive int, args ...string) {
	f.t.Helper()
	got, gotDirective := f.completion(args...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) || gotDirective != directive {
		f.t.Fatalf("completion %q: values=%q directive=%d, want %q directive=%d", args, got, gotDirective, want, directive)
	}
}

func (f *fixture) snapshot() string {
	f.t.Helper()
	// Compare names and bytes, including Git refs, manifests and the SQLite index.
	return f.must(f.call(command{Command: "sh", Args: []string{"-c", "find " + shellQuote(f.root) + " -type f -exec sha256sum {} + | LC_ALL=C sort"}}))
}

func TestCompletionEndToEnd(t *testing.T) {
	t.Run("context-matrix-read-only", func(t *testing.T) {
		f := newFixture(t)
		one, two := f.repo("one"), f.repo("two")
		f.git("-C", one, "branch", "common")
		f.git("-C", two, "branch", "common")
		f.git("-C", one, "branch", "one-only")
		f.must(f.create("alpha", one))
		f.must(f.create("beta"))
		f.must(f.cli(nil, "park", "beta"))
		config := f.must(f.call(command{Command: "cat", Args: []string{f.config}}))
		f.write(f.config, config+"\n[profiles.development]\n[profiles.experimental]\n")
		// Any attempted fetch would fail, and local ref completion must still work.
		f.git("-C", one, "remote", "add", "origin", "https://invalid.example.invalid/no-network.git")
		before := f.snapshot()
		f.completeWant([]string{"alpha", "beta"}, 4, "switch", "")
		f.completeWant([]string{"alpha", "beta"}, 4, "inspect", "")
		f.completeWant([]string{"one"}, 4, "switch", "alpha", "--repo", "")
		f.completeWant([]string{"one"}, 4, "switch", "--repo", "one", "alpha", "--repo=")
		f.completeWant([]string{}, 4, "switch", "unknown", "--repo", "")
		f.completeWant([]string{"one"}, 4, "remove", "repo", "alpha", "--repo", "")
		f.completeWant([]string{one, two, f.root + "/sources/discovery-fixture"}, 4, "new", "--repo", f.root+"/sources/")
		f.completeWant([]string{two, f.root + "/sources/discovery-fixture"}, 4, "new", "--repo="+one, "--repo", f.root+"/sources/")
		f.completeWant([]string{two, f.root + "/sources/discovery-fixture"}, 4, "add", "repo", "alpha", "--repo", f.root+"/sources/")
		f.completeWant([]string{"development", "experimental"}, 4, "--profile", "")
		f.completeWant([]string{"active", "all", "parked"}, 4, "list", "--state", "")
		f.completeWant([]string{"bash", "zsh"}, 4, "config", "shell", "init", "")
		f.completeWant([]string{"toml"}, 8, "--config", "")
		f.completeWant([]string{"default"}, 4, "new", "--base", "")
		f.completeWant([]string{"common", "default", "main"}, 4, "new", "--repo", one, "--repo", two, "--base", "")
		f.completeWant([]string{}, 4, "new", "")
		f.completeWant([]string{}, 4, "new", "--name", "")
		if after := f.snapshot(); before != after {
			t.Fatalf("completion changed fixture files\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
	t.Run("absent-state-and-malformed-config", func(t *testing.T) {
		f := newFixture(t)
		f.completeWant([]string{}, 4, "switch", "")
		f.must(f.call(command{Command: "sh", Args: []string{"-c", "test ! -e " + shellQuote(f.root+"/state")}}))
		f.write(f.config, "not valid [toml")
		for _, shell := range []string{"bash", "zsh"} {
			f.must(f.cli(nil, "config", "shell", "init", shell))
			f.must(f.cli(nil, "completion", shell))
		}
		f.completeWant([]string{"active", "all", "parked"}, 4, "list", "--state", "")
		f.completeWant([]string{"bash", "zsh"}, 4, "config", "shell", "init", "")
		f.must(f.call(command{Command: "sh", Args: []string{"-c", "test ! -e " + shellQuote(f.root+"/state")}}))
	})
}
