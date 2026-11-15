package shellintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/spf13/cobra"
)

// The test executable doubles as a real, separate workdrive process. Cobra
// generates the actual completion scripts rather than testing a look-alike.
func TestMain(m *testing.M) {
	if os.Getenv("WORKDRIVE_TEST_HELPER") == "1" {
		os.Exit(shellHelper())
	}
	os.Exit(m.Run())
}

type helperCall struct {
	Args      []string
	Directive string
	Previous  string
	Leaked    bool
	Exported  bool
}

func shellHelper() int {
	call := helperCall{Args: os.Args[1:], Directive: os.Getenv(DirectiveEnv), Previous: os.Getenv(PreviousEnv)}
	invocation, err := Capture()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer invocation.Close()
	_, directiveLeaked := os.LookupEnv(DirectiveEnv)
	_, previousLeaked := os.LookupEnv(PreviousEnv)
	call.Leaked = directiveLeaked || previousLeaked
	_, call.Exported = os.LookupEnv("_workdrive_previous_workspace")
	if path := os.Getenv("TEST_CALL_LOG"); path != "" {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		_ = json.NewEncoder(file).Encode(call)
		_ = file.Close()
	}
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "completion" || args[0] == "__complete" || args[0] == "__completeNoDesc") {
		root := &cobra.Command{Use: "workdrive", SilenceErrors: true, SilenceUsage: true}
		root.SetOut(os.Stdout)
		root.SetErr(os.Stderr)
		root.AddCommand(&cobra.Command{Use: "switch", Run: func(*cobra.Command, []string) {}, ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			if invocation.PreviousToken() != "" {
				return []string{"previous-available\tPrevious workspace"}, cobra.ShellCompDirectiveNoFileComp
			}
			return []string{"workspace\tWorkspace"}, cobra.ShellCompDirectiveNoFileComp
		}})
		root.AddCommand(&cobra.Command{Use: "completion [shell]", RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("need shell")
			}
			if args[0] == "bash" {
				return root.GenBashCompletionV2(os.Stdout, true)
			}
			return root.GenZshCompletion(os.Stdout)
		}})
		root.SetArgs(args)
		if root.Execute() != nil {
			return 1
		}
		return 0
	}
	if len(args) == 0 {
		return 1
	}
	if os.Getenv("TEST_STDOUT") != "" {
		fmt.Fprint(os.Stdout, os.Getenv("TEST_STDOUT"))
	}
	if os.Getenv("TEST_STDERR") != "" {
		fmt.Fprint(os.Stderr, os.Getenv("TEST_STDERR"))
	}
	switch args[0] {
	case "navigate":
		var previous *Identity
		if os.Getenv("TEST_REPLACE") == "1" {
			previous = &Identity{ID: "origin-id", Key: "-", Path: "/origin path/\n"}
		}
		if err := invocation.Preflight(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := invocation.Publish(os.Getenv("TEST_DESTINATION"), previous); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "raw":
		record, err := os.ReadFile(os.Getenv("TEST_RAW_RECORD"))
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(call.Directive, record, 0600); err != nil {
			panic(err)
		}
	case "echo-stdin":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "wait":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		fmt.Fprintln(os.Stdout, "WORKDRIVE_READY")
		<-ctx.Done()
		return 130
	case "no-handoff":
	default:
		return 1
	}
	status, _ := strconv.Atoi(os.Getenv("TEST_STATUS"))
	return status
}

type shellCase struct{ name, path string }

var realShells = []shellCase{{"bash-system", "/bin/bash"}, {"bash-modern", "/opt/homebrew/bin/bash"}, {"zsh", "/bin/zsh"}}

type shellFixture struct {
	t              *testing.T
	shell          shellCase
	env            []string
	dir, init, tmp string
	result, log    string
}

func newShellFixture(t *testing.T, shell shellCase) *shellFixture {
	t.Helper()
	if _, err := os.Stat(shell.path); err != nil {
		t.Skipf("shell unavailable: %s", shell.path)
	}
	dir := t.TempDir()
	// macOS's /var can be a symlink; compare against the shell's physical cwd.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	bin, tmp := filepath.Join(dir, "bin"), filepath.Join(dir, "tmp space 'quote' $literal\nline")
	for _, path := range []string{bin, tmp} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(bin, "workdrive")); err != nil {
		t.Fatal(err)
	}
	kind := "bash"
	if shell.name == "zsh" {
		kind = "zsh"
	}
	script, err := Init(kind)
	if err != nil {
		t.Fatal(err)
	}
	init := filepath.Join(dir, "init.sh")
	if err := os.WriteFile(init, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	f := &shellFixture{t: t, shell: shell, dir: dir, tmp: tmp, init: init, result: filepath.Join(dir, "result"), log: filepath.Join(dir, "calls.jsonl")}
	f.env = append(os.Environ(), "WORKDRIVE_TEST_HELPER=1", "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0", "PATH="+bin+":"+pathWithoutWorkdrive(), "HOME="+dir, "ZDOTDIR="+dir, "TMPDIR="+tmp, "INIT="+init, "RESULT="+f.result, "TEST_CALL_LOG="+f.log)
	return f
}

// pathWithoutWorkdrive drops PATH entries holding a real workdrive, so a
// fixture that deletes its fake binary sees "command not found" rather than
// falling back to whatever is installed on the developer's machine.
func pathWithoutWorkdrive() string {
	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(dir, "workdrive")); err == nil {
			continue
		}
		kept = append(kept, dir)
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

func (f *shellFixture) command(script string, interactive bool) *exec.Cmd {
	args := []string{"--noprofile", "--norc"}
	if f.shell.name == "zsh" {
		args = []string{"-f"}
	}
	if interactive {
		args = append(args, "-i")
	}
	args = append(args, "-c", script)
	cmd := exec.Command(f.shell.path, args...)
	cmd.Dir = f.dir
	cmd.Env = f.env
	return cmd
}

func (f *shellFixture) run(script string, environment ...string) (string, string) {
	f.t.Helper()
	cmd := f.command(script, false)
	cmd.Env = append(cmd.Env, environment...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("shell: %v\nstdout: %s\nstderr: %s", err, &stdout, &stderr)
	}
	f.assertClean()
	return stdout.String(), stderr.String()
}

func (f *shellFixture) assertClean() {
	f.t.Helper()
	entries, err := os.ReadDir(f.tmp)
	if err != nil || len(entries) != 0 {
		f.t.Fatalf("temporary capability leaked: %v, %v", entries, err)
	}
}

func (f *shellFixture) state() []string {
	f.t.Helper()
	data, err := os.ReadFile(f.result)
	if err != nil {
		f.t.Fatal(err)
	}
	fields := strings.Split(string(data), "\x00")
	return fields[:len(fields)-1]
}

func (f *shellFixture) calls() []helperCall {
	f.t.Helper()
	file, err := os.Open(f.log)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var calls []helperCall
	for {
		var call helperCall
		if err := decoder.Decode(&call); err == io.EOF {
			break
		} else if err != nil {
			f.t.Fatal(err)
		}
		if call.Leaked || call.Exported {
			f.t.Fatalf("integration environment inherited: %#v", call)
		}
		calls = append(calls, call)
	}
	return calls
}

const saveState = `builtin printf '%s\0%s\0%s\0' "$PWD" "${_workdrive_previous_workspace-}" "$_result_status" > "$RESULT"`

func TestRealShellLiteralNavigationAndHistory(t *testing.T) {
	for _, shell := range realShells {
		t.Run(shell.name, func(t *testing.T) {
			f := newShellFixture(t, shell)
			destination := filepath.Join(f.dir, "space 'quote' ; $(touch PWNED) `touch PWNED` $dollar \\\ntrailing\n\n")
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			stdout, stderr := f.run(`builtin source "$INIT"
_workdrive_previous_workspace=old-token
builtin source "$INIT"
workdrive navigate 'argument with spaces' '$literal;no code'
_result_status=$?
`+saveState, "TEST_DESTINATION="+destination, "TEST_REPLACE=1", "TEST_STDOUT=normal stdout\n", "TEST_STDERR=normal stderr\n")
			if stdout != "normal stdout\n" || stderr != "normal stderr\n" {
				t.Fatalf("stdio changed: %q %q", stdout, stderr)
			}
			state := f.state()
			wantToken, _ := EncodeIdentity(Identity{ID: "origin-id", Key: "-", Path: "/origin path/\n"})
			if state[0] != destination || state[1] != wantToken || state[2] != "0" {
				t.Fatalf("state: %#v", state)
			}
			calls := f.calls()
			last := calls[len(calls)-1]
			if last.Previous != "old-token" || !strings.HasPrefix(last.Directive, f.tmp+"/") || len(last.Args) != 3 || last.Args[1] != "argument with spaces" || last.Args[2] != "$literal;no code" {
				t.Fatalf("invocation: %#v", last)
			}
			if _, err := os.Stat(filepath.Join(f.dir, "PWNED")); !os.IsNotExist(err) {
				t.Fatal("handoff was interpreted as code")
			}
		})
	}
}

func TestRealShellStatusesAndHistoryPreservation(t *testing.T) {
	for _, shell := range realShells {
		for _, test := range []struct {
			name, command, status, replacement string
			missing                            bool
		}{
			{name: "keep", command: "navigate", status: "0"},
			{name: "binary failure", command: "navigate", status: "37", replacement: "1"},
			{name: "failed cd", command: "navigate", status: "0", missing: true, replacement: "1"},
			{name: "no directive", command: "no-handoff", status: "0"},
		} {
			t.Run(shell.name+"/"+test.name, func(t *testing.T) {
				f := newShellFixture(t, shell)
				destination := filepath.Join(f.dir, "destination")
				if !test.missing {
					if err := os.Mkdir(destination, 0700); err != nil {
						t.Fatal(err)
					}
				}
				stdout, _ := f.run(`builtin source "$INIT"
_workdrive_previous_workspace=old-token
if workdrive "$TEST_COMMAND"; then _result_status=0; else _result_status=$?; fi
`+saveState, "TEST_COMMAND="+test.command, "TEST_STATUS="+test.status, "TEST_DESTINATION="+destination, "TEST_REPLACE="+test.replacement, "TEST_STDOUT=stdout\n")
				state := f.state()
				wantDir := f.dir
				if test.name == "keep" {
					wantDir = destination
				}
				if state[0] != wantDir || state[1] != "old-token" || stdout != "stdout\n" {
					t.Fatalf("state %#v stdout %q", state, stdout)
				}
				if test.missing {
					if state[2] == "0" {
						t.Fatal("failed cd succeeded")
					}
				} else if state[2] != test.status {
					t.Fatalf("status %q want %s", state[2], test.status)
				}
				f.calls()
			})
		}
	}
}

func TestRealShellRejectsMalformedRecords(t *testing.T) {
	for _, shell := range realShells {
		t.Run(shell.name, func(t *testing.T) {
			f := newShellFixture(t, shell)
			destination := filepath.Join(f.dir, "safe")
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			valid := "1\x00" + destination + "\x00keep\x00\x00"
			records := []string{
				"garbage", "1\x00", strings.TrimSuffix(valid, "\x00"), valid + "extra", valid + "\x00",
				"2\x00" + destination + "\x00keep\x00\x00",
				"1\x00relative\x00keep\x00\x00", "1\x00\x00keep\x00\x00",
				"1\x00" + destination + "\x00unknown\x00\x00",
				"1\x00" + destination + "\x00keep\x00unexpected\x00",
				"1\x00" + destination + "\x00replace\x00\x00",
				"1\x00" + destination + "\x00replace\x00v1.$(touch PWNED)\x00",
				"1\x00" + destination + "\x00replace\x00v1.\x00",
				"1\x00" + destination + "\x00replace\x00v1.bad.token\x00",
			}
			for index, record := range records {
				t.Run(strconv.Itoa(index), func(t *testing.T) {
					path := filepath.Join(f.dir, "record")
					if err := os.WriteFile(path, []byte(record), 0600); err != nil {
						t.Fatal(err)
					}
					_, stderr := f.run(`builtin source "$INIT"
_workdrive_previous_workspace=old-token
if workdrive raw; then _result_status=0; else _result_status=$?; fi
`+saveState, "TEST_RAW_RECORD="+path)
					state := f.state()
					if state[0] != f.dir || state[1] != "old-token" || state[2] == "0" || !strings.Contains(stderr, "invalid shell navigation handoff") {
						t.Fatalf("malformed record accepted: state %#v stderr %q", state, stderr)
					}
				})
			}
		})
	}
}

func TestRealShellCompletionAndReinitialization(t *testing.T) {
	for _, shell := range realShells {
		t.Run(shell.name, func(t *testing.T) {
			f := newShellFixture(t, shell)
			prefix, check := "", "builtin complete -p workdrive >/dev/null"
			if shell.name == "zsh" {
				prefix = "autoload -Uz compinit; compinit -D -i\n"
				check = "[[ ${_comps[workdrive]} == _workdrive ]]"
			}
			stdout, _ := f.run(prefix+`_workdrive_previous_workspace=old-token
builtin export _workdrive_previous_workspace
builtin source "$INIT"
builtin source "$INIT"
`+check+` || exit 9
workdrive __complete switch ''
workdrive __completeNoDesc switch ''
_result_status=$?
`+saveState, DirectiveEnv+"=/must/not/be/passed")
			state := f.state()
			if state[0] != f.dir || state[1] != "old-token" || state[2] != "0" || strings.Count(stdout, "previous-available") != 2 {
				t.Fatalf("completion state %#v output %q", state, stdout)
			}
			count := 0
			for _, call := range f.calls() {
				if len(call.Args) > 0 && strings.HasPrefix(call.Args[0], "__complete") {
					count++
					if call.Directive != "" || call.Previous != "old-token" {
						t.Fatalf("completion capability: %#v", call)
					}
				}
			}
			if count != 2 {
				t.Fatalf("completion calls: %d", count)
			}
		})
	}
}

func TestRealShellStdinAndMktempFailure(t *testing.T) {
	for _, shell := range realShells {
		t.Run(shell.name, func(t *testing.T) {
			f := newShellFixture(t, shell)
			cmd := f.command(`builtin source "$INIT"; workdrive echo-stdin`, false)
			cmd.Stdin = strings.NewReader("literal stdin\n\n")
			output, err := cmd.Output()
			if err != nil || string(output) != "literal stdin\n\n" {
				t.Fatalf("stdin: %q, %v", output, err)
			}
			f.assertClean()
			if err := os.WriteFile(filepath.Join(f.dir, "bin", "mktemp"), []byte("#!/bin/sh\nexit 23\n"), 0700); err != nil {
				t.Fatal(err)
			}
			_, stderr := f.run(`builtin source "$INIT"
_workdrive_previous_workspace=old-token
if workdrive no-handoff; then _result_status=0; else _result_status=$?; fi
` + saveState)
			state := f.state()
			if state[0] != f.dir || state[1] != "old-token" || state[2] != "23" || !strings.Contains(stderr, "cannot create private") {
				t.Fatalf("mktemp failure: %#v, %q", state, stderr)
			}
			for _, call := range f.calls() {
				if len(call.Args) > 0 && call.Args[0] == "no-handoff" {
					t.Fatal("binary invoked despite mktemp failure")
				}
			}
		})
	}
}

func TestRealShellMissingBinaryAndStrictOptions(t *testing.T) {
	for _, shell := range realShells {
		t.Run(shell.name, func(t *testing.T) {
			f := newShellFixture(t, shell)
			destination := filepath.Join(f.dir, "strict destination")
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			f.run(`set -eu
builtin source "$INIT"
workdrive navigate
_result_status=$?
`+saveState, "TEST_DESTINATION="+destination)
			if state := f.state(); state[0] != destination || state[1] != "" || state[2] != "0" {
				t.Fatalf("strict shell: %#v", state)
			}
			_, _ = f.run(`builtin source "$INIT"
_workdrive_previous_workspace=old-token
command rm -- "$TEST_BINARY"
if workdrive no-handoff; then _result_status=0; else _result_status=$?; fi
`+saveState, "TEST_BINARY="+filepath.Join(f.dir, "bin", "workdrive"))
			state := f.state()
			if state[0] != f.dir || state[1] != "old-token" || state[2] != "127" {
				t.Fatalf("missing binary: %#v", state)
			}
		})
	}
}

func TestRealShellInterruptCleanupAndTraps(t *testing.T) {
	for _, shell := range realShells {
		t.Run(shell.name, func(t *testing.T) {
			f := newShellFixture(t, shell)
			script := `PS1=''
PROMPT_COMMAND=':'
builtin trap 'builtin printf "%s\n" user-trap >/dev/null' INT
_before_trap=$(builtin trap -p INT)
builtin source "$INIT"
_workdrive_previous_workspace=old-token
if workdrive wait; then _result_status=0; else _result_status=$?; fi
_after_trap=$(builtin trap -p INT)
[[ $_before_trap == "$_after_trap" ]] || exit 18
[[ $PROMPT_COMMAND == : ]] || exit 19
` + saveState + `
builtin printf '%s\n' WORKDRIVE_FINISHED
`
			// Zsh uses `trap` rather than Bash's `trap -p` to print declarations.
			if shell.name == "zsh" {
				script = strings.ReplaceAll(script, "builtin trap -p INT", "builtin trap")
			}
			cmd := f.command(script, true)
			terminal, err := pty.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			defer func() { _ = cmd.Process.Kill() }()
			output := make(chan string, 100)
			go func() {
				buffer := make([]byte, 4096)
				for {
					n, err := terminal.Read(buffer)
					if n != 0 {
						output <- string(buffer[:n])
					}
					if err != nil {
						close(output)
						return
					}
				}
			}()
			var transcript strings.Builder
			deadline := time.After(10 * time.Second)
			ready := false
			for !ready {
				select {
				case chunk, ok := <-output:
					transcript.WriteString(chunk)
					if !ok {
						t.Fatalf("shell ended before ready: %s", &transcript)
					}
					ready = strings.Contains(transcript.String(), "WORKDRIVE_READY")
				case <-deadline:
					t.Fatalf("shell did not become ready: %s", &transcript)
				}
			}
			if _, err := terminal.Write([]byte{3}); err != nil {
				t.Fatal(err)
			}
			for {
				select {
				case chunk, ok := <-output:
					transcript.WriteString(chunk)
					if !ok {
						if err := cmd.Wait(); err != nil {
							t.Fatalf("shell after interrupt: %v\n%s", err, &transcript)
						}
						state := f.state()
						if state[0] != f.dir || state[1] != "old-token" || state[2] != "130" || !strings.Contains(transcript.String(), "WORKDRIVE_FINISHED") {
							t.Fatalf("interrupt state %#v\n%s", state, &transcript)
						}
						f.assertClean()
						return
					}
				case <-deadline:
					t.Fatalf("interrupt timed out: %s", &transcript)
				}
			}
		})
	}
}
