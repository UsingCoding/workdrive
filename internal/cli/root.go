package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/discovery"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/shellintegration"
	"github.com/usingcoding/workdrive/internal/tui"
)

type command struct {
	configPath, profile          string
	jsonOutput, noColor, verbose bool
	service                      *app.Service
	out, errOut                  io.Writer
	integration                  *shellintegration.Invocation
	origin                       *domain.Workspace
}

func Execute() error {
	integration, err := shellintegration.Capture()
	if err != nil {
		return err
	}
	defer integration.Close()
	c := &command{out: os.Stdout, errOut: os.Stderr, integration: integration}
	root := c.root()
	defer func() {
		if c.service != nil {
			c.service.Close()
		}
	}()
	return root.Execute()
}

func (c *command) root() *cobra.Command {
	root := &cobra.Command{Use: "workdrive", Short: "Local workspace and Git worktree lifecycle manager", SilenceUsage: true, SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if c.noColor {
				cmd.SetContext(tui.WithoutColor(cmd.Context()))
			}
			if shellUtility(cmd) {
				return nil
			}
			cfg, err := config.Load(c.configPath)
			if err != nil {
				return err
			}
			if cmd.CommandPath() == "workdrive config validate" {
				return nil
			}
			s, err := app.Open(cfg, stateDir())
			if err != nil {
				return err
			}
			c.service = s
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !tty() {
				return app.ErrNeedsTTY
			}
			navigation, err := tui.Dashboard(cmd.Context(), c.service, c.prepareNavigation)
			if err != nil || navigation == nil {
				return err
			}
			return c.publishNavigation(*navigation)
		},
	}
	root.PersistentFlags().StringVar(&c.configPath, "config", "", "configuration TOML path")
	root.PersistentFlags().StringVar(&c.profile, "profile", "", "workspace profile for new workspaces")
	root.PersistentFlags().BoolVar(&c.jsonOutput, "json", false, "machine-readable read-only output")
	root.PersistentFlags().BoolVar(&c.noColor, "no-color", false, "disable terminal colors")
	root.PersistentFlags().BoolVarP(&c.verbose, "verbose", "v", false, "verbose diagnostics")
	root.AddCommand(c.newCommand(), c.listCommand(), c.inspectCommand(), c.switchCommand(), c.parkCommand(), c.addCommand(), c.removeCommand(), c.cleanupCommand(), c.refreshCommand(), c.configCommand(), c.resumeCommand())
	c.configureCompletions(root)
	return root
}

func stateDir() string {
	if p := os.Getenv("XDG_STATE_HOME"); p != "" {
		return filepath.Join(p, "workdrive")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "workdrive")
}

func tty() bool { return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()) }
func (c *command) requireTTY() error {
	if !tty() {
		return app.ErrNeedsTTY
	}
	return nil
}
func (c *command) selectKey(ctx context.Context, arg []string) (string, error) {
	if len(arg) > 0 {
		return arg[0], nil
	}
	if err := c.requireTTY(); err != nil {
		return "", fmt.Errorf("workspace key omitted: %w", err)
	}
	return tui.PickWorkspace(ctx, c.service)
}
func (c *command) encode(v any) error { return json.NewEncoder(c.out).Encode(v) }
func (c *command) showWarnings(w []string) {
	for _, m := range w {
		fmt.Fprintln(c.errOut, "warning:", m)
	}
}

func (c *command) newCommand() *cobra.Command {
	var name, base string
	var repos []string
	var switchAfter bool
	cmd := &cobra.Command{Use: "new [key]", Short: "Open the editable workspace creation wizard", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if switchAfter {
			if err := c.prepareNavigation(); err != nil {
				return err
			}
		}
		if err := c.requireTTY(); err != nil {
			return fmt.Errorf("new always opens a wizard: %w", err)
		}
		key := ""
		if len(args) > 0 {
			key = args[0]
		}
		result, err := tui.NewWizard(cmd.Context(), c.service, app.NewRequest{Key: key, Name: name, Profile: c.profile}, repos, base, switchAfter)
		if err != nil || !result.Navigate {
			return err
		}
		navigation, err := c.service.Navigate(app.NavigationRequest{Key: result.Workspace.Key, ExpectedID: result.Workspace.ID, ExpectedPath: result.Workspace.Path})
		if err != nil {
			return err
		}
		return c.publishNavigation(navigation)
	}}
	cmd.Flags().BoolVar(&switchAfter, "switch", false, "offer current-shell navigation after successful creation")
	cmd.Flags().StringVar(&name, "name", "", "workspace display title")
	cmd.Flags().StringArrayVar(&repos, "repo", nil, "source repository name or absolute path (repeatable)")
	cmd.Flags().StringVar(&base, "base", "", "Git base ref for new branches")
	return cmd
}
func (c *command) listCommand() *cobra.Command {
	var state string
	cmd := &cobra.Command{Use: "list", Short: "List registered workspaces", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		ws, warnings, err := c.service.List()
		if err != nil {
			return err
		}
		c.showWarnings(warnings)
		filtered := make([]*domain.Workspace, 0, len(ws))
		for _, w := range ws {
			if state == "all" || string(w.State) == state {
				filtered = append(filtered, w)
			}
		}
		if c.jsonOutput {
			return c.encode(filtered)
		}
		for _, w := range filtered {
			fmt.Fprintf(c.out, "%-24s %-18s %2d worktrees  %-10s %s\n", w.Key, w.State, len(w.Resources), w.ActualStatus, w.Name)
		}
		return nil
	}}
	cmd.Flags().StringVar(&state, "state", "all", "active, parked or all")
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if state != "all" && state != "active" && state != "parked" {
			return fmt.Errorf("invalid state %q", state)
		}
		return nil
	}
	return cmd
}
func (c *command) inspectCommand() *cobra.Command {
	return &cobra.Command{Use: "inspect [key]", Short: "Inspect workspace and actual worktree health", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		key, err := c.selectKey(cmd.Context(), args)
		if err != nil {
			return err
		}
		w, warnings, err := c.service.Inspect(cmd.Context(), key)
		if err != nil {
			return err
		}
		if c.jsonOutput {
			return c.encode(struct {
				Workspace *domain.Workspace `json:"workspace"`
				Warnings  []string          `json:"warnings"`
			}{w, warnings})
		}
		fmt.Fprintf(c.out, "%s — %s\nPath: %s\nProfile: %s\nLifecycle: %s  Health: %s\n", w.Key, w.Name, w.Path, w.Profile, w.State, w.ActualStatus)
		for _, r := range w.Resources {
			fmt.Fprintf(c.out, "  %s  %s -> %s  branch %s  %s\n", r.ID, r.Source, r.Path, r.Branch, r.ActualStatus)
		}
		c.showWarnings(warnings)
		return nil
	}}
}
func (c *command) switchCommand() *cobra.Command {
	var printPath, nested bool
	var repository string
	cmd := &cobra.Command{Use: "switch [key|-]", Short: "Switch the current shell to a workspace", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !printPath && !nested {
			if err := c.prepareNavigation(); err != nil {
				return err
			}
		} else if nested {
			if err := c.requireTTY(); err != nil {
				return err
			}
		}
		key, err := c.selectKey(cmd.Context(), args)
		if err != nil {
			return err
		}
		req := app.NavigationRequest{Key: key, Repository: repository}
		// Only a typed `-` before any `--` means history; a picker selection or
		// `switch -- -` is the literal workspace key.
		if len(args) == 1 && args[0] == "-" && cmd.ArgsLenAtDash() != 0 {
			identity, err := c.previousWorkspace()
			if err != nil {
				return err
			}
			req.Key, req.ExpectedID, req.ExpectedPath = identity.Key, identity.ID, identity.Path
		}
		navigation, err := c.service.Navigate(req)
		if err != nil {
			return err
		}
		if printPath {
			c.showWarnings(navigation.Warnings)
			_, err = fmt.Fprintln(c.out, navigation.Path)
			return err
		}
		if !nested {
			return c.publishNavigation(navigation)
		}
		c.showWarnings(navigation.Warnings)
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		child := exec.CommandContext(cmd.Context(), shell, "-i")
		child.Dir = navigation.Path
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		return child.Run()
	}}
	cmd.Flags().BoolVar(&printPath, "print-path", false, "print the absolute destination path only")
	cmd.Flags().BoolVar(&nested, "shell", false, "start a nested interactive shell instead of navigating")
	cmd.Flags().StringVar(&repository, "repo", "", "recorded worktree ID, source/destination path or unique source basename")
	cmd.MarkFlagsMutuallyExclusive("shell", "print-path")
	return cmd
}
func (c *command) parkCommand() *cobra.Command {
	return &cobra.Command{Use: "park [key]", Short: "Mark a workspace parked without modifying worktrees", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		key, err := c.selectKey(cmd.Context(), args)
		if err != nil {
			return err
		}
		w, err := c.service.Park(key)
		if err == nil {
			fmt.Fprintln(c.out, w.Key, "parked")
		}
		return err
	}}
}

func (c *command) choices(ctx context.Context, values []string, profile string) ([]app.RepositoryChoice, error) {
	found := c.service.Discover(ctx, profile)
	for _, w := range found.Warnings {
		fmt.Fprintln(c.errOut, "warning:", w)
	}
	var choices []app.RepositoryChoice
	for _, v := range values {
		r, err := discovery.Resolve(found.Repositories, v)
		if err != nil && tty() && strings.Contains(err.Error(), "ambiguous repository") {
			var matches []discovery.Repository
			for _, candidate := range found.Repositories {
				if candidate.Name == v {
					matches = append(matches, candidate)
				}
			}
			r, err = tui.PickRepository(ctx, matches)
		}
		if err != nil {
			return nil, err
		}
		choices = append(choices, app.RepositoryChoice{Source: r.Path, Name: r.Name})
	}
	return choices, nil
}
func (c *command) addCommand() *cobra.Command {
	var repos []string
	repo := &cobra.Command{Use: "repo [key]", Short: "Add local repository worktrees", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		key, err := c.selectKey(cmd.Context(), args)
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			if err := c.requireTTY(); err != nil {
				return err
			}
			return tui.AddRepos(cmd.Context(), c.service, key)
		}
		workspace, err := c.service.Find(key)
		if err != nil {
			return err
		}
		choices, err := c.choices(cmd.Context(), repos, workspace.Profile)
		if err != nil {
			return err
		}
		plan, err := c.service.PlanAdd(cmd.Context(), key, choices)
		if err != nil {
			return err
		}
		if !plan.Safe() {
			return app.Blockers(plan.Blockers)
		}
		w, err := c.service.Add(cmd.Context(), plan)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "Added %d worktree(s) to %s\n", len(choices), w.Key)
		return nil
	}}
	repo.Flags().StringArrayVar(&repos, "repo", nil, "repository name or absolute source path (repeatable)")
	root := &cobra.Command{Use: "add", Short: "Add resources to a workspace"}
	root.AddCommand(repo)
	return root
}
func (c *command) removeCommand() *cobra.Command {
	var selector string
	repo := &cobra.Command{Use: "repo [key]", Short: "Safely remove one owned worktree", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := c.requireTTY(); err != nil {
			return err
		}
		key, err := c.selectKey(cmd.Context(), args)
		if err != nil {
			return err
		}
		if selector == "" {
			selector, err = tui.PickRepo(cmd.Context(), c.service, key)
			if err != nil {
				return err
			}
		}
		plan, err := c.service.PlanRemove(cmd.Context(), key, selector)
		if err != nil {
			return err
		}
		if !plan.Safe() {
			return app.Blockers(plan.Blockers)
		}
		approved, err := tui.Confirm(cmd.Context(), "Remove worktree "+plan.Remove[0].Path+"? Branch "+plan.Remove[0].Branch+" is preserved.")
		if err != nil {
			return err
		}
		if !approved {
			return nil
		}
		return c.service.Remove(cmd.Context(), plan)
	}}
	repo.Flags().StringVar(&selector, "repo", "", "source path, resource ID or unique repository name")
	root := &cobra.Command{Use: "remove", Short: "Remove resources from a workspace"}
	root.AddCommand(repo)
	return root
}
func (c *command) cleanupCommand() *cobra.Command {
	return &cobra.Command{Use: "cleanup [key]", Short: "Preview and approve safe workspace removal", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := c.requireTTY(); err != nil {
			return fmt.Errorf("cleanup refuses noninteractive execution: %w", err)
		}
		key, err := c.selectKey(cmd.Context(), args)
		if err != nil {
			return err
		}
		plan, err := c.service.PlanCleanup(cmd.Context(), key)
		if err != nil {
			return err
		}
		approved, err := tui.PreviewCleanup(cmd.Context(), plan)
		if err != nil {
			return err
		}
		if !approved {
			return nil
		}
		if err := c.service.Cleanup(cmd.Context(), plan); err != nil {
			return err
		}
		fmt.Fprintln(c.out, "Removed workspace", key)
		return nil
	}}
}
func (c *command) refreshCommand() *cobra.Command {
	return &cobra.Command{Use: "refresh [key]", Short: "Scan manifests and verify local state", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			w, warnings, err := c.service.Inspect(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c.showWarnings(warnings)
			if c.jsonOutput {
				return c.encode(w)
			}
			fmt.Fprintf(c.out, "%s: %s\n", w.Key, w.ActualStatus)
			return nil
		}
		warnings, err := c.service.Refresh(cmd.Context())
		c.showWarnings(warnings)
		return err
	}}
}
func (c *command) configCommand() *cobra.Command {
	root := &cobra.Command{Use: "config", Short: "Configuration commands"}
	root.AddCommand(c.shellCommand())
	root.AddCommand(&cobra.Command{Use: "validate", Short: "Validate configuration and profiles", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(c.configPath)
		if err != nil {
			return err
		}
		for name := range cfg.Profiles {
			if _, err := cfg.Effective(name); err != nil {
				return err
			}
		}
		fmt.Fprintln(c.out, "Configuration valid")
		return nil
	}})
	return root
}
func (c *command) resumeCommand() *cobra.Command {
	return &cobra.Command{Use: "resume [key]", Short: "Inspect and resume an interrupted operation", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		key, err := c.selectKey(cmd.Context(), args)
		if err != nil {
			return err
		}
		if err := c.service.Resume(cmd.Context(), key); err != nil {
			return err
		}
		fmt.Fprintln(c.out, "Resumed", key)
		return nil
	}}
}
