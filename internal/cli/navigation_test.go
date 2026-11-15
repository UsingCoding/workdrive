package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/shellintegration"
)

type navigationFixture struct {
	configPath string
	service    *app.Service
	workspace  *domain.Workspace
}

func cliNavigationFixture(t *testing.T) navigationFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\n[workspace]\nroot = "+quoteTOML(filepath.Join(root, "workspaces"))+"\n[repositories]\nsources = []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := app.Open(cfg, stateDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	plan, err := s.PlanNew(app.NewRequest{Key: "target"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Park(w.Key)
	if err != nil {
		t.Fatal(err)
	}
	return navigationFixture{path, s, w}
}

func executeNavigation(t *testing.T, fixture navigationFixture, integration *shellintegration.Invocation, args ...string) (string, string, error) {
	t.Helper()
	var out, errs bytes.Buffer
	c := &command{out: &out, errOut: &errs, integration: integration}
	cmd := c.root()
	cmd.SetOut(&out)
	cmd.SetErr(&errs)
	cmd.SetArgs(append([]string{"--config", fixture.configPath}, args...))
	err := cmd.Execute()
	if c.service != nil {
		c.service.Close()
	}
	return out.String(), errs.String(), err
}

func TestIntegratedSwitchAndPreflight(t *testing.T) {
	f := cliNavigationFixture(t)
	for _, args := range [][]string{{"switch", f.workspace.Key}, {"new", "other", "--switch"}} {
		_, _, err := executeNavigation(t, f, nil, args...)
		if err == nil || !strings.Contains(err.Error(), "shell integration required") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	w, err := f.service.Find(f.workspace.Key)
	if err != nil || w.State != domain.StateParked {
		t.Fatalf("failed switch activated target: %+v %v", w, err)
	}
	directory, err := os.MkdirTemp(t.TempDir(), "handoff-")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "directive")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	i := shellintegration.NewInvocation(file, "")
	defer i.Close()
	out, errs, err := executeNavigation(t, f, i, "switch", f.workspace.Key)
	if err != nil || out != "" || errs != "" {
		t.Fatalf("switch: %q %q %v", out, errs, err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	fields := bytes.Split(data, []byte{0})
	if len(fields) != 5 || string(fields[1]) != f.workspace.Path {
		t.Fatalf("handoff: %q", data)
	}
	w, err = f.service.Find(f.workspace.Key)
	if err != nil || w.State != domain.StateActive {
		t.Fatalf("target not activated: %+v %v", w, err)
	}
}

func TestPreviousIdentityAndLiteralDash(t *testing.T) {
	f := cliNavigationFixture(t)
	token, err := shellintegration.EncodeIdentity(workspaceIdentity(f.workspace))
	if err != nil {
		t.Fatal(err)
	}
	i := shellintegration.NewInvocation("", token)
	defer i.Close()
	out, _, err := executeNavigation(t, f, i, "switch", "-", "--print-path")
	if err != nil || out != f.workspace.Path+"\n" {
		t.Fatalf("previous: %q %v", out, err)
	}
	stale := workspaceIdentity(f.workspace)
	stale.ID = "00000000-0000-4000-8000-000000000001"
	token, err = shellintegration.EncodeIdentity(stale)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = executeNavigation(t, f, shellintegration.NewInvocation("", token), "switch", "-", "--print-path")
	if err == nil {
		t.Fatal("stale history identity navigated")
	}
	plan, err := f.service.PlanNew(app.NewRequest{Key: "-"})
	if err != nil {
		t.Fatal(err)
	}
	literal, err := f.service.Create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err = executeNavigation(t, f, nil, "switch", "--print-path", "--", "-")
	if err != nil || out != literal.Path+"\n" {
		t.Fatalf("literal dash: %q %v", out, err)
	}
	_, _, err = executeNavigation(t, f, nil, "switch", f.workspace.Key, "--shell", "--print-path")
	if err == nil {
		t.Fatal("incompatible modes accepted")
	}
}

func TestNavigationPublishesActualCwdIdentity(t *testing.T) {
	f := cliNavigationFixture(t)
	plan, err := f.service.PlanNew(app.NewRequest{Key: "origin"})
	if err != nil {
		t.Fatal(err)
	}
	origin, err := f.service.Create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(origin.Path)
	directory, err := os.MkdirTemp(t.TempDir(), "handoff-")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "directive")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	i := shellintegration.NewInvocation(file, "")
	defer i.Close()
	if _, _, err := executeNavigation(t, f, i, "switch", f.workspace.Key); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	fields := bytes.Split(data, []byte{0})
	if len(fields) != 5 || string(fields[2]) != "replace" {
		t.Fatalf("history record: %q", data)
	}
	previous, err := shellintegration.DecodeIdentity(string(fields[3]))
	if err != nil || previous.ID != origin.ID {
		t.Fatalf("origin history: %+v %v", previous, err)
	}
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeNavigation(t, f, i, "switch", origin.Key); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	fields = bytes.Split(data, []byte{0})
	if len(fields) != 5 || string(fields[2]) != "keep" {
		t.Fatalf("same-workspace navigation rotated history: %q", data)
	}
}

// A shell left inside a directory that was then removed (for example by
// cleanup) must still be able to switch away; it just records no history.
func TestSwitchFromDeletedDirectory(t *testing.T) {
	f := cliNavigationFixture(t)
	gone := filepath.Join(t.TempDir(), "removed")
	if err := os.Mkdir(gone, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(t.TempDir(), "handoff-")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "directive")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	i := shellintegration.NewInvocation(file, "")
	defer i.Close()
	if _, _, err := executeNavigation(t, f, i, "switch", f.workspace.Key); err != nil {
		t.Fatalf("switch from deleted cwd: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	fields := bytes.Split(data, []byte{0})
	if len(fields) != 5 || string(fields[1]) != f.workspace.Path || string(fields[2]) != "keep" {
		t.Fatalf("handoff from deleted cwd: %q", data)
	}
}

func TestShellInitDoesNotLoadConfigurationOrCreateState(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "absent")
	t.Setenv("XDG_STATE_HOME", state)
	path := filepath.Join(root, "broken.toml")
	if err := os.WriteFile(path, []byte("this is not valid toml"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &command{out: &out, errOut: &out}
	cmd := c.root()
	cmd.SetArgs([]string{"--config", path, "config", "shell", "init", "zsh"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "workdrive()") {
		t.Fatalf("missing wrapper: %s", out.String())
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("init created state: %v", err)
	}
}
