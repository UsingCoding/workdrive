# MVP 1 verification — 2026-11-13

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

## Verification commands

- `mise run build` — passed.
- `mise run lint` (format check + `go vet ./...`) — passed.
- `mise run test` (`go test ./...`) — passed.
- `go test -race ./internal/app ./internal/tui` — passed.
- Gated real Worktrunk test with installed `wt v0.68.0` and disposable repositories — passed; verified invocation-local path override and foreground branch-preserving removal.
- CLI `--help`, `config validate`, empty `list --json`, non-TTY refusal tests — passed.

## Scope decisions and deviations

- Installed Worktrunk v0.68.0 lacks the spec's example `switch --path`; the adapter probes support and uses the documented per-command `--config-set worktree-path=...` fallback. It verifies the registered destination before recording success.
- There is no standalone `rollback` command. Explicit, interactive `cleanup KEY` supplies a safety-preflighted rollback path for an interrupted create **after a manifest exists**; if creation stopped before the first manifest, inspect the durable intent and use `resume KEY` first. Unknown or dirty data blocks deletion.
- The dashboard shows **unknown** health until Git inspection rather than inferring healthy from a manifest. `inspect` and `refresh` perform actual verification. A historical healthy cache is not presented as live health.
- Terminal-driven wizard and dashboard were model-tested but not manually exercised in a real interactive terminal; the program's CLI build/help and noninteractive behavior were smoke-tested. The <200 ms dashboard rendering target was not benchmarked.
- The TUI pins Bubble Tea v1.3.10 (the available compatible dependency set); a Bubble Tea v2 migration was not validated. This differs from the specification's v2-compatible API preference.
- The shared `--verbose` flag is accepted but does not currently enable additional diagnostic output. `--no-color` is effective by default because the MVP 1 TUI emits no color escape sequences.
- Worktrunk removal still uses enabled upstream hooks. A blocking hook leaves recoverable state; hooks are not automatically skipped. No force flags are used.
