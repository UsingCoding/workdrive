// Package config loads the offline MVP 1 configuration and validates paths
// and deliberately restricted templates before they can drive side effects.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/usingcoding/workdrive/internal/domain"
)

const SchemaVersion = 1

type Config struct {
	SchemaVersion int                `toml:"schema_version"`
	Workspace     WorkspaceConfig    `toml:"workspace"`
	Repositories  RepositoryConfig   `toml:"repositories"`
	Worktree      WorktreeConfig     `toml:"worktree"`
	UI            UIConfig           `toml:"ui"`
	Profiles      map[string]Profile `toml:"profiles"`
}

type WorkspaceConfig struct {
	Root              string `toml:"root"`
	DefaultProfile    string `toml:"default_profile"`
	DirectoryTemplate string `toml:"directory_template"`
}

type RepositoryConfig struct {
	RefreshOnStart bool     `toml:"refresh_on_start"`
	Sources        []Source `toml:"sources"`
}

type Source struct {
	Name     string   `toml:"name"`
	Path     string   `toml:"path"`
	MaxDepth int      `toml:"max_depth"`
	Exclude  []string `toml:"exclude"`
}

type WorktreeConfig struct {
	Driver                string `toml:"driver"`
	Executable            string `toml:"executable"`
	BranchTemplate        string `toml:"branch_template"`
	PathTemplate          string `toml:"path_template"`
	BaseRef               string `toml:"base_ref"`
	DeleteBranchOnCleanup bool   `toml:"delete_branch_on_cleanup"`
}

type UIConfig struct {
	ShowHidden bool `toml:"show_hidden"`
}

type Profile struct {
	WorkspaceRoot string           `toml:"workspace_root"`
	Worktree      WorktreeOverride `toml:"worktree"`
}

type WorktreeOverride struct {
	BranchTemplate string `toml:"branch_template"`
	PathTemplate   string `toml:"path_template"`
	BaseRef        string `toml:"base_ref"`
}

// Defaults returns safe, standalone defaults with no external integrations.
// The single ~/projects source is bounded and generated workspaces are always
// excluded during discovery, even when the user supplies no exclusion list.
func Defaults() *Config {
	return &Config{
		SchemaVersion: SchemaVersion,
		Workspace:     WorkspaceConfig{Root: "~/projects/workspaces", DefaultProfile: "development", DirectoryTemplate: "{{ .Workspace.Key }}"},
		Repositories:  RepositoryConfig{Sources: []Source{{Name: "projects", Path: "~/projects", MaxDepth: 3, Exclude: []string{"workspaces/**", "**/node_modules/**", "**/.cache/**"}}}},
		Worktree:      WorktreeConfig{Driver: "worktrunk", Executable: "wt", BranchTemplate: "{{ .Workspace.Key }}", PathTemplate: "{{ .Workspace.Path }}/{{ .Repo.Name }}", BaseRef: "default"},
		Profiles:      map[string]Profile{"development": {}},
	}
}

// ConfigPath follows XDG_CONFIG_HOME, falling back to ~/.config on macOS and
// Linux (not macOS's Application Support directory).
func ConfigPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "workdrive", "config.toml"), nil
}

func StatePath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "workdrive"), nil
}

// Load reads the global config and sorted profiles/*.toml overlays. An empty
// path selects XDG config; an absent default file uses compiled defaults.
// An explicitly named missing file is always an error.
func Load(path string) (*Config, error) {
	explicit := path != ""
	if !explicit {
		var err error
		path, err = ConfigPath()
		if err != nil {
			return nil, err
		}
	}
	var err error
	if explicit && !filepath.IsAbs(path) && path != "~" && !strings.HasPrefix(path, "~/") {
		path, err = filepath.Abs(path)
	} else {
		path, err = ExpandPath(path)
	}
	if err != nil {
		return nil, err
	}
	cfg := Defaults()
	if err := decodeFile(path, cfg); err != nil && (explicit || !errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), "profiles", "*.toml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, file := range files {
		var overlay struct {
			Profiles map[string]Profile `toml:"profiles"`
		}
		if err := decodeFile(file, &overlay); err != nil {
			return nil, err
		}
		if len(overlay.Profiles) != 1 {
			return nil, fmt.Errorf("%s: profile file must define exactly one [profiles.NAME]", file)
		}
		for name, profile := range overlay.Profiles {
			previous := cfg.Profiles[name]
			if profile.WorkspaceRoot != "" {
				previous.WorkspaceRoot = profile.WorkspaceRoot
			}
			if profile.Worktree.BranchTemplate != "" {
				previous.Worktree.BranchTemplate = profile.Worktree.BranchTemplate
			}
			if profile.Worktree.PathTemplate != "" {
				previous.Worktree.PathTemplate = profile.Worktree.PathTemplate
			}
			if profile.Worktree.BaseRef != "" {
				previous.Worktree.BaseRef = profile.Worktree.BaseRef
			}
			cfg.Profiles[name] = previous
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func decodeFile(path string, out any) error {
	md, err := toml.DecodeFile(path, out)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return fmt.Errorf("%s: unknown configuration field %s", path, extra[0])
	}
	return nil
}

// Validate rejects unsafe values and invalid configuration before execution.
func (c *Config) Validate() error {
	if c == nil || c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported configuration schema version")
	}
	if c.Worktree.Driver != "worktrunk" || c.Worktree.Executable == "" || c.Worktree.DeleteBranchOnCleanup {
		return fmt.Errorf("MVP 1 requires a worktrunk executable and preserved Git branches")
	}
	if c.Workspace.DefaultProfile == "" {
		return fmt.Errorf("workspace.default_profile is required")
	}
	if _, ok := c.Profiles[c.Workspace.DefaultProfile]; !ok {
		return fmt.Errorf("unknown default profile %q", c.Workspace.DefaultProfile)
	}
	if _, err := CanonicalPath(c.Workspace.Root); err != nil {
		return fmt.Errorf("workspace.root: %w", err)
	}
	for _, pair := range []struct{ label, value string }{
		{"workspace.directory_template", c.Workspace.DirectoryTemplate},
		{"worktree.path_template", c.Worktree.PathTemplate},
		{"worktree.branch_template", c.Worktree.BranchTemplate},
	} {
		if _, err := RenderTemplate(pair.value, TemplateContext{}); err != nil {
			return fmt.Errorf("%s: %w", pair.label, err)
		}
	}
	seen := map[string]bool{}
	for _, source := range c.Repositories.Sources {
		if source.Name == "" || seen[source.Name] || source.MaxDepth < 0 || source.MaxDepth > 16 {
			return fmt.Errorf("repository source %q: name must be unique and max_depth between 0 and 16", source.Name)
		}
		seen[source.Name] = true
		if _, err := CanonicalPath(source.Path); err != nil {
			return fmt.Errorf("repository source %q: %w", source.Name, err)
		}
		for _, pattern := range source.Exclude {
			if err := ValidateExclude(pattern); err != nil {
				return fmt.Errorf("repository source %q: %w", source.Name, err)
			}
		}
	}
	for name, profile := range c.Profiles {
		if err := domain.ValidateKey(name); err != nil {
			return fmt.Errorf("profile: %w", err)
		}
		if profile.WorkspaceRoot != "" {
			if _, err := CanonicalPath(profile.WorkspaceRoot); err != nil {
				return fmt.Errorf("profile %q workspace_root: %w", name, err)
			}
		}
		for _, value := range []string{profile.Worktree.BranchTemplate, profile.Worktree.PathTemplate} {
			if value != "" {
				if _, err := RenderTemplate(value, TemplateContext{}); err != nil {
					return fmt.Errorf("profile %q: %w", name, err)
				}
			}
		}
	}
	return nil
}

// Effective returns a separate, validated copy with profile overrides applied.
// Empty profile selects workspace.default_profile.
func (c *Config) Effective(profile string) (*Config, error) {
	if c == nil {
		return nil, fmt.Errorf("nil config")
	}
	if profile == "" {
		profile = c.Workspace.DefaultProfile
	}
	p, ok := c.Profiles[profile]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
	copy := *c
	if p.WorkspaceRoot != "" {
		copy.Workspace.Root = p.WorkspaceRoot
	}
	if p.Worktree.BranchTemplate != "" {
		copy.Worktree.BranchTemplate = p.Worktree.BranchTemplate
	}
	if p.Worktree.PathTemplate != "" {
		copy.Worktree.PathTemplate = p.Worktree.PathTemplate
	}
	if p.Worktree.BaseRef != "" {
		copy.Worktree.BaseRef = p.Worktree.BaseRef
	}
	if err := copy.Validate(); err != nil {
		return nil, err
	}
	return &copy, nil
}

// WorkspacePath expands the directory template using a workspace key.
// Use WorkspacePathWithName when the template references Workspace.Name.
func (c *Config) WorkspacePath(key string) (string, error) {
	return c.WorkspacePathWithName(key, "")
}

// WorkspacePathWithName expands the template and checks containment under the
// configured workspace root, including paths through existing symlinks.
func (c *Config) WorkspacePathWithName(key, name string) (string, error) {
	if err := domain.ValidateKey(key); err != nil {
		return "", err
	}
	if strings.Contains(c.Workspace.DirectoryTemplate, ".Repo.") || strings.Contains(c.Workspace.DirectoryTemplate, ".Workspace.Path") {
		return "", fmt.Errorf("directory template may reference only Workspace.Key and Workspace.Name")
	}
	if name == "" && strings.Contains(c.Workspace.DirectoryTemplate, ".Workspace.Name") {
		return "", fmt.Errorf("directory template needs a workspace name")
	}
	root, err := CanonicalPath(c.Workspace.Root)
	if err != nil {
		return "", err
	}
	part, err := RenderTemplate(c.Workspace.DirectoryTemplate, TemplateContext{Workspace: TemplateWorkspace{Key: key, Name: name}})
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(part) || hasDotDot(part) {
		return "", fmt.Errorf("workspace directory template must produce a relative path without ..")
	}
	return ValidateDestination(root, filepath.Join(root, part))
}

// WorktreePath expands the default path template. Use
// WorktreePathWithContext for templates referencing additional fields.
func (c *Config) WorktreePath(workspacePath, repoName string) (string, error) {
	return c.WorktreePathWithContext("", "", workspacePath, repoName, "")
}

// WorktreePathWithContext expands the path template with all permitted fields
// and checks it stays strictly inside the workspace. It does not adopt or
// overwrite an existing destination.
func (c *Config) WorktreePathWithContext(workspaceKey, workspaceName, workspacePath, repoName, repoPath string) (string, error) {
	if repoName == "" || repoName == "." || repoName == ".." || repoName == ".workdrive" || repoName == ".git" || strings.ContainsAny(repoName, "/\\") || strings.TrimSpace(repoName) != repoName || strings.ContainsFunc(repoName, func(r rune) bool { return r < 32 || r == 127 }) {
		return "", fmt.Errorf("invalid repository path component %q", repoName)
	}
	for _, field := range []struct{ variable, value string }{
		{".Workspace.Key", workspaceKey}, {".Workspace.Name", workspaceName}, {".Repo.Path", repoPath},
	} {
		if field.value == "" && strings.Contains(c.Worktree.PathTemplate, field.variable) {
			return "", fmt.Errorf("worktree path template needs %s", field.variable)
		}
	}
	root, err := CanonicalPath(workspacePath)
	if err != nil {
		return "", err
	}
	value, err := RenderTemplate(c.Worktree.PathTemplate, TemplateContext{Workspace: TemplateWorkspace{Key: workspaceKey, Name: workspaceName, Path: root}, Repo: TemplateRepo{Name: repoName, Path: repoPath}})
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("worktree path template must produce an absolute path")
	}
	if hasDotDot(value) {
		return "", fmt.Errorf("worktree path template contains ..")
	}
	return ValidateDestination(root, value)
}

// Branch renders the configured branch template and checks Git ref syntax.
func (c *Config) Branch(key, workspaceName, workspacePath, repoName, repoPath string) (string, error) {
	value, err := RenderTemplate(c.Worktree.BranchTemplate, TemplateContext{Workspace: TemplateWorkspace{Key: key, Name: workspaceName, Path: workspacePath}, Repo: TemplateRepo{Name: repoName, Path: repoPath}})
	if err != nil {
		return "", err
	}
	if err := ValidateBranch(value); err != nil {
		return "", err
	}
	return value, nil
}

func hasDotDot(path string) bool {
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == ".." {
			return true
		}
	}
	return false
}
