# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and verify

Go 1.26.3 is pinned in `.mise.toml`; Git and Worktrunk (`wt`) are needed for worktree operations, but zero-repository workspaces do not need `wt`.

```sh
mise run build                 # go build -o .build/workdrive ./cmd/workdrive
mise run lint                  # gofmt check on cmd/internal and go vet ./...
mise run test                  # go test ./...
mise run fmt                   # gofmt -w cmd internal
go test ./internal/app -run '^TestZeroRepoWorkspaceLifecycle$' -count=1
go test -race ./internal/app ./internal/tui
WORKDRIVE_REAL_WT=1 go test ./internal/drivers/worktrunk -run '^TestRealWorktrunk$' -count=1
```

The last test is opt-in and uses disposable repositories with an installed real `wt`; normal tests use temporary repositories and fake Worktrunk adapters/executables. CI runs build, lint and test on macOS and Linux. Run `.build/workdrive --help` for CLI commands.

## Architecture

- `cmd/workdrive` enters the Cobra CLI in `internal/cli`; invoking the CLI without a subcommand starts the Bubble Tea dashboard, while `new` always starts an editable wizard (even with flags). `internal/tui` owns dashboard, wizard, selection and confirmation models; interactive selection and destructive approval require a TTY.
- `internal/app.Service` coordinates CLI/TUI operations. Its `PlanNew`, `PlanAdd`, `PlanRemove` and `PlanCleanup` methods gather blockers before mutation; `Create`, `Add`, `Remove` and `Cleanup` acquire a per-workspace cross-process lock and replan before acting. `Resume` inspects actual state before continuing interrupted operations. Keep safety checks in the service/adapter rather than bypassing them in a UI handler.
- `internal/domain` defines versioned workspace/resource identities, lifecycle state and separately observed health. `internal/manifest` persists the authoritative TOML at `<workspace>/.workdrive/workspace.toml`; `internal/registry` is only a rebuildable SQLite index under the XDG state directory. `List` rechecks manifests, `Inspect` checks Git state, and `Refresh` reconciles the index from valid manifests. An indexed entry alone does not establish ownership or health.
- `internal/lifecycle` holds operation journals and locks under the state directory. The service journals intended steps before mutating files or worktrees; interrupted operations can become `attention_required`, including creation interrupted before the first manifest. Preserve this ordering and recovery path when changing lifecycle operations.
- `internal/config` loads defaults, TOML and named profile overlays and validates bounded paths, branch names and templates; `internal/discovery` scans configured source roots, excluding workspace roots, with a per-profile cache managed by the service. Source repositories are linked, not owned. Only explicitly owned worktrees can be removed.
- `internal/drivers/worktrunk` is the `wt`/Git boundary behind an injectable `Adapter`. It verifies Git registration, source identity, branch and all staged/unstaged/untracked/ignored files before removal; cleanup also rejects unrecognized workspace files. Removal runs in the foreground without force, preserves branches, and does not skip Worktrunk hooks. The adapter probes `wt switch --path` support and otherwise uses an invocation-local `--config-set worktree-path=...` override.

MVP 1 is offline and local-only; TUIOS, issue trackers, generic resources and remote automation in `spec/WORKDRIVE_SPEC.md` are future scope. See `README.md` for configuration and usage, and `docs/MVP1_CHECKLIST.md` for tested acceptance cases and known deviations.
