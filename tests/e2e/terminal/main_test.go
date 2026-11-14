package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestHeadlessCapturesStreamsAndExit(t *testing.T) {
	got := run(request{Command: "/bin/sh", Args: []string{"-c", "printf output; printf error >&2; exit 7"}})
	if got.ExitCode != 7 || got.Stdout != "output" || got.Stderr != "error" || got.Error != "" {
		t.Fatalf("headless result: %+v", got)
	}
}

func TestInteractiveRequiresAndDrivesTerminal(t *testing.T) {
	got := run(request{Command: "/bin/sh", Args: []string{"-c", "test -t 0 && test -t 1 || exit 8; printf 'Ready'; read -r answer; printf 'Done:%s' \"$answer\""}, Steps: []step{{Wait: "Ready", Send: "yes\n"}, {Wait: "Done:yes", Send: ""}}})
	if got.ExitCode != 0 || !strings.Contains(got.Output, "Done:yes") || got.Error != "" {
		t.Fatalf("PTY result: %+v", got)
	}
}

func TestWriteAndRemove(t *testing.T) {
	path := t.TempDir() + "/nested/file"
	if got := run(request{Write: path, Data: base64.StdEncoding.EncodeToString([]byte("fixture"))}); got.ExitCode != 0 {
		t.Fatalf("write: %+v", got)
	}
	if got := run(request{Command: "/bin/cat", Args: []string{path}}); got.Stdout != "fixture" {
		t.Fatalf("read: %+v", got)
	}
	if got := run(request{Remove: path}); got.ExitCode != 0 {
		t.Fatalf("remove: %+v", got)
	}
}
