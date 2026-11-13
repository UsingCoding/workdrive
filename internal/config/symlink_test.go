package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredSymlinkRootNormalizesOnlyRootPrefix(t *testing.T) {
	base, err := CanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "real")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	path, err := ValidateDestination(alias, filepath.Join(alias, "task"))
	if err != nil || path != filepath.Join(root, "task") {
		t.Fatalf("configured symlink root: %s %v", path, err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateDestination(alias, filepath.Join(alias, "escape", "task")); err == nil {
		t.Fatal("descendant symlink escaped configured root")
	}
}
