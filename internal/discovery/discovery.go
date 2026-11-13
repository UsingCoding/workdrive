// Package discovery finds existing local Git sources without following
// directory symlinks, cloning, or scanning outside configured bounded roots.
package discovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/usingcoding/workdrive/internal/config"
)

type Repository struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Path        string `json:"path"`
	Source      string `json:"source"`
}

type Warning struct {
	Root    string `json:"root"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Result struct {
	Repositories []Repository `json:"repositories"`
	Warnings     []Warning    `json:"warnings"`
}

// Scan scans at most MaxDepth directory levels beneath each source and
// automatically excludes every configured workspace root. A failed root,
// unreadable directory or invalid .git entry emits a warning without hiding
// repositories found elsewhere. No symlinked directory is descended into.
func Scan(ctx context.Context, repositories config.RepositoryConfig, workspaceRoots ...string) Result {
	result := Result{Repositories: []Repository{}, Warnings: []Warning{}}
	seen := make(map[string]bool)
	var excludedRoots []string
	for _, root := range workspaceRoots {
		resolved, err := config.CanonicalPath(root)
		if err != nil {
			result.Warnings = append(result.Warnings, Warning{Root: root, Path: root, Message: err.Error()})
			continue
		}
		excludedRoots = append(excludedRoots, resolved)
	}
	for _, source := range repositories.Sources {
		root, err := config.CanonicalPath(source.Path)
		if err != nil {
			result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: source.Path, Message: err.Error()})
			continue
		}
		if source.MaxDepth < 0 || source.MaxDepth > 16 {
			result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: root, Message: "max_depth must be between 0 and 16"})
			continue
		}
		for _, pattern := range source.Exclude {
			if err := config.ValidateExclude(pattern); err != nil {
				result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: root, Message: err.Error()})
			}
		}
		var visit func(path string, depth int)
		visit = func(path string, depth int) {
			if ctx.Err() != nil {
				return
			}
			for _, excluded := range excludedRoots {
				if within(excluded, path) {
					return
				}
			}
			if depth > 0 {
				rel, _ := filepath.Rel(root, path)
				for _, pattern := range source.Exclude {
					if match(pattern, filepath.ToSlash(rel)) {
						return
					}
				}
			}
			if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
				actual, err := gitTopLevel(ctx, path)
				if err != nil {
					result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: path, Message: err.Error()})
				} else if actual == path && !seen[actual] {
					seen[actual] = true
					result.Repositories = append(result.Repositories, Repository{Name: filepath.Base(actual), Path: actual, Source: source.Name})
				}
			} else if !os.IsNotExist(err) {
				result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: path, Message: fmt.Sprintf("inspect .git: %v", err)})
			}
			if depth >= source.MaxDepth {
				return
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: path, Message: fmt.Sprintf("read directory: %v", err)})
				return
			}
			for _, entry := range entries {
				if entry.Name() == ".git" || !entry.IsDir() {
					continue
				}
				visit(filepath.Join(path, entry.Name()), depth+1)
			}
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: root, Message: fmt.Sprintf("repository root unavailable: %v", err)})
			continue
		}
		visit(root, 0)
	}
	sort.Slice(result.Repositories, func(i, j int) bool { return result.Repositories[i].Path < result.Repositories[j].Path })
	names := make(map[string]int)
	for _, repo := range result.Repositories {
		names[repo.Name]++
	}
	for i := range result.Repositories {
		repo := &result.Repositories[i]
		repo.DisplayName = repo.Name
		if names[repo.Name] > 1 {
			repo.DisplayName = fmt.Sprintf("%s [%s: %s]", repo.Name, repo.Source, repo.Path)
		}
	}
	if err := ctx.Err(); err != nil {
		result.Warnings = append(result.Warnings, Warning{Message: err.Error()})
	}
	return result
}

func within(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func gitTopLevel(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
	// Unset inherited repository overrides; an empty GIT_DIR is still set and
	// makes Git fail to find even a valid .git directory.
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_DIR=") || strings.HasPrefix(entry, "GIT_WORK_TREE=") || strings.HasPrefix(entry, "GIT_COMMON_DIR=") || strings.HasPrefix(entry, "GIT_CEILING_DIRECTORIES=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("invalid Git repository: %w", err)
	}
	return config.CanonicalPath(strings.TrimSpace(string(out)))
}

// Resolve accepts an exact absolute source path or a unique repo name or
// disambiguated display name. An ambiguous bare name always returns an error;
// a caller may instead present its candidates in an interactive picker.
func Resolve(repos []Repository, nameOrPath string) (Repository, error) {
	if filepath.IsAbs(nameOrPath) {
		path, err := config.CanonicalPath(nameOrPath)
		if err != nil {
			return Repository{}, err
		}
		for _, repo := range repos {
			if repo.Path == path {
				return repo, nil
			}
		}
		return Repository{}, fmt.Errorf("repository %q not among discovered sources", nameOrPath)
	}
	var candidates []Repository
	for _, repo := range repos {
		if repo.Name == nameOrPath || repo.DisplayName == nameOrPath {
			candidates = append(candidates, repo)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) > 1 {
		paths := make([]string, 0, len(candidates))
		for _, repo := range candidates {
			paths = append(paths, repo.Path)
		}
		return Repository{}, fmt.Errorf("ambiguous repository %q; choose an absolute path: %s", nameOrPath, strings.Join(paths, ", "))
	}
	return Repository{}, fmt.Errorf("repository %q not found among configured sources", nameOrPath)
}

// match implements segment-based glob matching, where ** matches zero or
// more path segments and * never crosses a slash.
func match(pattern, rel string) bool {
	parts := strings.Split(pattern, "/")
	components := strings.Split(rel, "/")
	var walk func(int, int) bool
	walk = func(i, j int) bool {
		if i == len(parts) {
			return j == len(components)
		}
		if parts[i] == "**" {
			return walk(i+1, j) || (j < len(components) && walk(i, j+1))
		}
		if j >= len(components) {
			return false
		}
		ok, err := filepath.Match(parts[i], components[j])
		return err == nil && ok && walk(i+1, j+1)
	}
	return walk(0, 0)
}
