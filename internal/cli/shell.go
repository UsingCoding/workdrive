package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/shellintegration"
)

const integrationHelp = "shell integration required: add eval \"$(workdrive config shell init zsh)\" to ~/.zshrc after compinit (or use init bash in ~/.bashrc); alternatively use switch --shell or --print-path"

func shellUtility(cmd *cobra.Command) bool {
	if cmd.CommandPath() == "workdrive config shell init" {
		return true
	}
	for current := cmd; current != nil; current = current.Parent() {
		if current.Parent() != nil && current.Parent().Parent() == nil {
			switch current.Name() {
			case "completion", "__complete", "__completeNoDesc":
				return true
			}
		}
	}
	return false
}

func (c *command) shellCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "shell", Short: "Shell integration commands"}
	cmd.AddCommand(&cobra.Command{Use: "init <zsh|bash>", Short: "Print shell integration and completion setup", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		script, err := shellintegration.Init(args[0])
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(c.out, script)
		return err
	}})
	return cmd
}

func (c *command) prepareNavigation() error {
	if c.integration == nil {
		return fmt.Errorf("%s", integrationHelp)
	}
	if err := c.integration.Preflight(); err != nil {
		return fmt.Errorf("%s: %w", integrationHelp, err)
	}
	// The origin only feeds optional `switch -` history. An unresolvable cwd,
	// such as a workspace just removed by cleanup, must not stop the user from
	// navigating away; it just records no origin.
	c.origin = nil
	if cwd, err := os.Getwd(); err == nil {
		c.origin, _ = c.service.WorkspaceAt(cwd)
	}
	return nil
}

func workspaceIdentity(w *domain.Workspace) shellintegration.Identity {
	return shellintegration.Identity{ID: w.ID, Key: w.Key, Path: w.Path}
}

func (c *command) publishNavigation(n app.Navigation) error {
	var previous *shellintegration.Identity
	if c.origin != nil && c.origin.ID != n.Workspace.ID {
		identity := workspaceIdentity(c.origin)
		previous = &identity
	}
	c.showWarnings(n.Warnings)
	return c.integration.Publish(n.Path, previous)
}

func (c *command) previousWorkspace() (shellintegration.Identity, error) {
	if c.integration == nil || c.integration.PreviousToken() == "" {
		return shellintegration.Identity{}, fmt.Errorf("no previous workspace in this shell; switch between workspaces first")
	}
	identity, err := shellintegration.DecodeIdentity(c.integration.PreviousToken())
	if err != nil {
		return shellintegration.Identity{}, fmt.Errorf("invalid previous workspace: %w; select a workspace explicitly", err)
	}
	return identity, nil
}
