package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadOnlyMissingDoesNotInitialize(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "missing", "registry.db")
	r, err := OpenReadOnly(path)
	if err != nil || r != nil {
		t.Fatalf("missing registry: %v %v", r, err)
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only open created state: %v %v", entries, err)
	}
	if _, err := OpenReadOnly("relative.db"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestReadOnlyExistingRejectsWritesAndRetainsLocking(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "registry space.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	root := filepath.Join(base, "workspaces")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	w := testWorkspace(t, root, "task", "id-task")
	if err := writer.Upsert(w); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if list, err := r.List(); err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	if err := r.Delete(w.Key); err == nil {
		t.Fatal("read-only index accepted a write")
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("query changed database: %v", err)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("created sidecar %s: %v", suffix, err)
		}
	}
	w.Name = "updated while reader open"
	if err := writer.Upsert(w); err != nil {
		t.Fatal(err)
	}
	if entry, err := r.Get(w.Key); err != nil || entry.Name != w.Name {
		t.Fatalf("reader ignored concurrent commit: %+v %v", entry, err)
	}
}

func TestReadOnlyEscapesURIPath(t *testing.T) {
	base := t.TempDir()
	plain := filepath.Join(base, "plain.db")
	writer, err := Open(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "registry space?#.db")
	if err := os.Rename(plain, path); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if list, err := r.List(); err != nil || len(list) != 0 {
		t.Fatalf("escaped URI path: %v %v", list, err)
	}
}

func TestReadOnlyDoesNotCreateSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.List(); err == nil {
		t.Fatal("empty database unexpectedly had schema")
	}
	if st, err := os.Stat(path); err != nil || st.Size() != 0 {
		t.Fatalf("created database schema: %v %v", st, err)
	}
}
