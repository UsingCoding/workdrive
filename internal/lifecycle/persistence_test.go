package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAdvisoryLockRetainsStableInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "workspace.lock")
	one, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second process acquired lock: %v", err)
	}
	if err := one.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := one.Unlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file removed on release: %v", err)
	}
	two, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Unlock()
	if err := os.Symlink(path, filepath.Join(filepath.Dir(path), "alias.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(filepath.Join(filepath.Dir(path), "alias.lock")); err == nil {
		t.Fatal("followed symlinked lock file")
	}
}

func TestJournalResumeAndOrderedCompletion(t *testing.T) {
	j, err := NewJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	op, err := j.Plan("workspace-id", "create", []Step{{ID: "manifest", Description: "write manifest"}, {ID: "repo", Description: "create worktree"}})
	if err != nil {
		t.Fatal(err)
	}
	if op.Done() {
		t.Fatal("unfinished journal is marked complete")
	}
	if _, err := j.Complete(op.ID, "repo"); err == nil {
		t.Fatal("allowed completion out of order")
	}
	if _, err := j.RecordError(op.ID, errors.New("interrupted")); err != nil {
		t.Fatal(err)
	}
	resumed, err := j.Load(op.ID)
	if err != nil || resumed.Error != "interrupted" || len(resumed.Steps) != 2 {
		t.Fatalf("cannot load failure journal: %+v %v", resumed, err)
	}
	if _, err := j.Complete(op.ID, "manifest"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Complete(op.ID, "manifest"); err != nil {
		t.Fatalf("completion not idempotent: %v", err)
	}
	if _, err := j.Complete(op.ID, "repo"); err != nil {
		t.Fatal(err)
	}
	resumed, err = j.Load(op.ID)
	if err != nil || !resumed.Done() || resumed.Error != "" {
		t.Fatalf("operation not durably completed: %+v %v", resumed, err)
	}
	if _, err := j.Load("../outside"); err == nil {
		t.Fatal("journal path traversal accepted")
	}
}
