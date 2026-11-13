// Package worktrunk provides a safety-checked adapter for the wt executable.
// Callers must separately establish workspace ownership and check for unrelated
// files in the workspace before deleting its root. Inspection is not a lock:
// callers must serialize lifecycle operations and review the plan before Remove.
package worktrunk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Request identifies a worktree. Source is an existing source checkout, Path
// is the absolute intended worktree path, and Branch is a local branch name.
// Base is used only for creation; "default" and an empty Base mean the default
// branch. Paths are checked against Git's registration, not just the filesystem.
type Request struct {
	Source string
	Path   string
	Branch string
	Base   string
}

// Inspection reports actual Git state. A nonempty Blockers list means this
// worktree must not be removed. The caller, not the adapter, records ownership.
type Inspection struct {
	Present  bool
	Clean    bool
	Blockers []string
}

// Adapter is the interface used by workspace lifecycle operations.
type Adapter interface {
	Create(context.Context, Request) error
	Inspect(context.Context, Request) (Inspection, error)
	Remove(context.Context, Request) error
}

// Driver executes wt using argument vectors; Executable defaults to "wt".
type Driver struct{ Executable string }

func New(executable string) *Driver { return &Driver{Executable: executable} }

func (d *Driver) executable() string {
	if d.Executable == "" {
		return "wt"
	}
	return d.Executable
}

func run(ctx context.Context, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w: %s", executable, args, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func git(ctx context.Context, source string, args ...string) ([]byte, error) {
	return run(ctx, "git", append([]string{"-C", source}, args...)...)
}

// canonical requires an absolute, symlink-free destination. In particular,
// neither an existing worktree nor an ancestor may redirect deletion elsewhere.
func canonical(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("worktree path must be clean and absolute: %q", path)
	}
	parent := path
	for {
		_, err := os.Lstat(parent)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return "", err
			}
			if resolved != parent {
				return "", fmt.Errorf("worktree path traverses a symlink: %q", parent)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if next := filepath.Dir(parent); next == parent {
			return "", fmt.Errorf("cannot resolve worktree path: %q", path)
		} else {
			parent = next
		}
	}
	return path, nil
}

func (r Request) validate() (string, string, error) {
	if r.Source == "" || r.Path == "" || r.Branch == "" {
		return "", "", errors.New("source, path and branch are required")
	}
	path, err := canonical(r.Path)
	if err != nil {
		return "", "", err
	}
	if strings.HasPrefix(r.Branch, "-") || r.Branch == "@" || r.Branch == "HEAD" {
		return "", "", fmt.Errorf("invalid local branch %q", r.Branch)
	}
	// Git validates the ref syntax; reject non-local shortcuts before passing to wt.
	if _, err := run(context.Background(), "git", "check-ref-format", "refs/heads/"+r.Branch); err != nil {
		return "", "", fmt.Errorf("invalid local branch %q: %w", r.Branch, err)
	}
	source, err := filepath.Abs(r.Source)
	if err != nil {
		return "", "", err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", "", fmt.Errorf("source repository unavailable: %w", err)
	}
	return source, path, nil
}

type registered struct {
	path   string
	branch string
	bare   bool
	pruned bool
}

func worktrees(ctx context.Context, source string) ([]registered, error) {
	out, err := git(ctx, source, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var entries []registered
	for _, record := range strings.Split(string(out), "\x00\x00") {
		if record == "" {
			continue
		}
		var item registered
		for _, field := range strings.Split(strings.Trim(record, "\x00"), "\x00") {
			switch {
			case strings.HasPrefix(field, "worktree "):
				item.path = strings.TrimPrefix(field, "worktree ")
			case strings.HasPrefix(field, "branch refs/heads/"):
				item.branch = strings.TrimPrefix(field, "branch refs/heads/")
			case field == "bare":
				item.bare = true
			case strings.HasPrefix(field, "prunable "):
				item.pruned = true
			}
		}
		if item.path == "" {
			return nil, errors.New("invalid git worktree registration")
		}
		entries = append(entries, item)
	}
	return entries, nil
}

func commonDir(ctx context.Context, path string) (string, error) {
	out, err := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(out)))
}

func (d *Driver) inspect(ctx context.Context, r Request) (Inspection, string, string, error) {
	source, path, err := r.validate()
	if err != nil {
		return Inspection{}, "", "", err
	}
	base, err := commonDir(ctx, source)
	if err != nil {
		return Inspection{}, "", "", fmt.Errorf("source repository unavailable: %w", err)
	}
	items, err := worktrees(ctx, source)
	if err != nil {
		return Inspection{}, "", "", err
	}
	state := Inspection{}
	for _, item := range items {
		if item.branch == r.Branch && item.path != path {
			state.Blockers = append(state.Blockers, fmt.Sprintf("branch %q is checked out at %q", r.Branch, item.path))
		}
		if item.path == path {
			state.Present = true
			if item.branch != r.Branch || item.bare || item.pruned {
				state.Blockers = append(state.Blockers, fmt.Sprintf("worktree at %q has another branch or stale registration", path))
			}
		}
	}
	if !state.Present {
		state.Blockers = append(state.Blockers, fmt.Sprintf("worktree at %q is not registered for the source", path))
		return state, source, path, nil
	}
	actual, err := commonDir(ctx, path)
	if err != nil || actual != base {
		state.Blockers = append(state.Blockers, fmt.Sprintf("worktree at %q does not belong to the source repository", path))
		return state, source, path, nil
	}
	// -z avoids interpreting filenames (including newlines) as status records.
	// --ignored=matching includes ignored files otherwise hidden by porcelain.
	status, err := git(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if err != nil {
		return state, source, path, err
	}
	state.Clean = len(status) == 0
	if !state.Clean {
		state.Blockers = append(state.Blockers, "worktree contains staged, unstaged, untracked or ignored files")
	}
	return state, source, path, nil
}

// Inspect always checks the source repository, registered worktree identity,
// local branch and full porcelain status (including ignored files).
func (d *Driver) Inspect(ctx context.Context, r Request) (Inspection, error) {
	state, _, _, err := d.inspect(ctx, r)
	return state, err
}

// Create rejects occupied paths and branches already checked out anywhere.
// A registered worktree is not implicitly adopted, even when it matches.
// The expected path is verified after wt exits; a wrong path is an error and
// is never silently accepted as an owned worktree.
func (d *Driver) Create(ctx context.Context, r Request) error {
	state, source, path, err := d.inspect(ctx, r)
	if err != nil {
		return err
	}
	if state.Present || len(state.Blockers) != 1 { // one expected "not registered" blocker
		return fmt.Errorf("cannot create worktree: %s", strings.Join(state.Blockers, "; "))
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("cannot create worktree: destination %q already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err = git(ctx, source, "show-ref", "--verify", "--quiet", "refs/heads/"+r.Branch)
	branchExists := err == nil
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return err
		}
	}
	// Probe the executable at runtime: --path is not supported by wt v0.68.0.
	// --config-set is global and takes a TOML string value, not an unquoted path.
	help, err := run(ctx, d.executable(), "switch", "--help")
	if err != nil {
		return err
	}
	args := []string{"-C", source}
	if !strings.Contains(string(help), "--path <") {
		quoted, err := json.Marshal(path) // JSON strings are also TOML basic strings.
		if err != nil {
			return err
		}
		args = append(args, "--config-set", "worktree-path="+string(quoted))
	}
	args = append(args, "switch", "--no-cd", "--format", "json")
	if !branchExists {
		args = append(args, "--create")
		if r.Base != "" && r.Base != "default" {
			args = append(args, "--base", r.Base)
		}
	}
	if strings.Contains(string(help), "--path <") {
		args = append(args, "--path", path)
	}
	args = append(args, r.Branch)
	if _, err := run(ctx, d.executable(), args...); err != nil {
		return err
	}
	state, _, _, err = d.inspect(ctx, r)
	if err != nil {
		return err
	}
	if !state.Present || len(state.Blockers) != 0 {
		return fmt.Errorf("wt switch did not create the expected worktree at %q: %s", path, strings.Join(state.Blockers, "; "))
	}
	return nil
}

// Remove reruns inspection immediately before invoking foreground wt removal.
// No force flags are used; branches are always preserved. A failed hook or
// incomplete removal leaves the worktree and recovery to the caller.
func (d *Driver) Remove(ctx context.Context, r Request) error {
	state, source, path, err := d.inspect(ctx, r)
	if err != nil {
		return err
	}
	if len(state.Blockers) != 0 || !state.Present || !state.Clean {
		return fmt.Errorf("cannot remove worktree: %s", strings.Join(state.Blockers, "; "))
	}
	if _, err := run(ctx, d.executable(), "-C", source, "remove", "--no-delete-branch", "--foreground", "--format", "json", path); err != nil {
		return err
	}
	items, err := worktrees(ctx, source)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.path == path {
			return fmt.Errorf("wt remove returned before unregistering worktree %q", path)
		}
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("wt remove left destination %q on disk", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := git(ctx, source, "show-ref", "--verify", "refs/heads/"+r.Branch); err != nil {
		return fmt.Errorf("wt remove did not preserve branch %q: %w", r.Branch, err)
	}
	return nil
}
