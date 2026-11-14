# Docker CLI end-to-end tests

Run `mise run test:e2e` (or `go test -tags=e2e ./tests/e2e -count=1 -v -timeout=20m`) with a running Docker daemon. The single-stage Debian Trixie image bootstraps a checksum-verified mise binary; `mise install` reads the root Go pin and `tests/e2e/.mise.toml` to install Worktrunk v0.81.0. The OS-provided Git is checked at build time to satisfy Worktrunk's Git 2.43.0 minimum. The image builds the actual Workdrive executable and a small Go terminal driver. The testcontainers-go suite starts a fresh container for each independent scenario. It creates local committed Git sources, configuration, XDG state, and workspaces inside that container; it never mounts a host repository. The default `mise run test` does not need Docker. Ubuntu CI runs the opt-in suite separately.

The Go test process invokes the terminal driver with container exec for each CLI/Git command. The driver reports stdout, stderr, and exit status separately for noninteractive calls. For interactive calls it starts **the real CLI** under a Linux PTY, waits for a screen marker, and then sends the next key; per-screen and overall deadlines prevent hanging tests. Terminal transcripts are included in test failures. Each CLI call is a new process against the same container-local state, so persistence and recovery are tested across process boundaries. Destructive actions are always exercised through the production preview and confirmation UI, never by calling service methods directly.

## Scenarios

| Case | Verification |
| --- | --- |
| Smoke and zero-repository wizard | Help/config/list, non-TTY refusals, cancel review without mutation, approve zero-resource creation, inspect/list, cancel then approve cleanup. |
| Real worktrees | Wizard creates two independent worktrees; explicit CLI `add repo` creates a third without changing the first two; Git branches, registered paths, and file contents are checked. |
| Park, switch, remove, cleanup | Park preserves files; `switch --print-path` emits exactly one path; interactive rejection leaves data intact; approved removal and cleanup remove owned worktrees but preserve sources and branches. |
| Deletion blockers | Staged, unstaged, untracked, ignored and unknown workspace files independently block cleanup even when approval is attempted. |
| Index repair | `refresh` rediscovers the valid manifest after its disposable index is removed; unknown directories remain unowned. |
| Interrupted creation | A scoped failure wrapper delegates successful commands to the actual `wt`, fails one create after journaling, then is disabled; `resume` completes without duplicating already-created worktrees. |

Fine-grained adapter, journal, discovery, and TUI model cases remain in the default Go suite. On a machine without Docker, the explicitly requested E2E suite fails rather than silently skipping coverage. If the image cannot be downloaded or built, restore registry/network access before retrying; the tests do not download anything during their CLI scenarios.
