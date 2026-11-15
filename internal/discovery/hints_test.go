package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/usingcoding/workdrive/internal/config"
)

func TestHintsMetadataBoundsAndSharedExclusions(t *testing.T) {
	base, root := t.TempDir(), ""
	root = filepath.Join(base, "sources")
	fakeRepo := func(rel string) string {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	one := fakeRepo("api")
	fakeRepo("nested/worker")
	fakeRepo("vendor/excluded")
	fakeRepo("workspaces/task/repo")
	fakeRepo("other-profile/task/repo")
	fakeRepo("deep/one/two/too-deep")
	if err := os.MkdirAll(filepath.Join(root, "empty-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(one, filepath.Join(root, "symlink")); err != nil {
		t.Fatal(err)
	}
	repositories := config.RepositoryConfig{Sources: []config.Source{{Name: "local", Path: root, MaxDepth: 3, Exclude: []string{"vendor"}}}}
	// No executable can run. Empty .git directories still qualify as metadata
	// hints, proving this path does not perform Git validation per candidate.
	t.Setenv("PATH", t.TempDir())
	workspaceRoots := []string{filepath.Join(root, "workspaces"), filepath.Join(root, "other-profile")}
	found := Hints(context.Background(), repositories, HintLimits{}, workspaceRoots...)
	if found.Incomplete || len(found.Repositories) != 2 {
		t.Fatalf("unexpected hints: %+v", found)
	}
	for _, r := range found.Repositories {
		if !AllowsHint(repositories, r.Path, workspaceRoots...) {
			t.Fatalf("shared bounds rejected scanned hint: %s", r.Path)
		}
	}
	for _, rel := range []string{"vendor/excluded", "workspaces/task/repo", "other-profile/task/repo", "deep/one/two/too-deep"} {
		if AllowsHint(repositories, filepath.Join(root, rel), workspaceRoots...) {
			t.Fatalf("recorded hint bypassed exclusion: %s", rel)
		}
	}
	limited := Hints(context.Background(), repositories, HintLimits{Results: 1}, workspaceRoots...)
	if !limited.Incomplete || len(limited.Repositories) != 1 {
		t.Fatalf("result budget not enforced: %+v", limited)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Hints(ctx, repositories, HintLimits{}); len(got.Repositories) != 0 || !got.Incomplete {
		t.Fatalf("cancelled scan: %+v", got)
	}
	if got := Hints(context.Background(), repositories, HintLimits{Deadline: time.Nanosecond}); len(got.Repositories) != 0 || !got.Incomplete {
		t.Fatalf("expired deadline: %+v", got)
	}
}

func TestHintsVisitedEntryBudgetIncludesNonDirectories(t *testing.T) {
	root := t.TempDir()
	for i := range 30 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("entry-%02d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	repositories := config.RepositoryConfig{Sources: []config.Source{{Name: "root", Path: root, MaxDepth: 4}}}
	result := Hints(context.Background(), repositories, HintLimits{Entries: 3})
	if !result.Incomplete || len(result.Repositories) != 0 {
		t.Fatalf("entry budget ignored files: %+v", result)
	}
}
