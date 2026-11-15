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

`mise run test:e2e` runs Go test cases that start disposable Docker containers via testcontainers-go. The single-stage Debian Trixie image uses mise to install the project-pinned Go and Worktrunk v0.81.0; its OS-provided Git is checked at build time to satisfy Worktrunk's Git 2.43.0 minimum. It builds the real Workdrive CLI and includes a Go PTY driver for the interactive wizard and destructive confirmations. Git fixtures, configuration, state, and workspaces are created inside the containers; no host repository is mounted. A running Docker daemon is required. This opt-in suite is separate from `mise run test` and runs in a dedicated Ubuntu CI job; a requested E2E run fails if Docker is unavailable. See [the E2E architecture and scenarios](docs/E2E_TESTS.md).

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
workdrive switch scratch-api                             # new interactive shell in workspace
workdrive switch scratch-api --print-path                # absolute path only on stdout
workdrive remove repo scratch-api --repo /absolute/source/path
workdrive cleanup scratch-api                            # interactive preflight and approval
workdrive refresh                                        # scan/reindex local manifests
workdrive resume scratch-api                             # inspect and retry interrupted work
```

`new` **always** opens the editable wizard; it never provisions on flag parsing. The dashboard has search/filter/detail navigation and creation actions. Dashboard keys: `↑/↓` or `j/k` move, `/` fuzzy search, `f` cycle active/parked/all, `d` details, `n` new, `a` add repository, `z` remove repository, `p` park, `x` cleanup, `r` refresh, `Enter` switch, `?` help, `q` quit.

In the [Huh](https://github.com/charmbracelet/huh)-based wizard, `Tab`/`Shift+Tab` or `Enter` navigate fields, `/` filters repositories by name or path, and `Space` toggles selections. `Ctrl+N` advances from repository selection or skips ahead to review from worktree settings; `Ctrl+B` goes back, `Ctrl+R` rescans sources on the repository step, and `Esc` cancels. Optional fields show effective defaults as hints: leave them blank to use the workspace key as its name and the configured profile's paths, branch and base ref. Name/path hints update as the key, name or profile changes. Nothing is provisioned until the final review is explicitly approved.

Use absolute paths for duplicate repository names. Selection and destructive approval require an interactive terminal; noninteractive cleanup refuses to run. The `--print-path` option is intended for shell integration, for example `cd "$(workdrive switch scratch-api --print-path)"`. Workdrive itself cannot change its parent shell's current directory. Normal `switch` starts a child shell in the workspace. `park` does not terminate processes in MVP 1.

## Safety and recovery

Before cleanup or individual worktree removal, Workdrive verifies recorded ownership against Git, clean staged/unstaged/untracked/ignored status, branch and path identity, and unrecognized workspace files. Any blocker prevents approval; there is no automatic force option. Cleanup invokes Worktrunk removal by path in the foreground with `--no-delete-branch`, waits for completion, and removes only recognized metadata and an otherwise-empty workspace. Resolve blockers manually before retrying. Never assume a Git branch was deleted.

Manifests at `<workspace>/.workdrive/workspace.toml` are authoritative; `~/.local/state/workdrive/registry.db` is a rebuildable index. Cross-process locks and operation journals under `~/.local/state/workdrive/operations/` record intended/completed steps before mutations. A partial failure normally leaves `attention_required`; interruption before the first manifest leaves an inspectable create journal. Inspect the workspace and run `workdrive resume KEY` to retry after actual-state checks. `refresh` repairs missing index entries from valid manifests. It does not import unknown directories or assume ownership of unregistered worktrees.

## Scope

Only MVP 1 in [`spec/WORKDRIVE_SPEC.md`](spec/WORKDRIVE_SPEC.md) is implemented. Generic command-backed resources, TUIOS, YouTrack, custom hooks, dynamic search, and automatic remote actions belong to later MVPs. Worktree resource records in versioned manifests reserve their future representation without making any external integration mandatory.

See [MVP 1 verification report](docs/MVP1_CHECKLIST.md) for acceptance coverage and deviations.
