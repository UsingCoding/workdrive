package shellintegration

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func privateFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "directive")
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIdentityRoundTrip(t *testing.T) {
	for _, key := range []string{"api", "-", "ápi_2.0"} {
		identity := Identity{ID: "stable-id", Key: key, Path: "/a space/'quote'/$literal;`not code`/line\n\n"}
		token, err := EncodeIdentity(identity)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(token, "\n\t \x00/+=") {
			t.Fatalf("token is not shell safe: %q", token)
		}
		got, err := DecodeIdentity(token)
		if err != nil || got != identity {
			t.Fatalf("round trip: %#v, %v", got, err)
		}
	}
	// Unix filenames are byte strings; do not silently replace invalid UTF-8.
	identity := Identity{ID: "id", Key: "key", Path: "/bytes\xff"}
	token, err := EncodeIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeIdentity(token)
	if err != nil || got != identity {
		t.Fatalf("byte round trip: %#v, %v", got, err)
	}
}

func TestIdentityInvalid(t *testing.T) {
	for _, identity := range []Identity{
		{}, {ID: "id", Key: "api", Path: "relative"},
		{ID: "id", Key: "../bad", Path: "/ok"},
		{ID: "id", Key: ".workdrive", Path: "/ok"},
		{ID: "", Key: "api", Path: "/ok"},
		{ID: "id\x00bad", Key: "api", Path: "/ok"},
		{ID: "id", Key: "api", Path: "/bad\x00path"},
		{ID: "id", Key: "api", Path: "/" + strings.Repeat("a", maxIdentityTokenSize)},
	} {
		if _, err := EncodeIdentity(identity); err == nil {
			t.Errorf("accepted %#v", identity)
		}
	}
	encodeRaw := func(s string) string { return "v1." + base64.RawURLEncoding.EncodeToString([]byte(s)) }
	valid, _ := EncodeIdentity(Identity{ID: "id", Key: "api", Path: "/ok"})
	for _, token := range []string{"", "v2." + valid[3:], "v1.!!!", "v1.A", valid + "=", valid + "\n", encodeRaw("id\x00api"), encodeRaw("id\x00api\x00/ok\x00extra"), encodeRaw("id\x00api\x00relative"), encodeRaw("id\x00..\x00/ok"), "v1." + strings.Repeat("a", 1<<20)} {
		if _, err := DecodeIdentity(token); err == nil {
			t.Errorf("accepted invalid token %.80q", token)
		}
	}
}

func TestCapture(t *testing.T) {
	t.Setenv(DirectiveEnv, "/private/directive")
	t.Setenv(PreviousEnv, "opaque previous token")
	invocation, err := Capture()
	if err != nil || invocation.directive != "/private/directive" || invocation.PreviousToken() != "opaque previous token" {
		t.Fatalf("capture: %#v, %v", invocation, err)
	}
	for _, key := range []string{DirectiveEnv, PreviousEnv} {
		if _, exists := os.LookupEnv(key); exists {
			t.Errorf("%s remains in child environment", key)
		}
	}
	var nilInvocation *Invocation
	if nilInvocation.PreviousToken() != "" || nilInvocation.Close() != nil {
		t.Fatal("nil invocation is not safe")
	}
}

func TestPreflightRejectsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	asDirectory := filepath.Join(dir, "as-directory")
	if err := os.Mkdir(asDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	regular := privateFile(t)
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	linked := privateFile(t)
	if err := os.Link(linked, filepath.Join(dir, "hardlink")); err != nil {
		t.Fatal(err)
	}
	nonempty := privateFile(t)
	if err := os.WriteFile(nonempty, []byte("do not truncate"), 0600); err != nil {
		t.Fatal(err)
	}
	publicDirectory := t.TempDir()
	if err := os.Chmod(publicDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	publicParent := filepath.Join(publicDirectory, "directive")
	if err := os.WriteFile(publicParent, nil, 0600); err != nil {
		t.Fatal(err)
	}
	parentSymlink := filepath.Join(dir, "parent-symlink")
	if err := os.Symlink(filepath.Dir(regular), parentSymlink); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"absent integration": "", "relative": "relative", "missing": filepath.Join(dir, "missing"), "directory": asDirectory, "symlink": symlink, "fifo": fifo, "hardlink": linked, "nonempty": nonempty, "public parent": publicParent, "symlink parent": filepath.Join(parentSymlink, filepath.Base(regular))} {
		t.Run(name, func(t *testing.T) {
			invocation := NewInvocation(path, "")
			defer invocation.Close()
			err := invocation.Preflight()
			var setup *SetupError
			if !errors.As(err, &setup) || !strings.Contains(err.Error(), "workdrive config shell init") {
				t.Fatalf("expected actionable setup error, got %v", err)
			}
			if path == "" && !errors.Is(err, ErrNotInitialized) {
				t.Errorf("missing ErrNotInitialized: %v", err)
			}
		})
	}
	got, err := os.ReadFile(nonempty)
	if err != nil || string(got) != "do not truncate" {
		t.Fatalf("preflight changed existing contents: %q, %v", got, err)
	}
	for _, mode := range []os.FileMode{0644, 0660, 0666, 0400, 0700, 0600 | os.ModeSetuid} {
		t.Run(mode.String(), func(t *testing.T) {
			path := privateFile(t)
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			invocation := NewInvocation(path, "")
			defer invocation.Close()
			if err := invocation.Preflight(); err == nil {
				t.Fatalf("accepted mode %v", mode)
			}
		})
	}
}

func TestPreflightHeldHandleAndClose(t *testing.T) {
	path := privateFile(t)
	invocation := NewInvocation(path, "history")
	if err := invocation.Preflight(); err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(invocation.file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("capability is not close-on-exec: flags %d, %v", flags, err)
	}
	original := path + ".original"
	if err := os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement remains untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := invocation.Preflight(); err != nil {
		t.Fatal(err)
	}
	if err := invocation.Publish("/literal\n\n", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(original)
	if string(got) != "1\x00/literal\n\n\x00keep\x00\x00" {
		t.Fatalf("record: %q", got)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "replacement remains untouched" {
		t.Fatalf("reopened pathname: %q", got)
	}
	if err := invocation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := invocation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := invocation.Publish("/after-close", nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("publish after Close: %v", err)
	}
}

func TestPreflightPreservesKernelParentTraversal(t *testing.T) {
	root := t.TempDir()
	logical := filepath.Join(root, "logical")
	physical := filepath.Join(root, "physical")
	child := filepath.Join(physical, "child")
	for _, dir := range []string{logical, physical, child} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(child, filepath.Join(logical, "link")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{logical, physical} {
		if err := os.WriteFile(filepath.Join(dir, "directive"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Do not filepath.Join this: cleaning would change its kernel meaning.
	invocation := NewInvocation(logical+"/link/../directive", "")
	defer invocation.Close()
	if err := invocation.Publish("/destination", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(physical, "directive"))
	if string(got) != "1\x00/destination\x00keep\x00\x00" {
		t.Fatalf("published to wrong directory: %q", got)
	}
	got, _ = os.ReadFile(filepath.Join(logical, "directive"))
	if len(got) != 0 {
		t.Fatalf("cleaned path was modified: %q", got)
	}
}

func TestPublishValidationAndReplace(t *testing.T) {
	path := privateFile(t)
	const original = "unmodified until publish"
	invocation := NewInvocation(path, "")
	defer invocation.Close()
	if err := invocation.Preflight(); err != nil {
		t.Fatal(err)
	}
	// A held invocation may be reused, but invalid publication must not alter
	// bytes already written through its capability.
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "relative", "/nul\x00injection"} {
		if err := invocation.Publish(path, nil); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	if err := invocation.Publish("/valid", &Identity{}); err == nil {
		t.Fatal("accepted invalid history")
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("validation/preflight truncated file: %q", got)
	}
	previous := Identity{ID: "original-id", Key: "-", Path: "/origin"}
	token, _ := EncodeIdentity(previous)
	if err := invocation.Publish("/new path/'; $()\n\n", &previous); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	want := []byte("1\x00/new path/'; $()\n\n\x00replace\x00" + token + "\x00")
	if !bytes.Equal(got, want) {
		t.Fatalf("record: got %q want %q", got, want)
	}
	if err := invocation.Publish("/short", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "1\x00/short\x00keep\x00\x00" {
		t.Fatalf("trailing old data: %q", got)
	}
}

func TestInitUnsupported(t *testing.T) {
	for _, shell := range []string{"fish", "", "/bin/bash", "BASH", "bash; echo injected"} {
		if script, err := Init(shell); err == nil || script != "" {
			t.Fatalf("Init(%q): %q, %v", shell, script, err)
		}
	}
}
