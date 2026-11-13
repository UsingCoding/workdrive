package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/config"
)

func TestPrintPathOnlyAndNoninteractiveSafety(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	workspaceRoot := filepath.Join(root, "workspaces")
	configPath := filepath.Join(root, "config.toml")
	body := "schema_version = 1\n[workspace]\nroot = " + quoteTOML(workspaceRoot) + "\n"
	if err := os.WriteFile(configPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := app.Open(cfg, stateDir())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanNew(app.NewRequest{Key: "scratch-api"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Create(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Park(w.Key); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, string, error) {
		t.Helper()
		var out, errs bytes.Buffer
		c := &command{out: &out, errOut: &errs}
		cmd := c.root()
		cmd.SetArgs(append([]string{"--config", configPath}, args...))
		cmd.SetOut(&out)
		cmd.SetErr(&errs)
		err := cmd.Execute()
		if c.service != nil {
			_ = c.service.Close()
		}
		return out.String(), errs.String(), err
	}
	out, errs, err := run("switch", "scratch-api", "--print-path")
	if err != nil || out != w.Path+"\n" || errs != "" {
		t.Fatalf("stdout %q stderr %q err %v", out, errs, err)
	}
	out, _, err = run("list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 1 {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	_, _, err = run("new", "other")
	if err == nil || !strings.Contains(err.Error(), "wizard") {
		t.Fatalf("new without TTY: %v", err)
	}
	_, _, err = run("cleanup", w.Key)
	if err == nil || !strings.Contains(err.Error(), "noninteractive") {
		t.Fatalf("cleanup without TTY: %v", err)
	}
}

func quoteTOML(s string) string { data, _ := json.Marshal(s); return string(data) }
