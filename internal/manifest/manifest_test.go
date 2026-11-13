package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usingcoding/workdrive/internal/domain"
)

func TestWriteReadAndAtomicReplacement(t *testing.T) {
	root := filepath.Join(t.TempDir(), "example")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	w := &domain.Workspace{SchemaVersion: 1, ID: "identity", Key: "example", Name: "Example", Path: root,
		Profile: "development", State: domain.StateCreating, CreatedAt: now, UpdatedAt: now,
		Resources: []domain.Resource{{ID: "repo", Type: "worktree", Driver: "worktrunk", Ownership: domain.OwnershipOwned,
			Source: filepath.Join(root, "source"), Path: filepath.Join(root, "repo"), Branch: "example"}}}
	for _, name := range []string{"Example", "Changed"} {
		w.Name = name
		if err := Write(w); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Changed" || len(got.Resources) != 1 || got.Resources[0].Branch != "example" || !got.CreatedAt.Equal(now) {
		t.Fatalf("unexpected round trip: %+v", got)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".workdrive"))
	if err != nil || len(entries) != 1 || entries[0].Name() != FileName {
		t.Fatalf("temporary manifest left behind: %v %v", entries, err)
	}
	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[[resources]]") {
		t.Fatalf("resource table missing from TOML: %s", data)
	}
	if err := os.WriteFile(Path(root), append(data, []byte("\nfuture_setting = 1\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root); err == nil {
		t.Fatal("unknown future field should be rejected rather than discarded")
	}
}

func TestWriteRejectsSymlinkedMetadata(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, path := range []string{root, outside} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, ".workdrive")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := Write(&domain.Workspace{SchemaVersion: 1, ID: "id", Key: "workspace", Name: "Name", Path: root,
		Profile: "default", State: domain.StateActive, CreatedAt: now, UpdatedAt: now}); err == nil {
		t.Fatal("write followed metadata symlink")
	}
	if _, err := Read(root); err == nil {
		t.Fatal("read followed metadata symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, FileName)); !os.IsNotExist(err) {
		t.Fatalf("outside path changed: %v", err)
	}
}
