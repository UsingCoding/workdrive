// Package discovery finds existing local Git sources without following
// directory symlinks, cloning, or scanning outside configured bounded roots.
package discovery

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	Incomplete   bool         `json:"-"` // Hint budget/deadline reached; names may be ambiguous.
}

// Scan scans at most MaxDepth directory levels beneath each source and
// automatically excludes every configured workspace root. A failed root,
// unreadable directory or invalid .git entry emits a warning without hiding
// repositories found elsewhere. No symlinked directory is descended into.
func Scan(ctx context.Context, repositories config.RepositoryConfig, workspaceRoots ...string) Result {
	return scan(ctx, repositories, HintLimits{}, workspaceRoots...)
}

// HintLimits bounds metadata-only discovery. Zero/oversized fields use the
// hard ceilings, so callers cannot accidentally turn completion into a full scan.
type HintLimits struct {
	Deadline time.Duration
	Entries  int
	Results  int
}

const (
	MaxHintDuration = 150 * time.Millisecond
	MaxHintEntries  = 4096
	MaxHintResults  = 100
)

// Hints shares Scan's traversal and exclusions, but .git presence is only a
// hint. It never executes Git, persists a cache, or follows directory symlinks.
func Hints(ctx context.Context, repositories config.RepositoryConfig, limits HintLimits, workspaceRoots ...string) Result {
	if limits.Deadline <= 0 || limits.Deadline > MaxHintDuration {
		limits.Deadline = MaxHintDuration
	}
	if limits.Entries <= 0 || limits.Entries > MaxHintEntries {
		limits.Entries = MaxHintEntries
	}
	if limits.Results <= 0 || limits.Results > MaxHintResults {
		limits.Results = MaxHintResults
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Deadline)
	defer cancel()
	return scan(ctx, repositories, limits, workspaceRoots...)
}

func scan(ctx context.Context, repositories config.RepositoryConfig, limits HintLimits, workspaceRoots ...string) Result {
	hints := limits.Entries > 0
	visited := 0
	result := Result{Repositories: []Repository{}, Warnings: []Warning{}}
	stopped := func() bool {
		return ctx.Err() != nil || (hints && (visited >= limits.Entries || len(result.Repositories) >= limits.Results))
	}
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
		if stopped() {
			break
		}
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
			if stopped() {
				return
			}
			if !allowedPath(source, root, path, excludedRoots) {
				return
			}
			if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
				actual := path
				var err error
				if !hints {
					actual, err = gitTopLevel(ctx, path)
				}
				if err != nil {
					result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: path, Message: err.Error()})
				} else if actual == path && !seen[actual] {
					seen[actual] = true
					result.Repositories = append(result.Repositories, Repository{Name: filepath.Base(actual), Path: actual, Source: source.Name})
				}
			} else if !os.IsNotExist(err) {
				result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: path, Message: fmt.Sprintf("inspect .git: %v", err)})
			}
			if depth >= source.MaxDepth || stopped() {
				return
			}
			var entries []os.DirEntry
			var err error
			if hints {
				// Unlike os.ReadDir, this never allocates an unbounded directory
				// listing before applying the completion budget.
				var dir *os.File
				dir, err = os.Open(path)
				if err == nil {
					entries, err = dir.ReadDir(limits.Entries - visited)
					dir.Close()
					if err == io.EOF {
						err = nil
					}
					sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
				}
			} else {
				entries, err = os.ReadDir(path)
			}
			if err != nil {
				result.Warnings = append(result.Warnings, Warning{Root: source.Name, Path: path, Message: fmt.Sprintf("read directory: %v", err)})
				return
			}
			for _, entry := range entries {
				if stopped() {
					break
				}
				visited++
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
	result.Incomplete = hints && (stopped() || len(result.Warnings) > 0)
	if err := ctx.Err(); err != nil {
		result.Warnings = append(result.Warnings, Warning{Message: err.Error()})
	}
	return result
}

// AllowsHint checks only configured bounds and exclusions, allowing recorded
// source paths to be merged with fresh hints without bypassing discovery scope.
func AllowsHint(repositories config.RepositoryConfig, path string, workspaceRoots ...string) bool {
	var excluded []string
	for _, root := range workspaceRoots {
		if canonical, err := config.CanonicalPath(root); err == nil {
			excluded = append(excluded, canonical)
		}
	}
	for _, source := range repositories.Sources {
		root, err := config.CanonicalPath(source.Path)
		if err == nil && allowedPath(source, root, path, excluded) {
			return true
		}
	}
	return false
}

func allowedPath(source config.Source, root, path string, excluded []string) bool {
	if !within(root, path) {
		return false
	}
	for _, workspace := range excluded {
		if within(workspace, path) {
			return false
		}
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > source.MaxDepth {
		return false
	}
	for i := 1; i <= len(parts); i++ {
		for _, pattern := range source.Exclude {
			if match(pattern, strings.Join(parts[:i], "/")) {
				return false
			}
		}
	}
	return true
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
