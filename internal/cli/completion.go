package cli

import (
	"context"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/usingcoding/workdrive/internal/app"
	"github.com/usingcoding/workdrive/internal/config"
	"github.com/usingcoding/workdrive/internal/discovery"
	"github.com/usingcoding/workdrive/internal/domain"
	"github.com/usingcoding/workdrive/internal/drivers/worktrunk"
	"github.com/usingcoding/workdrive/internal/shellintegration"
)

const semanticCompletion = cobra.ShellCompDirectiveNoFileComp

func noSemanticArgs(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, semanticCompletion
}

// configureCompletions installs metadata only. In particular, constructing the
// command tree never loads configuration, opens SQLite or starts discovery.
func (c *command) configureCompletions(root *cobra.Command) {
	preRun := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Name() == cobra.ShellCompRequestCmd || cmd.CalledAs() == cobra.ShellCompNoDescRequestCmd {
			request := append([]string(nil), args...)
			ctx := context.WithValue(cmd.Context(), completionRequestKey{}, request)
			root.SetContext(ctx)
			cmd.SetContext(ctx)
		}
		if preRun != nil {
			return preRun(cmd, args)
		}
		return nil
	}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.ValidArgsFunction = noSemanticArgs
		switch strings.TrimPrefix(cmd.CommandPath(), root.Name()+" ") {
		case "switch", "inspect", "park", "cleanup", "refresh", "resume", "add repo", "remove repo":
			cmd.ValidArgsFunction = c.completeWorkspaces
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	_ = root.RegisterFlagCompletionFunc("config", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"toml"}, cobra.ShellCompDirectiveFilterFileExt
	})
	_ = root.RegisterFlagCompletionFunc("profile", c.completeProfiles)
	attach := func(path []string, flag string, callback cobra.CompletionFunc) {
		if cmd := completionCommand(root, path...); cmd != nil {
			_ = cmd.RegisterFlagCompletionFunc(flag, callback)
		}
	}
	attach([]string{"switch"}, "repo", c.completeResources)
	attach([]string{"remove", "repo"}, "repo", c.completeResources)
	attach([]string{"new"}, "repo", c.completeSources)
	attach([]string{"add", "repo"}, "repo", c.completeSources)
	attach([]string{"new"}, "base", c.completeBase)
	attach([]string{"new"}, "name", noSemanticArgs)
	attach([]string{"list"}, "state", fixedCompletion("active", "parked", "all"))
	if init := completionCommand(root, "config", "shell", "init"); init != nil {
		init.ValidArgsFunction = func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
			if len(args) != 0 {
				return nil, semanticCompletion
			}
			return fixedCompletion("zsh", "bash")(cmd, args, prefix)
		}
	}
}

type completionRequestKey struct{}

type ignoredCompletionValue struct{ kind string }

func (v ignoredCompletionValue) String() string   { return "" }
func (v ignoredCompletionValue) Set(string) error { return nil }
func (v ignoredCompletionValue) Type() string     { return v.kind }

// Cobra parses completion flags once with a synthetic trailing "--", then
// again without it. pflag retains that synthetic ArgsLenAtDash, so it cannot
// distinguish "switch <TAB>" from "switch -- <TAB>". Reparse the saved argv with
// Cobra/pflag (not a shell parser), cloning flag metadata with discarded values
// to avoid mutating bound flags or duplicating repeated --repo values.
func completionLiteralDash(cmd *cobra.Command) bool {
	ctx := cmd.Context()
	if ctx == nil {
		return cmd.ArgsLenAtDash() == 0
	}
	request, ok := ctx.Value(completionRequestKey{}).([]string)
	if !ok || len(request) == 0 {
		return cmd.ArgsLenAtDash() == 0
	}
	_, args, err := cmd.Root().Find(request[:len(request)-1])
	if err != nil {
		return false
	}
	flags := pflag.NewFlagSet("completion-boundary", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		clone := *flag
		clone.Value = ignoredCompletionValue{kind: flag.Value.Type()}
		flags.AddFlag(&clone)
	})
	return flags.Parse(args) == nil && flags.ArgsLenAtDash() == 0
}

func completionCommand(root *cobra.Command, names ...string) *cobra.Command {
	cmd := root
	for _, name := range names {
		var next *cobra.Command
		for _, child := range cmd.Commands() {
			if child.Name() == name {
				next = child
				break
			}
		}
		if next == nil {
			return nil
		}
		cmd = next
	}
	return cmd
}

func fixedCompletion(values ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		return completionValues(values, prefix), semanticCompletion
	}
}

func completionValues(values []string, prefix string) []string {
	set := make(map[string]bool)
	for _, candidate := range values {
		value, description, _ := strings.Cut(candidate, "\t")
		// Cobra's line/tab protocol cannot represent these values, even if a
		// shell could quote the original path. Do not silently change a value.
		if value == "" || strings.ContainsAny(value, "\t\r\n\x00") || !strings.HasPrefix(value, prefix) {
			continue
		}
		description = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, description)
		if description != "" {
			value += "\t" + description
		}
		set[value] = true
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// described rejects protocol delimiters in values before joining descriptions.
func described(value, description string) string {
	if strings.ContainsAny(value, "\t\r\n\x00") {
		return ""
	}
	return value + "\t" + description
}

func (c *command) completionQuery() (*config.Config, *app.Query, error) {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return nil, nil, err
	}
	q, err := app.OpenQuery(stateDir())
	return cfg, q, err
}

func (c *command) completeProfiles(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return nil, semanticCompletion
	}
	var values []string
	for name := range cfg.Profiles {
		values = append(values, name)
	}
	return completionValues(values, prefix), semanticCompletion
}

func (c *command) completeWorkspaces(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, semanticCompletion
	}
	_, q, err := c.completionQuery()
	if err != nil {
		return nil, semanticCompletion
	}
	defer q.Close()
	workspaces, err := q.List()
	if err != nil {
		return nil, semanticCompletion
	}
	var values []string
	literalDash := completionLiteralDash(cmd)
	for _, w := range workspaces {
		if cmd.Name() == "switch" {
			if !switchable(w) || (w.Key == "-" && !literalDash) {
				continue
			}
		}
		values = append(values, described(w.Key, w.Name+" ("+string(w.State)+")"))
	}
	if cmd.Name() == "switch" && !literalDash {
		if w := c.previousCompletionWorkspace(q); w != nil {
			values = append(values, described("-", "previous: "+w.Name))
		}
	}
	return completionValues(values, prefix), semanticCompletion
}

func switchable(w *domain.Workspace) bool {
	return w.State == domain.StateActive || w.State == domain.StateParked
}

func (c *command) previousCompletionWorkspace(q *app.Query) *domain.Workspace {
	if c.integration == nil {
		return nil
	}
	identity, err := shellintegration.DecodeIdentity(c.integration.PreviousToken())
	if err != nil {
		return nil
	}
	w, err := q.Find(identity.Key)
	if err != nil || w.ID != identity.ID || w.Path != identity.Path || !switchable(w) {
		return nil
	}
	return w
}

func (c *command) completionWorkspace(cmd *cobra.Command, q *app.Query, args []string) *domain.Workspace {
	if len(args) != 1 {
		return nil
	}
	if cmd.Name() == "switch" && args[0] == "-" && !completionLiteralDash(cmd) {
		return c.previousCompletionWorkspace(q)
	}
	w, _ := q.Find(args[0])
	return w
}

func (c *command) completeResources(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	_, q, err := c.completionQuery()
	if err != nil {
		return nil, semanticCompletion
	}
	defer q.Close()
	w := c.completionWorkspace(cmd, q, args)
	if w == nil || (cmd.Name() == "switch" && !switchable(w)) {
		return nil, semanticCompletion
	}
	var values []string
	for _, r := range w.Resources {
		if cmd.Name() == "switch" && (r.Type != "worktree" || r.Driver != "worktrunk") {
			continue
		}
		for _, selector := range []string{filepath.Base(r.Source), r.ID, r.Source, r.Path} {
			if !strings.HasPrefix(selector, prefix) {
				continue
			}
			matched, err := app.ResolveResource(w, selector)
			if err == nil && matched.ID == r.ID && described(selector, "") != "" {
				values = append(values, described(selector, r.Source+" → "+r.Path))
				break
			}
		}
	}
	return completionValues(values, prefix), semanticCompletion
}

// sourceHints merges cheap recorded paths with bounded fresh metadata. A
// truncated scan cannot establish global uniqueness of a basename; callers
// must retain absolute paths unless the complete hint set round-trips.
func (c *command) sourceHints(ctx context.Context, cmd *cobra.Command, args []string) (discovery.Result, map[string]bool) {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return discovery.Result{}, nil
	}
	q, err := app.OpenQuery(stateDir())
	if err != nil {
		return discovery.Result{}, nil
	}
	defer q.Close()
	profile := c.profile
	excluded := map[string]bool{}
	if cmd.Name() == "repo" { // add repo: use the target workspace's profile.
		w := c.completionWorkspace(cmd, q, args)
		if w == nil {
			return discovery.Result{}, nil
		}
		profile = w.Profile
		for _, r := range w.Resources {
			if path, err := config.CanonicalPath(r.Source); err == nil {
				excluded[path] = true
			}
		}
	}
	effective, err := cfg.Effective(profile)
	if err != nil {
		return discovery.Result{}, nil
	}
	roots := cfg.WorkspaceRoots()
	found := discovery.Hints(ctx, effective.Repositories, discovery.HintLimits{}, roots...)
	seen := map[string]bool{}
	for _, r := range found.Repositories {
		seen[r.Path] = true
	}
	workspaces, _ := q.List()
	for _, w := range workspaces {
		for _, r := range w.Resources {
			path, err := config.CanonicalPath(r.Source)
			if err == nil && !seen[path] && discovery.AllowsHint(effective.Repositories, path, roots...) {
				found.Repositories = append(found.Repositories, discovery.Repository{Name: filepath.Base(path), Path: path})
				seen[path] = true
			}
		}
	}
	return found, excluded
}

func suppliedRepos(cmd *cobra.Command) []string {
	repos, _ := cmd.Flags().GetStringArray("repo")
	return repos
}

func (c *command) completeSources(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	found, excluded := c.sourceHints(cmd.Context(), cmd, args)
	for _, selector := range suppliedRepos(cmd) {
		if r, err := discovery.Resolve(found.Repositories, selector); err == nil {
			excluded[r.Path] = true
		} else {
			// Omit all possibilities of an already supplied ambiguous name.
			for _, r := range found.Repositories {
				if r.Name == selector {
					excluded[r.Path] = true
				}
			}
		}
	}
	var values []string
	for _, r := range found.Repositories {
		if !excluded[r.Path] {
			value := r.Path
			if !filepath.IsAbs(prefix) && !found.Incomplete && len(found.Warnings) == 0 {
				if matched, err := discovery.Resolve(found.Repositories, r.Name); err == nil && matched.Path == r.Path {
					value = r.Name
				}
			}
			values = append(values, described(value, r.Path+" (local source hint)"))
		}
	}
	return completionValues(values, prefix), semanticCompletion
}

func (c *command) completeBase(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	values := []string{"default"}
	selected := suppliedRepos(cmd)
	if len(selected) == 0 {
		return completionValues(values, prefix), semanticCompletion
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 250*time.Millisecond)
	defer cancel()
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return completionValues(values, prefix), semanticCompletion
	}
	// `new` sources come from the selected profile, exactly as execution and
	// the source hints resolve them.
	effective, err := cfg.Effective(c.profile)
	if err != nil {
		return completionValues(values, prefix), semanticCompletion
	}
	roots := cfg.WorkspaceRoots()
	var found *discovery.Result // scanned only if a selector is a name
	var sources []string
	for _, selector := range selected {
		if filepath.IsAbs(selector) {
			path, err := config.CanonicalPath(selector)
			if err != nil || !discovery.AllowsHint(effective.Repositories, path, roots...) {
				return completionValues(values, prefix), semanticCompletion
			}
			sources = append(sources, path)
			continue
		}
		if found == nil {
			hints, _ := c.sourceHints(ctx, cmd, args)
			found = &hints
		}
		if found.Incomplete {
			return completionValues(values, prefix), semanticCompletion
		}
		r, err := discovery.Resolve(found.Repositories, selector)
		if err != nil {
			return completionValues(values, prefix), semanticCompletion
		}
		sources = append(sources, r.Path)
	}
	values = append(values, commonLocalRefs(ctx, worktrunk.GitRefs{}, sources)...)
	return completionValues(values, prefix), semanticCompletion
}

// commonLocalRefs reads each distinct source concurrently so several sources
// fit the shared deadline; any failure or timeout yields no refs.
func commonLocalRefs(ctx context.Context, reader worktrunk.LocalRefReader, sources []string) []string {
	var unique []string
	seen := map[string]bool{}
	for _, source := range sources {
		if !seen[source] {
			seen[source] = true
			unique = append(unique, source)
		}
	}
	type read struct {
		refs []string
		err  error
	}
	results := make([]read, len(unique))
	var wg sync.WaitGroup
	for i, source := range unique {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refs, err := reader.LocalRefs(ctx, source)
			results[i] = read{refs, err}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	var common map[string]bool
	for _, r := range results {
		if r.err != nil {
			return nil
		}
		current := map[string]bool{}
		for _, ref := range r.refs {
			current[ref] = true
		}
		if common == nil {
			common = current
			continue
		}
		for ref := range common {
			if !current[ref] {
				delete(common, ref)
			}
		}
	}
	var result []string
	for ref := range common {
		result = append(result, ref)
	}
	sort.Strings(result)
	return result
}
