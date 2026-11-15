# Workdrive (MVP 1)

Workdrive manages local task directories and Git worktrees on macOS/Linux. This checkpoint is offline and requires no issue tracker, TUIOS, network service, daemon, or Claude integration. Source Git repositories are **linked**, never owned; Workdrive-created worktrees are owned, but their Git branches are preserved when removed.

## Requirements and build

- Go 1.26.3 and [mise](https://mise.jdx.dev/) for the development tasks.
- Git and [Worktrunk](https://worktrunk.dev/) (`wt`) installed for worktree operations. A zero-repository workspace does not require `wt`.

```sh
mise run build
mise run lint
mise run test
.build/workdrive --help
```

`mise run fmt` formats the Go sources. To run the disposable **real Worktrunk** adapter test, explicitly opt in with `WORKDRIVE_REAL_WT=1 go test ./internal/drivers/worktrunk`. Tests never use your existing repositories.

## Docker CLI end-to-end tests

`mise run test:e2e` runs Go test cases that start disposable Docker containers via testcontainers-go. The single-stage Debian Trixie image uses mise to install the project-pinned Go and Worktrunk v0.81.0; its OS-provided Git is checked at build time to satisfy Worktrunk's Git 2.43.0 minimum. It builds the real Workdrive CLI and includes bash, zsh, and a Go PTY driver for the interactive wizard, dashboard, current-shell navigation and destructive confirmations. Git fixtures, configuration, state, and workspaces are created inside the containers; no host repository is mounted. A running Docker daemon is required. This opt-in suite is separate from `mise run test` and runs in a dedicated Ubuntu CI job; a requested E2E run fails if Docker is unavailable. See [the E2E architecture and scenarios](docs/E2E_TESTS.md).

## Configuration

Copy [`examples/config.toml`](examples/config.toml) to `~/.config/workdrive/config.toml`, or pass `--config PATH`. Without a config file, safe defaults use `~/projects` as the bounded discovery root and `~/projects/workspaces` for workspaces. `XDG_CONFIG_HOME` and `XDG_STATE_HOME` override the corresponding base locations. Named profile overlays may live in `~/.config/workdrive/profiles/*.toml`. Run `workdrive config validate` after editing.

Keys are **case-sensitive** and are portable directory names; the display title can contain spaces. Source discovery is bounded, cached per profile and refreshable with `Ctrl+R` in the wizard or `workdrive refresh`; `repositories.refresh_on_start` requests an asynchronous initial scan. Worktree destinations must remain inside the configured workspace root. Worktrunk v0.68.0 does not support `wt switch --path`; Workdrive checks capabilities and uses its invocation-local `--config-set worktree-path=...` override instead, without changing your saved Worktrunk configuration. Existing Worktrunk hooks remain enabled. `base_ref = "default"` means Worktrunk's default base.

## Use

```sh
workdrive                                               # interactive dashboard
workdrive new scratch-api                                # editable four-step wizard
workdrive new PLATFORM-1425 --name 'Auth token refresh' --repo auth-service --repo user-service
workdrive list --state all --json
workdrive inspect scratch-api
workdrive add repo scratch-api --repo /absolute/path/to/local-repo
workdrive park scratch-api                               # metadata only; files remain
workdrive new scratch-ui --switch                         # wizard; Enter on success navigates
workdrive switch scratch-api                             # current shell, after init below
workdrive switch                                         # picker, then current-shell navigation
workdrive switch -                                       # previous workspace in this shell
workdrive switch scratch-api --repo auth-service          # recorded worktree, not a source repo
workdrive switch scratch-api --shell                     # explicitly start a nested shell
workdrive switch scratch-api --print-path                # absolute path only on stdout
workdrive remove repo scratch-api --repo /absolute/source/path
workdrive cleanup scratch-api                            # interactive preflight and approval
workdrive refresh                                        # scan/reindex local manifests
workdrive resume scratch-api                             # inspect and retry interrupted work
```

`new` **always** opens the editable wizard; it never provisions on flag parsing. The dashboard has search/filter/detail navigation and creation actions. Dashboard keys: `↑/↓` or `j/k` move, `/` fuzzy search, `f` cycle active/parked/all, `d` details, `n` new, `a` add repository, `z` remove repository, `p` park, `x` cleanup, `r` refresh, `Enter` exit into the workspace, `?` help, `q` quit without navigating. Dashboard `n` returns to browsing after creation.

In the [Huh](https://github.com/charmbracelet/huh)-based wizard, `Tab`/`Shift+Tab` or `Enter` navigate fields, `/` filters repositories by name or path, and `Space` toggles selections. `Ctrl+N` advances from repository selection or skips ahead to review from worktree settings; `Ctrl+B` goes back, `Ctrl+R` rescans sources on the repository step, and `Esc` cancels. Optional fields show effective defaults as hints: leave them blank to use the workspace key as its name and the configured profile's paths, branch and base ref. Name/path hints update as the key, name or profile changes. Nothing is provisioned until the final review is explicitly approved. With `new --switch`, only **Enter on the fully successful result** navigates to the final edited workspace; `q`, `Esc`, and `Ctrl+C` close without navigating. Failed, partial or canceled creation never navigates.

Use absolute paths for duplicate source repository names. Selection and destructive approval require an interactive terminal; noninteractive cleanup refuses to run. `park` does not terminate processes in MVP 1.

## Current-shell navigation and completion

Install the binary on `PATH` (for a local build, `export PATH="$PWD/.build:$PATH"` from the project directory), then add one of these lines to your shell startup file (or evaluate it in the current session):

```sh
# ~/.zshrc, after your existing compinit initialization
eval "$(workdrive config shell init zsh)"

# ~/.bashrc
eval "$(workdrive config shell init bash)"
```

Initialization prints a shell function and registers Cobra completion; it does not edit rc files or load workspace configuration. It is safe to repeat. Bash and zsh are supported; fish and prompt integration are not included. With initialization, ordinary `switch`, the switch picker, dashboard Enter, and confirmed `new --switch` change the **same shell's** directory after the CLI/TUI exits. Without it, switching explains setup before activation instead of silently starting a child shell; dashboard browsing still works.

`switch -` returns to the previous **workspace**, using shell-local history rather than `OLDPWD` or a global active workspace. Switching between different workspaces records the one being left; selecting another repository in the same workspace does not rotate history. Manual `cd` is accounted for on the next navigation, while starting outside a workspace retains existing history. Shells do not share this history. Deleted/replaced workspaces or incompatible configuration produce an error, not a best-guess destination. The literal workspace key `-` remains valid: use `workdrive switch -- -` for that key.

`--repo` selects a worktree already recorded in that workspace by resource ID, source path, destination path, or unique source basename. It works with previous-workspace navigation, `--print-path`, and `--shell`. Missing, ambiguous or unsafe targets fail without falling back to the workspace root; dirty worktrees are navigable. No worktree is created or repaired by switching.

`--shell` explicitly starts a nested interactive `$SHELL`; exiting it returns to the parent's unchanged directory. `--print-path` emits exactly the absolute destination and a newline on stdout (warnings go to stderr), preserving its activation behavior without updating shell history. These two modes are mutually exclusive. For scripts, use `cd -- "$(workdrive switch scratch-api --print-path)"`; this manual `cd` does not update Workdrive history.

An executable alone cannot change its parent's cwd: `command workdrive`, a direct executable path, a pipeline, or a subshell bypasses or confines the wrapper's navigation. Explicit-key integrated switching does not require a TTY; pickers, dashboard, wizard and nested interactive shells do. Activation and the final shell `cd` cannot be atomic: a late handoff/`cd` failure does not roll back an activated workspace. The wrapper uses a private, literal handoff record rather than evaluating runtime paths as shell code. While a wrapped command runs, it owns Ctrl-C cancellation and restores prior traps afterward; it does not replay a pre-existing INT handler for that intercepted event.

### Context-aware Tab completion

Initialization includes completion; standalone scripts are also available from `workdrive completion bash` and `workdrive completion zsh`. Suggestions depend on the command:

| Context | Suggestions |
| --- | --- |
| Existing workspace arguments | Manifest-validated indexed keys with title/state descriptions; `switch` only offers active/parked keys, plus `-` when this shell's previous token is valid. |
| `switch KEY --repo`, `switch - --repo` | Recorded worktrees for that workspace, not source repositories. |
| `remove repo KEY --repo` | Unambiguous recorded resource selectors. |
| `new --repo` | Bounded local source hints, excluding already supplied sources. |
| `add repo KEY --repo` | Source hints for the workspace's profile, excluding attached/already supplied sources. |
| `--profile`, `list --state`, `config shell init` | Configured profiles; `active`/`parked`/`all`; `bash`/`zsh`. |
| `--config` | TOML filenames, without loading the candidate file. |
| `new --base` | `default`, plus common local refs of explicitly selected, unambiguously resolved sources. |
| `new [key]`, `--name` | Free text, without unrelated file/workspace suggestions. |

Completion is read-only and offline: it never activates, repairs, refreshes, fetches, runs hooks, creates state, or changes shell history. Workspace suggestions consult the existing index and recheck manifests rather than scanning all workspace roots. Source hints offer short names only when a complete, warning-free bounded scan proves they resolve uniquely; otherwise they use accepted absolute paths. An absolute-path prefix also requests paths. Traversal is metadata-only, not a full Git validation or guaranteed persistent-cache hit on every Tab; execution revalidates selected arguments. Static, profile and indexed-key callbacks do not run source discovery. Local-ref completion reads only explicitly selected sources. Ambiguity or a deadline may limit suggestions.

## Safety and recovery

Before cleanup or individual worktree removal, Workdrive verifies recorded ownership against Git, clean staged/unstaged/untracked/ignored status, branch and path identity, and unrecognized workspace files. Any blocker prevents approval; there is no automatic force option. Cleanup invokes Worktrunk removal by path in the foreground with `--no-delete-branch`, waits for completion, and removes only recognized metadata and an otherwise-empty workspace. Resolve blockers manually before retrying. Never assume a Git branch was deleted.

Manifests at `<workspace>/.workdrive/workspace.toml` are authoritative; `~/.local/state/workdrive/registry.db` is a rebuildable index. Cross-process locks and operation journals under `~/.local/state/workdrive/operations/` record intended/completed steps before mutations. A partial failure normally leaves `attention_required`; interruption before the first manifest leaves an inspectable create journal. Inspect the workspace and run `workdrive resume KEY` to retry after actual-state checks. `refresh` repairs missing index entries from valid manifests. It does not import unknown directories or assume ownership of unregistered worktrees.

## Scope

Only MVP 1 in [`spec/WORKDRIVE_SPEC.md`](spec/WORKDRIVE_SPEC.md) is implemented. Generic command-backed resources, TUIOS, YouTrack, custom hooks, dynamic search, and automatic remote actions belong to later MVPs. Worktree resource records in versioned manifests reserve their future representation without making any external integration mandatory.

See [MVP 1 verification report](docs/MVP1_CHECKLIST.md) for acceptance coverage and deviations.
