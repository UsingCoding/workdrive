package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usingcoding/workdrive/internal/config"
)

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestFakeWorktrunkExecutableEndToEnd(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	cfg.Repositories.Sources = nil
	fakePath := filepath.Join(root, "fake-wt")
	fake := `#!/bin/sh
set -eu
source=''
dest=''
branch=''
create=0
mode=''
for arg do
  case "$arg" in
    switch|remove) mode="$arg" ;;
    --help) printf 'Usage: wt switch --create BRANCH\n'; exit 0 ;;
  esac
done
while [ "$#" -gt 0 ]; do
  case "$1" in
    -C) source="$2"; shift 2 ;;
    --config-set)
      dest="${2#worktree-path=}"
      dest="${dest#\"}"
      dest="${dest%\"}"
      shift 2 ;;
    --create) create=1; shift ;;
    --base|--format) shift 2 ;;
    --no-cd|--foreground|--no-delete-branch) shift ;;
    switch|remove) mode="$1"; shift ;;
    *) branch="$1"; shift ;;
  esac
done
if [ "$mode" = switch ]; then
  if [ "$create" -eq 1 ]; then git -C "$source" worktree add -b "$branch" "$dest" >/dev/null; else git -C "$source" worktree add "$dest" "$branch" >/dev/null; fi
else
  git -C "$source" worktree remove "$branch" >/dev/null
fi
printf '{}\n'
`
	if err := os.WriteFile(fakePath, []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Worktree.Executable = fakePath
	s, err := Open(cfg, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var choices []RepositoryChoice
	for _, name := range []string{"auth", "users", "gateway"} {
		dir := filepath.Join(root, "sources", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		gitCommand(t, dir, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(dir, "README"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		gitCommand(t, dir, "add", "README")
		gitCommand(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "initial")
		choices = append(choices, RepositoryChoice{Source: dir, Name: name})
	}
	ctx := context.Background()
	plan, err := s.PlanNew(NewRequest{Key: "scratch-api", Repos: choices[:2]})
	if err != nil || !plan.Safe() {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	w, err := s.Create(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range w.Resources {
		if got := gitCommand(t, r.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "scratch-api" {
			t.Errorf("branch: %s", got)
		}
	}
	add, err := s.PlanAdd(ctx, w.Key, choices[2:])
	if err != nil || !add.Safe() {
		t.Fatalf("add plan: %+v %v", add, err)
	}
	w, err = s.Add(ctx, add)
	if err != nil || len(w.Resources) != 3 {
		t.Fatalf("add: %v %+v", err, w)
	}
	if _, err := s.Park(w.Key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Switch(w.Key); err != nil {
		t.Fatal(err)
	}
	for _, r := range w.Resources {
		if _, err := os.Stat(filepath.Join(r.Path, "README")); err != nil {
			t.Fatal(err)
		}
	}
	cleanup, err := s.PlanCleanup(ctx, w.Key)
	if err != nil || !cleanup.Safe() {
		t.Fatalf("cleanup plan: %+v %v", cleanup, err)
	}
	if err := s.Cleanup(ctx, cleanup); err != nil {
		t.Fatal(err)
	}
	for _, r := range w.Resources {
		if _, err := os.Stat(r.Source); err != nil {
			t.Fatalf("source removed: %v", err)
		}
		if got := gitCommand(t, r.Source, "branch", "--list", "scratch-api"); !strings.Contains(got, "scratch-api") {
			t.Errorf("branch removed: %s", r.Source)
		}
	}
}
