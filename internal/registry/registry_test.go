package registry

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/manifest"
)

func TestRebuildAndConflicts(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspaces")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	registry, err := Open(filepath.Join(base, "state", "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	one := testWorkspace(t, root, "Alpha", "id-1")
	if err := registry.Upsert(one); err != nil {
		t.Fatal(err)
	}
	if err := registry.Upsert(one); err != nil {
		t.Fatalf("idempotent update: %v", err)
	}
	two := testWorkspace(t, root, "alpha", "id-2")
	if err := registry.Upsert(two); err != nil {
		t.Fatalf("distinct case-sensitive keys: %v", err)
	}
	collision := *two
	collision.Key = one.Key
	if err := registry.Upsert(&collision); err == nil {
		t.Fatal("different workspace replaced existing key")
	}
	missing := testWorkspace(t, root, "missing", "id-3")
	if err := registry.Upsert(missing); err != nil {
		t.Fatal(err)
	}
	for _, w := range []*domain.Workspace{one, two} {
		if err := manifest.Write(w); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Rebuild([]string{root, root}); err != nil {
		t.Fatal(err)
	}
	entries, err := registry.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != "Alpha" || entries[1].Key != "alpha" || entries[0].WorktreeCount != 1 {
		t.Fatalf("unexpected rebuilt entries: %+v", entries)
	}
	if entry, err := registry.Get("missing"); err != nil || entry != nil {
		t.Fatalf("stale entry survived rebuild: %+v %v", entry, err)
	}
	if err := os.WriteFile(manifest.Path(two.Path), []byte("invalid = ["), 0600); err != nil {
		t.Fatal(err)
	}
	if err := registry.Rebuild([]string{root}); err == nil {
		t.Fatal("corrupted manifest should block rebuild")
	}
	entries, err = registry.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("failed rebuild changed old index: %v, %+v", err, entries)
	}
	if err := registry.Delete(one.Key); err != nil {
		t.Fatal(err)
	}
	if entry, err := registry.Get(one.Key); err != nil || entry != nil {
		t.Fatalf("delete did not remove entry: %+v %v", entry, err)
	}
}

func testWorkspace(t *testing.T, root, key, id string) *domain.Workspace {
	t.Helper()
	path := filepath.Join(root, id)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return &domain.Workspace{SchemaVersion: 1, ID: id, Key: key, Name: key, Path: path, Profile: "default",
		State: domain.StateActive, CreatedAt: now, UpdatedAt: now,
		Resources: []domain.Resource{{ID: "repo", Type: "worktree", Driver: "worktrunk", Ownership: domain.OwnershipOwned}}}
}
