package worktrunk

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LocalRefReader is separate from the mutating Adapter so completion can be
// tested/injected without opening a lifecycle service or invoking Worktrunk.
type LocalRefReader interface {
	LocalRefs(context.Context, string) ([]string, error)
}

type GitRefs struct{ Executable string }

const MaxLocalRefs = 2000

// LocalRefs reads only existing refs of an explicitly selected checkout. It
// never fetches or executes hooks, and bounds both subprocess output and count.
func (g GitRefs) LocalRefs(ctx context.Context, source string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if !filepath.IsAbs(source) {
		return nil, fmt.Errorf("source must be absolute")
	}
	executable := g.Executable
	if executable == "" {
		executable = "git"
	}
	cmd := exec.CommandContext(ctx, executable, "--no-optional-locks", "-c", "core.fsmonitor=false", "-C", source,
		"for-each-ref", fmt.Sprintf("--count=%d", MaxLocalRefs+1), "--format=%(refname:short)", "refs/heads", "refs/tags", "refs/remotes")
	cmd.WaitDelay = 25 * time.Millisecond
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var out limitedRefOutput
	cmd.Stdout = &out
	cmd.Stderr = &limitedRefOutput{}
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var refs []string
	for _, ref := range strings.Split(out.String(), "\n") {
		if ref == "" {
			continue
		}
		refs = append(refs, ref)
	}
	if len(refs) > MaxLocalRefs {
		return nil, fmt.Errorf("too many local refs")
	}
	sort.Strings(refs)
	return refs, nil
}

type limitedRefOutput struct{ bytes.Buffer }

func (b *limitedRefOutput) Write(p []byte) (int, error) {
	if len(p) > 256*1024-b.Len() {
		return 0, fmt.Errorf("local ref output exceeds limit")
	}
	return b.Buffer.Write(p)
}
