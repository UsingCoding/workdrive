package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ExpandPath expands a leading ~/ only; arbitrary environment substitutions
// are deliberately unsupported in paths and templates.
func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute or start with ~/: %q", path)
	}
	return filepath.Clean(path), nil
}

// CanonicalPath resolves symlinks in the longest existing ancestor, including
// when the final workspace or worktree directory has yet to be created.
func CanonicalPath(path string) (string, error) {
	abs, err := ExpandPath(path)
	if err != nil {
		return "", err
	}
	var missing []string
	ancestor := abs
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve %q: %w", path, err)
		}
		// EvalSymlinks reports ENOENT both for a genuinely missing path and
		// for a broken link; the latter must never be treated as creatable.
		if info, lstatErr := os.Lstat(ancestor); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("resolve %q: broken symlink %q", path, ancestor)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("cannot resolve path %q", path)
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
}

// ValidateDestination requires a strictly nested canonical destination and
// rejects lexical traversal even when it would eventually normalize inside.
func ValidateDestination(root, destination string) (string, error) {
	if hasDotDot(destination) {
		return "", fmt.Errorf("destination contains ..: %q", destination)
	}
	base, err := CanonicalPath(root)
	if err != nil {
		return "", err
	}
	lexicalRoot, err := ExpandPath(root)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(destination) {
		return "", fmt.Errorf("destination must be absolute: %q", destination)
	}
	clean := filepath.Clean(destination)
	// A configured root may itself contain a symlink (for example macOS /var).
	// Normalize only that root prefix, never arbitrary destination symlinks.
	if lexicalRoot != base {
		if rel, err := filepath.Rel(lexicalRoot, clean); err == nil && nested(rel) {
			clean = filepath.Join(base, rel)
		}
	}
	// Refuse lexical escapes as well as resolved escapes; an outside symlink
	// pointing back inside the workspace is not a valid destination either.
	lexicalRel, err := filepath.Rel(base, clean)
	if err != nil || !nested(lexicalRel) {
		return "", fmt.Errorf("destination %q must stay strictly inside %q", destination, base)
	}
	// Even broken symlinks must not become an unreviewed parent directory.
	ancestor := base
	for _, component := range strings.Split(lexicalRel, string(filepath.Separator)) {
		ancestor = filepath.Join(ancestor, component)
		info, err := os.Lstat(ancestor)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("destination traverses symlink %q", ancestor)
		}
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect destination %q: %w", ancestor, err)
		}
	}
	resolved, err := CanonicalPath(clean)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, resolved)
	if err != nil || !nested(rel) {
		return "", fmt.Errorf("destination %q must stay strictly inside %q", destination, base)
	}
	return resolved, nil
}

func nested(rel string) bool {
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CheckNewDestination refuses implicit adoption of existing worktrees or
// nonempty workspace directories. Empty existing workspace dirs may be
// permitted only by an explicit higher-level policy, never by this helper.
func CheckNewDestination(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return fmt.Errorf("destination already exists: %q", path)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect destination %q: %w", path, err)
	}
	return nil
}

type TemplateContext struct {
	Workspace TemplateWorkspace
	Repo      TemplateRepo
}

type TemplateWorkspace struct {
	Key  string
	Name string
	Path string
}

type TemplateRepo struct {
	Name string
	Path string
}

var templateVariable = regexp.MustCompile(`^\s*\.(Workspace\.(Key|Name|Path)|Repo\.(Name|Path))\s*$`)
var action = regexp.MustCompile(`\{\{(.*?)\}\}`)

// RenderTemplate replaces only five declared fields, without functions,
// control actions, pipelines, escaping or arbitrary Go template evaluation.
func RenderTemplate(text string, context TemplateContext) (string, error) {
	if text == "" {
		return "", fmt.Errorf("empty template")
	}
	var output strings.Builder
	remaining := text
	for {
		loc := action.FindStringSubmatchIndex(remaining)
		if loc == nil {
			if strings.Contains(remaining, "{{") || strings.Contains(remaining, "}}") {
				return "", fmt.Errorf("malformed or unsupported template action")
			}
			output.WriteString(remaining)
			return output.String(), nil
		}
		if strings.Contains(remaining[:loc[0]], "{{") || strings.Contains(remaining[:loc[0]], "}}") {
			return "", fmt.Errorf("malformed template action")
		}
		output.WriteString(remaining[:loc[0]])
		name := remaining[loc[2]:loc[3]]
		if !templateVariable.MatchString(name) {
			return "", fmt.Errorf("unsupported template action {{%s}}", name)
		}
		switch strings.TrimSpace(name) {
		case ".Workspace.Key":
			output.WriteString(context.Workspace.Key)
		case ".Workspace.Name":
			output.WriteString(context.Workspace.Name)
		case ".Workspace.Path":
			output.WriteString(context.Workspace.Path)
		case ".Repo.Name":
			output.WriteString(context.Repo.Name)
		case ".Repo.Path":
			output.WriteString(context.Repo.Path)
		}
		remaining = remaining[loc[1]:]
	}
}

// ValidateBranch delegates to Git's authoritative ref format check. No shell
// is involved; never trust the rendered branch as a command argument option.
func ValidateBranch(branch string) error {
	if branch == "" {
		return fmt.Errorf("empty Git branch")
	}
	output, err := exec.Command("git", "check-ref-format", "--branch", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("invalid Git branch %q: %s%v", branch, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// ValidateExclude accepts slash-separated, bounded glob segments; ** must be
// an entire segment, and neither absolute nor parent-relative patterns work.
func ValidateExclude(pattern string) error {
	if pattern == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "\\") || hasDotDot(pattern) {
		return fmt.Errorf("invalid exclude pattern %q", pattern)
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "" || (strings.Contains(segment, "**") && segment != "**") {
			return fmt.Errorf("invalid exclude pattern %q", pattern)
		}
		if segment != "**" {
			if _, err := filepath.Match(segment, "any"); err != nil {
				return fmt.Errorf("invalid exclude pattern %q: %w", pattern, err)
			}
		}
	}
	return nil
}
