package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/usingcoding/workdrive/internal/config"
)

func TestRepositoryDiscoveryCacheAndManualRefresh(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspace.Root = filepath.Join(root, "workspaces")
	cfg.Repositories.Sources = []config.Source{{Name: "projects", Path: filepath.Join(root, "projects"), MaxDepth: 2}}
	if err := os.MkdirAll(cfg.Repositories.Sources[0].Path, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(cfg, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if found := s.Discover(ctx, ""); len(found.Repositories) != 0 {
		t.Fatalf("unexpected repositories: %+v", found)
	}
	repo := filepath.Join(root, "projects", "service")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "init", "-b", "main")
	if found := s.Discover(ctx, ""); len(found.Repositories) != 0 {
		t.Fatal("discovery cache was not retained")
	}
	if found := s.RefreshDiscovery(ctx, ""); len(found.Repositories) != 1 {
		t.Fatalf("refresh did not find repository: %+v", found)
	}
}
