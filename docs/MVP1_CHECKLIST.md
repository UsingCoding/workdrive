# MVP 1 verification

The original core verification below was recorded on 2026-11-13. Current-shell navigation and completion coverage was added on 2026-11-15; its verification status is recorded separately below, not implied by the earlier passes.

Specification: [`spec/WORKDRIVE_SPEC.md`](../spec/WORKDRIVE_SPEC.md), section 1.11. MVP 2 and MVP 3 integrations are intentionally absent.

| Acceptance criterion | Evidence |
|---|---|
| Non-issue key, zero repositories | `TestZeroRepoWorkspaceLifecycle`, wizard model test |
| Two repositories, independent Git worktrees, editable/prefilled TUI | `TestFakeWorktrunkExecutableEndToEnd`, `TestWizardSelectsTwoRepositoriesAndEditsBranchBaseDestination`, `TestWizardPrefillAndAmbiguousNames`; actual terminal wizard not manually exercised |
| Multi-root nested and `.git`-file discovery, exclusions, duplicate names | `TestScanNestedGitFilesDuplicatesAndExclusions`, `TestScanDepthAndGlobExclusions`, `TestRepositoryDiscoveryCacheAndManualRefresh` |
| Add third repository without disturbing two | `TestFakeWorktrunkExecutableEndToEnd`, `TestAddAndUnknownDataBlocksCleanup` |
| Park/switch preserve working files and worktrees | `TestFakeWorktrunkExecutableEndToEnd`, `TestZeroRepoWorkspaceLifecycle` |
| Reject traversal, symlinks and directory collisions | `TestRestrictedTemplatesAndPaths`, `TestRejectPathAndKeyCollisions`, `TestWriteRejectsSymlinkedMetadata` |
| Refuse risky cleanup without force | `TestRemovalBlocksDirtyAndIgnoredFiles`, `TestNestedUnknownFileBlocksBeforeWorktreeRemoval`, `TestBlockedCleanupCannotBeApproved`; individual staged/unstaged/untracked/ignored cases covered in adapter tests |
| Clean cleanup preserves branches and original repositories | `TestFakeWorktrunkExecutableEndToEnd`, gated `TestRealWorktrunk` |
| Interrupted create/cleanup inspectable and resumable | `TestInterruptedCreateResumesOnlyMissingWorktree`, `TestInterruptedCleanupResumes`, `TestResumeBeforeFirstManifestWrite`, `TestJournalResumeAndOrderedCompletion` |
| `switch --print-path` absolute path only | `TestPrintPathOnlyAndNoninteractiveSafety` |
| Temporary Git repos, fake `wt`, gated real `wt` | App and adapter integration tests; `WORKDRIVE_REAL_WT=1 go test ./internal/drivers/worktrunk -run TestRealWorktrunk` |

## Original core verification commands (2026-11-13)

- `mise run build` — passed.
- `mise run lint` (format check + `go vet ./...`) — passed.
- `mise run test` (`go test ./...`) — passed.
- `go test -race ./internal/app ./internal/tui` — passed.
- Gated real Worktrunk test with installed `wt v0.68.0` and disposable repositories — passed; verified invocation-local path override and foreground branch-preserving removal.
- CLI `--help`, `config validate`, empty `list --json`, non-TTY refusal tests — passed.
- `mise run test:e2e` — passed with Docker, Worktrunk v0.81.0, and Git satisfying Worktrunk's 2.43.0 minimum.

## Docker CLI end-to-end suite

`mise run test:e2e` builds a single-stage Linux image with the actual CLI, OS-provided Git, bash/zsh, and mise-managed Go and Worktrunk, then drives the wizard, dashboard, shell navigation and confirmations through a real PTY. The suite is opt-in locally and is wired to a dedicated Ubuntu CI job. Existing lifecycle scenarios cover zero-repo creation/cancellation, real worktree creation/add/remove/cleanup, park/switch, dirty/ignored/unknown-data blockers, registry refresh, and interrupted create/resume.

## Current-shell navigation and completion (2026-11-15)

The added Docker test definitions are:

| Contract | E2E evidence |
| --- | --- |
| Same original shell PID and changed cwd; per-shell previous-workspace history | `TestShellNavigationEndToEnd` for bash/zsh, including A→B→`-`, recorded repository moves, manual/outside cwd, print-path history preservation, child-shell isolation, literal `switch -- -`. |
| Picker/dashboard Enter exits then navigates; terminal restored; embedded `n` stays in dashboard | `TestShellPickerDashboardEndToEnd` for bash/zsh, comparing terminal settings and cwd. |
| `new --switch` uses edited result and navigates only after full-success Enter | `TestShellNewSwitchEndToEnd`; canceled review and successful-result q/Esc/Ctrl-C do not navigate. |
| Explicit `--shell` starts a nested shell and leaves parent cwd unchanged | `TestExplicitNestedShellEndToEnd` for bash/zsh. |
| No integration or invalid recorded selection cannot activate a parked workspace; exact print-path output | `TestCLIEndToEnd/worktrees-add-park-remove-cleanup`. |
| Context-aware read-only Cobra completion | `TestCompletionEndToEnd`, including raw protocol/directives, keys, sources/recorded repos, profiles/state/shell/config/base, no filename fallback for free text, fixture-byte snapshots, absent state and malformed config. |

Current-checkout Docker verification: **in progress**; the historical E2E pass above does not certify these new cases. Protocol validation, handoff safety, lifecycle races and other focused edge cases remain in the default Go suite; this checklist does not claim they were all driven through Docker.

## Scope decisions and deviations

- Shell integration temporarily owns INT cancellation while a wrapped command runs. It cleans up and restores the user's pre-existing trap for subsequent signals, but does not replay that handler for the intercepted Ctrl-C event. Prompt hooks are not replaced. SIGKILL cannot guarantee cleanup.
- Installed Worktrunk v0.68.0 lacks the spec's example `switch --path`; the adapter probes support and uses the documented per-command `--config-set worktree-path=...` fallback. It verifies the registered destination before recording success.
- There is no standalone `rollback` command. Explicit, interactive `cleanup KEY` supplies a safety-preflighted rollback path for an interrupted create **after a manifest exists**; if creation stopped before the first manifest, inspect the durable intent and use `resume KEY` first. Unknown or dirty data blocks deletion.
- The dashboard shows **unknown** health until Git inspection rather than inferring healthy from a manifest. `inspect` and `refresh` perform actual verification. A historical healthy cache is not presented as live health.
- The wizard/dashboard have model coverage and automated real-PTY Docker scenarios, not a recorded manual terminal acceptance session. The <200 ms dashboard rendering target was not benchmarked.
- The TUI pins Bubble Tea v1.3.10 (the available compatible dependency set); a Bubble Tea v2 migration was not validated. This differs from the specification's v2-compatible API preference.
- The shared `--verbose` flag is accepted but does not currently enable additional diagnostic output. `--no-color` disables the TUI's styled colors.
- Worktrunk removal still uses enabled upstream hooks. A blocking hook leaves recoverable state; hooks are not automatically skipped. No force flags are used.
