package worktrunk

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLocalRefsAreOfflineAndIgnoreInheritedRepository(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo with spaces")
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	runGit("init", "-q", "-b", "main", repo)
	runGit("-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "initial")
	runGit("-C", repo, "branch", "feature/one")
	runGit("-C", repo, "tag", "v1")
	// A nonexistent remote and failing hooks cannot affect the local read.
	runGit("-C", repo, "remote", "add", "origin", "/must-not-be-contacted")
	marker := filepath.Join(root, "hook-ran")
	hooks := filepath.Join(root, "hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reference-transaction", "post-checkout", "pre-auto-gc"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	runGit("-C", repo, "config", "core.hooksPath", hooks)
	t.Setenv("GIT_DIR", filepath.Join(root, "wrong-git"))
	t.Setenv("GIT_WORK_TREE", filepath.Join(root, "wrong-tree"))
	refs, err := (GitRefs{}).LocalRefs(context.Background(), repo)
	if err != nil || !reflect.DeepEqual(refs, []string{"feature/one", "main", "v1"}) {
		t.Fatalf("refs: %v %v", refs, err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("hook executed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(repo, ".git", "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("fetched remote: %v", err)
	}
}

func TestLocalRefsDeadlineAndOutputBounds(t *testing.T) {
	root := t.TempDir()
	sleeper := filepath.Join(root, "slow-git")
	if err := os.WriteFile(sleeper, []byte("#!/bin/sh\nexec sleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := (GitRefs{Executable: sleeper}).LocalRefs(ctx, root); err == nil {
		t.Fatal("timeout returned success")
	}
	if time.Since(start) > time.Second {
		t.Fatal("local read exceeded timeout")
	}
	var output limitedRefOutput
	if _, err := output.Write([]byte(strings.Repeat("x", 256*1024+1))); err == nil {
		t.Fatal("unbounded subprocess output accepted")
	}
	if _, err := (GitRefs{}).LocalRefs(context.Background(), "relative"); err == nil {
		t.Fatal("relative source accepted")
	}
}
