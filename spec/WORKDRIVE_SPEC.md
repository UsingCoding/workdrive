# Workdrive — Product & Technical Specification

**Status:** Implementation specification, v1.0  
**Date:** 2026-11-12  
**Language:** Go  
**Tooling:** mise  
**TUI:** Bubble Tea (Charm ecosystem)  
**Primary platforms:** macOS and Linux  
**Implementation plan:** MVP 1 → MVP 2 → MVP 3, in order

> **Instructions for a coding agent:** Treat the definitions, lifecycle rules, safety invariants, CLI contract, and acceptance criteria in this document as normative. Build and test one MVP at a time. Do not implement future-MVP features as hard dependencies of an earlier MVP. If a nonessential detail is unspecified, choose the smallest maintainable solution, document the decision, and preserve compatibility with subsequent MVPs. No network service or background daemon is required for Workdrive itself.

---

## 0. Product vision and shared requirements

### 0.1 Problem

Starting a development task currently means manually creating a workspace, finding local Git repositories, creating issue-specific worktrees via Worktrunk, creating a TUIOS session rooted at the workspace, and launching Claude. Later, the session may be discarded while worktrees and the workspace persist. Switching back should recreate the session automatically. Finished workspaces should be safely cleaned up. External services such as YouTrack should provide titles/status badges but **must not be hardcoded into Workdrive**.

### 0.2 Outcome

Workdrive is a local-first **workspace lifecycle manager** with built-in filesystem/repository/Worktrunk functionality and a declarative, script-backed external resource system. The TUI is the primary user interface; the CLI supports explicit commands and prepopulating interactive workflows.

The target end-to-end flow (available after MVP 3):

1. `workdrive new PLATFORM-1425 --repo auth-service --repo user-service` opens a prefilled TUI wizard.
2. Workdrive creates `~/projects/workspaces/PLATFORM-1425/` and two worktrees from already-local source repositories.
3. Workdrive attaches configured default resources, including an optional script-managed TUIOS session and optional YouTrack reference.
4. `workdrive switch PLATFORM-1425` inspects the terminal-session resource, recreates it if missing, then switches/attaches; the user manually runs `claude`.
5. `workdrive resource add ...` / `workdrive add repo ...` add more resources or worktrees later.
6. `workdrive park PLATFORM-1425` removes the ephemeral session only; it leaves the directory, branches, worktrees and issue reference unchanged.
7. `workdrive cleanup PLATFORM-1425` previews all effects, blocks on unsafe changes, deletes owned worktrees and owned ephemeral resources, detaches external references, and removes the workspace directory and registry entry.

### 0.3 Guiding invariants

- Workspaces, not issue IDs, are the top-level entity. A workspace has a stable generated UUID/ULID, a user-visible unique `key`, a `title`, a `root/path`, and a `profile`.
- An issue ID is **optional** and is not a built-in domain concept. Any workspace key may be entered manually; workspace keys can also be arbitrary labels such as `scratch-api`.
- Each workspace contains **zero or more resources**, each with its own identity, driver, inputs, lifecycle, ownership and status.
- Source repositories are **linked**, never owned or deleted by Workdrive. Worktrees that Workdrive creates inside a workspace are **owned**; their Git branches are independently managed and **preserved on cleanup by default**.
- Cleanup must not silently discard uncommitted changes, untracked files, ignored data, or unmerged work. Destructive operations require an explicit preflight and user approval; unsafe paths are never force-deleted by default.
- `park` preserves all persistent resources. For terminal sessions it destroys the session (not merely detaches), potentially ending running processes. Warn the user.
- `switch` is idempotent. It must not recreate existing worktrees or rerun workspace setup. With TUIOS configured, it recreates only the missing ephemeral session before opening it.
- Resource providers are **optional**. Workdrive must work with no YouTrack, network access, TUIOS, or custom scripts.
- External service integrations (YouTrack, GitLab, Claude, TUIOS) must not require application-specific code in the core.
- Persist state locally; don't run a Workdrive daemon; don't put credentials into manifests, logs, or SQLite.

### 0.4 Definitions

| Term | Meaning |
|---|---|
| Workspace | Persistent directory and identity with a manifest and resource collection |
| Resource type | Configured behavior schema (driver, supported actions, fields, lifecycle defaults) |
| Resource instance | A particular resource attached to one workspace, with inputs and opaque persisted state |
| Built-in driver | Native Go implementation, initially filesystem/repository discovery/Worktrunk |
| Command driver | Generic driver executing user-configured commands under a versioned JSON contract |
| Linked | External entity Workdrive references but does not own; cleanup detaches metadata only |
| Owned | Entity provisioned and permitted to be removed by Workdrive under safety policy |
| Parked | Workspace whose configured park actions have completed; persistent data remains |
| Reconciliation | Inspection and reporting of divergence between recorded and actual state |
| Profile | Config overlay controlling roots/default resource types and workflow policy |

### 0.5 Technology / repository layout

Prefer standard library where reasonable. Suggested dependencies: Cobra for CLI, Bubble Tea v2-compatible APIs and Bubbles/Lip Gloss for TUI (pin compatible versions), a TOML parser, a pure-Go SQLite driver, and standard `os/exec` for process execution. No requirement for an embedded web server, ORM or Go plugin loading.

```text
workdrive/
├── cmd/workdrive/main.go
├── internal/
│   ├── app/                   # command use-cases, workflows
│   ├── domain/                # workspace/resource/status/policies
│   ├── config/                # config load, validation, layering
│   ├── registry/              # SQLite index, scanning/rebuild
│   ├── manifest/              # per-workspace TOML read/write
│   ├── discovery/             # local Git repository scanner
│   ├── drivers/worktrunk/     # native adapter
│   ├── drivers/command/       # script protocol (MVP 2)
│   ├── lifecycle/             # planner, journal, locks, reconciliation
│   ├── tui/                   # dashboard, wizard, pickers, details
│   └── cli/                   # Cobra commands and flags
├── examples/
│   ├── config.toml
│   ├── resources/tuios.toml
│   ├── resources/youtrack.toml
│   └── scripts/
├── docs/
├── .mise.toml
├── go.mod
└── README.md
```

Do not rigidly enforce this layout if smaller packages give clearer boundaries. Domain types must not import Bubble Tea, Cobra, SQLite or Worktrunk-specific packages.

---

# MVP 1 — Core Workspace & Worktree Management

## 1.1 Goal / deliverable

A usable offline CLI/TUI capable of creating, finding, inspecting, entering, parking and safely removing workspaces with multiple Git worktrees. MVP 1 has **no configurable external resources, YouTrack integration, or TUIOS integration**. Its lifecycle abstractions and metadata format must permit adding those in MVP 2 without breaking existing workspaces.

## 1.2 In scope

- CLI executable `workdrive`, with optional `wd` symlink/alias documented but not required.
- `workdrive` opens the Bubble Tea dashboard.
- `workdrive new [key] [--name TITLE] [--repo NAME_OR_PATH ...] [--profile PROFILE] [--base REF]` **always opens the creation wizard**; args prefill fields but users can edit them.
- Dashboard: list/search/filter workspaces, show key/title/parked state/worktree count/health, open details; keyboard navigation.
- Wizard with steps: (1) key/name/path, (2) repository multi-select, (3) branch/base/path per repo, (4) review and explicit confirm.
- Configurable workspace root, profiles, repository source roots, scan max-depth/exclusions, branch/path templates, and Worktrunk executable.
- Discover local Git repositories efficiently, including nested repositories and `.git` files, with canonical-path deduplication and duplicate-name disambiguation.
- Create worktrees into the workspace directory via built-in Worktrunk adapter; add/remove individual worktrees later.
- Persistent per-workspace manifest and local registry index.
- Workspace list, inspect, switch, park, cleanup; idempotent operations and safe interruption recovery.
- Structured errors and actionable messages; no remote access required.
- Mise configuration for dev tasks and dependency versions.

## 1.3 CLI contract

```text
workdrive                                  # dashboard
workdrive new [key] [--name <title>] [--repo <name-or-path> ...]
              [--profile <name>] [--base <ref>]
workdrive list [--json] [--state active|parked|all]
workdrive inspect [key] [--json]           # key omitted => picker
workdrive switch [key] [--print-path]      # key omitted => picker
workdrive park [key]                       # key omitted => picker
workdrive add repo [key] [--repo <name-or-path> ...]
workdrive remove repo [key] [--repo <name-or-path>]
workdrive cleanup [key]                    # preflight, preview, approval
workdrive refresh [key]                    # scan and verify local state
workdrive config validate
```

Shared flags: `--config PATH`, `--profile NAME` where meaningful, `--json` for machine-readable read-only results, `--no-color`, `--verbose`. Noninteractive commands requiring a selection must fail clearly when no TTY is available. MVP 1 does not require headless workspace creation. `cleanup` without a TTY must refuse unless a future explicit and safe approval mechanism is implemented.

**Shell caveat:** A subprocess cannot change its parent shell's current directory. In MVP 1, `switch` should launch an interactive shell with cwd set to the workspace (without changing the caller's shell), or `--print-path` can print an absolute path for shell integration. Document both behaviors; don't claim it can `cd` the parent shell. In MVP 2, a configured `open` resource (TUIOS) can become the preferred switch destination.

## 1.4 Workspace key / path rules

- Unique key (case-sensitive or insensitive choice must be documented; choose case-sensitive within canonical registry).
- Keys intended for filesystem directory names: reject empty, `.`/`..`, path separators, control characters, leading/trailing whitespace, and reserved metadata names. Allow letters, numbers, `_`, `-`, `.` excluding pathological values. A user-friendly display title may contain spaces.
- Canonicalize roots and destinations; refuse workspace creation outside configured root by `..`, absolute template output, or symlink traversal.
- Do not overwrite an existing nonempty directory or adopt existing worktrees implicitly.
- Resolve duplicate repository labels via stable aliases or selection by absolute source path.

## 1.5 Repository discovery

Configuration sources consist of `name`, `path`, `max_depth`, `exclude` patterns. Explicitly exclude configured workspace roots by default to avoid recursing into generated worktrees. Discovery finds source Git repositories, not worktrees already registered in workspaces. Cache source paths for TUI responsiveness; provide refresh. Handle missing roots and permission errors by warning, not by failing the entire wizard. On ambiguous `--repo NAME`, show choices in TTY; reject ambiguity when noninteractive.

Store canonical source path per selected repo, not just display name. A repo source may have a dot-git directory or a dot-git file. Validate using Git commands, not just presence of `.git`.

## 1.6 Worktrunk adapter

Keep Worktrunk calls centralized and testable behind a Go interface. The user must already have `wt` installed. Use `wt -C <source> switch --create <branch> --path <absolute-destination>` (with `--base` when provided) when the branch does not yet exist. For an existing, eligible branch, use `wt -C <source> switch <branch> --path <destination>` as appropriate; **first verify branch/worktree ownership and path to avoid stealing or silently adopting a different checkout**. Prefer structured Worktrunk output where supported.

Cleanup invokes foreground removal by **worktree path**, with explicit branch preservation, e.g.:

```bash
wt -C /path/to/source remove \
  --no-delete-branch --foreground --format json \
  /path/to/workspace/repo
```

Do **not** use `--force` or `--force-delete` by default. Handle `wt remove` hooks and background behavior explicitly: foreground completion is mandatory before removing the workspace directory or marking the operation successful. If a configured Worktrunk hook blocks, report its error and keep recoverable state. Optionally support `use_hooks` in the adapter configuration, with explicit documentation; do not silently disable upstream hooks.

Before removal verify:

1. The source Git repository still exists or report a recoverable blocker.
2. The actual worktree is registered for the expected source and at the recorded canonical path.
3. The worktree is clean including staged, unstaged and untracked changes (use Git porcelain, with robust handling of paths); ignored files and unrelated workspace files are treated as potentially valuable, not disposable.
4. The branch is preserved unless an explicitly designed, separately confirmed branch-deletion action is added later.
5. No unknown files or directories remain under workspace root after removing expected worktrees, except Workdrive metadata. Unknown files block final directory deletion until individually reviewed.

If the same Git branch is already checked out elsewhere, do not forcibly move it; present a conflict to resolve.

## 1.7 Lifecycle

```text
new:      absent -> creating -> active
park:     active -> parked         (MVP 1: metadata state only)
switch:   parked -> active         (MVP 1: launch shell / print path)
cleanup:  active|parked -> cleaning -> removed
failure:  creating|cleaning -> attention_required (recoverable)
```

Keep `actual_status` (healthy/missing/conflict/error) independent of workspace `lifecycle_state`. Never claim a workspace is healthy just because a manifest exists. `park` in MVP 1 does not kill processes, Git worktrees, or external sessions because those integrations do not exist yet. Persist state transitions atomically.

**Create:** validate configuration and all paths, plan/preview, acquire per-workspace lock, record intended steps, create directory and metadata, create worktrees in sequence, mark complete. If partial failure occurs, retain an operation journal and offer retry/resume/explicit rollback; don't delete data that may have been modified.

**Cleanup:** perform *all* preflight checks before any destructive step; show detailed itemized plan and blockers; on approval record intent; remove worktrees sequentially; confirm all removed; delete only recognized metadata and otherwise-empty workspace; remove registry entry last. If a later step fails, leave manifest/registry in `attention_required` and make the operation resumable.

**Switch:** verify directory exists, mark active, then open a shell in the directory (or print path). `switch` never creates/moves worktrees; missing worktrees trigger warnings and an optional repair action.

## 1.8 Persistence

Default paths (respect XDG env vars with macOS-compatible defaults):

```text
~/.config/workdrive/config.toml
~/.config/workdrive/profiles/*.toml
~/.local/state/workdrive/registry.db
~/.local/state/workdrive/operations/<id>.json
~/projects/workspaces/<key>/.workdrive/workspace.toml
```

The **manifest is authoritative**, SQLite is a rebuildable index/cache. At startup or refresh, detect entries missing manifests and manifests missing from the registry. A workspace manifest must contain schema version, id, key, name, absolute root path, profile, lifecycle state, timestamps, and resource instances. Store paths as canonical absolute paths (or clearly specified workspace-relative paths only for owned worktree paths), not unresolved `~`.

Minimal manifestation example:

```toml
schema_version = 1
id = "01JEXAMPLE1425"
key = "PLATFORM-1425"
name = "Auth token refresh"
path = "/home/dev/projects/workspaces/PLATFORM-1425"
profile = "development"
state = "active"

[[resources]]
id = "auth-service"
type = "worktree"
driver = "worktrunk"
ownership = "owned"
source = "/home/dev/projects/auth-service"
path = "/home/dev/projects/workspaces/PLATFORM-1425/auth-service"
branch = "PLATFORM-1425"
```

Atomic manifest writes via temp file + rename; guard concurrent process access with per-workspace advisory lock. Registry indexes must never replace ownership validation when deleting files.

## 1.9 MVP 1 configuration schema (illustrative, normative keys)

```toml
schema_version = 1

[workspace]
root = "~/projects/workspaces"
default_profile = "development"
directory_template = "{{ .Workspace.Key }}"

[repositories]
refresh_on_start = false

[[repositories.sources]]
name = "projects"
path = "~/projects"
max_depth = 3
exclude = ["workspaces/**", "**/node_modules/**", "**/.cache/**"]

[worktree]
driver = "worktrunk"
executable = "wt"
branch_template = "{{ .Workspace.Key }}"
path_template = "{{ .Workspace.Path }}/{{ .Repo.Name }}"
base_ref = "default"
delete_branch_on_cleanup = false

[ui]
show_hidden = false

[profiles.development]
workspace_root = "~/projects/workspaces"
```

Template variables are explicitly scoped to `Workspace.Key`, `Workspace.Name`, `Workspace.Path`, and `Repo.Name` (plus `Repo.Path` where safe). Validate output paths and Git branch validity after expansion. Template rendering must not execute arbitrary code. Profile overrides are deterministic and validated.

## 1.10 MVP 1 TUI

- **Dashboard:** fuzzy filter by key/title/path; active/parked toggle; counts; health indicators; current selection; help bar.
- **New wizard:** identity → repositories (multi-select searchable list) → worktree branch/base/destinations → review and apply. Pre-populate CLI args. Back/forward, cancel and explicit confirmation. Never provision until confirmation.
- **Workspace detail:** key/title/path/state; source repo, worktree path, branch, status and errors; actions for switch, add/remove repo, park and cleanup.
- **Cleanup preview:** clear list of resource deletions, preserved branches and blockers; no approve button when unsafe blockers exist.
- **Async operations:** UI stays responsive to scanning/provisioning; long commands show progress and cancellability without corruption.

Suggested keys: `n` new, `Enter` switch, `a` add repo, `p` park, `x` cleanup, `r` refresh, `/` search, `?` help, `q` quit. Document actual mappings.

## 1.11 MVP 1 acceptance tests

- [ ] Create a workspace with no issue identifier and zero repositories.
- [ ] Create a workspace with two selected local repositories via TUI and through prefilled flags; each destination is an independently functional Git worktree.
- [ ] Discover repos from multiple roots including a nested repo and `.git` file; filter excluded workspace roots; handle duplicate names.
- [ ] Add a third repository after creation without disturbing the first two.
- [ ] Park and switch a workspace; all working files and worktrees remain unchanged.
- [ ] Refuse path traversal and existing-directory collisions.
- [ ] Refuse cleanup of staged, unstaged, untracked, ignored, unknown, or otherwise risky data without explicit review; no automatic `--force`.
- [ ] Cleanup clean owned worktrees, preserve branches and all original source repositories, and remove the workspace and registry entry.
- [ ] Simulate a failure halfway through create/cleanup and prove the operation can be inspected/resumed safely.
- [ ] `switch --print-path` prints an absolute path only; no misleading parent-shell `cd` behavior.
- [ ] Tests use temporary Git repos and a fake Worktrunk executable; real Worktrunk integration is covered by a gated integration test.

**MVP 1 completion gate:** A user can reliably manage local task directories and their worktrees without any external resource configuration.

---

# MVP 2 — Generic Script-Backed Resource System & TUIOS

## 2.1 Goal / deliverable

Introduce a **provider-neutral resource model** and generic command driver. Enable defining external resource types, lifecycle behavior, fields, and presentation entirely in TOML and scripts. Deliver TUIOS as the reference external integration, with no TUIOS-specific code in the core.

## 2.2 In scope

- Generic resource types and instances, stable IDs, input values, opaque provider state, ownership, dependency metadata (simple workspace dependency initially).
- Configurable resource definition files under `~/.config/workdrive/resources/*.toml`.
- Generic command driver with actions `create`, `inspect`, `open`, `park`, `resume`, `destroy`, plus optional `validate` and `plan`.
- Versioned JSON stdin/stdout contract, structured action errors, process timeouts, cancellation, allowed env vars, terminal mode for interactive `open`.
- Lifecycle action dispatch + policies; actions may be omitted and should not be guessed.
- Declarative dynamic fields for the attach/create TUI forms.
- CLI attach/detach/inspect/run actions and per-workspace resource inventory.
- Async inspection, cached badges and freshness/error indicators; no blocking network/tool calls while rendering dashboard.
- Script-managed TUIOS integration: detached session creation, inspect, park/kill, resume, open/switch, cleanup.
- Integration scripts and config examples with mocked tests.

## 2.3 Domain model

```text
ResourceType:
  name, label, driver, ownership_default, allow_multiple
  actions{}, fields[], lifecycle{}, status_cache_ttl
  env_allowlist[], config_version
ResourceInstance:
  id, type, ownership (owned|linked|adopted)
  inputs{}, state{} (opaque), depends_on[]
  provisioned_by_workdrive (boolean), recorded_definition_fingerprint
  last_inspection_cache (in SQLite only)
ResourceStatus:
  presence (present|absent|unknown), health (ok|warning|error|unknown)
  label, tone (neutral|info|success|warning|error)
  title, subtitle, url?, checked_at, stale
```

`adopted` is treated as linked for deletion until the user explicitly chooses ownership transfer. No automatic destructive action may operate on `linked` or untransferred `adopted` resources. Type definitions may be absent later; preserve instances and show `provider_missing` instead of deleting them.

## 2.4 Lifecycle semantics

- **`create`**: provision an `owned` resource or validate/attach a `linked` reference; definition controls actual script behavior. Persist returned opaque state.
- **`inspect`**: return current presence, health and display metadata; never mutate external entities.
- **`open`**: interactive action; may replace the current client session or attach a terminal. Doesn't implicitly create external entities; orchestration can `ensure` first.
- **`park`**: invoke configured actions for resources whose `on_park` requires it, then mark workspace parked only if all mandatory actions completed.
- **`resume`**: reestablish missing/parked ephemeral resources during `switch`; don't recreate persistent worktrees.
- **`destroy`**: remove an owned resource during cleanup if policy permits.
- **`detach`**: remove a local instance/reference from the manifest, not the remote entity.
- **`validate`**: optional provider-specific prerequisites, inputs and credentials.
- **`plan`**: optional preview with side effects and blockers; a destructive custom provider without an explicit plan is classified as unsafe and must require extra confirmation or be blocked by policy.
- **`ensure`**: core operation: inspect; if present do nothing; if absent call `resume` or `create`; if unknown, stop and report uncertainty rather than guessing.

`on_create = create|attach|none`, `on_park = park|keep`, `on_switch = ensure_open|open|refresh|none`, `on_cleanup = destroy|detach|keep`. Lifecycle config is declarative, **not a comma-separated string**. A missing action does not become an implicit shell command. On cleanup, linked resources are always detached (unless user explicitly elects `keep` metadata while workspace persists); they must not be remotely deleted.

The `park` state belongs to the workspace. Resources report separate actual presence. Example: after parking, `tuios` instance remains in manifest as desired association even though the daemon session is absent.

## 2.5 Script protocol `workdrive/v1`

### Invocation

- JSON-mode actions: run command as an **argument array** (`["/abs/script", "arg"]`), send a single JSON object over stdin, require one JSON object on stdout, send logs to stderr. Never use implicit `/bin/sh -c`.
- Terminal-mode actions: suspend TUI rendering, inherit stdin/stdout/stderr as a terminal, pass the same request in a secure `0600` temporary JSON file and its path in `WORKDRIVE_CONTEXT_FILE`; JSON stdout is not required. Restore TUI on return when possible.
- Resolve relative configured command paths against the config directory. Expand environment variables only from explicit allowed list. Inject only nonsecret context in `WORKDRIVE_*` environment variables; secrets remain inherited env vars selected by allowlist, and are redacted from logs.
- Set `WORKDRIVE_API_VERSION=workdrive/v1`, action timeout per resource and configurable size limits on stdout/stderr (e.g. 1 MiB response cap). Kill entire child process group when canceled on macOS/Linux.
- Atomic state write only after successful response and expected postconditions. Include operation ID for correlating logs; retries must be safe.

### Request example

```json
{
  "api_version": "workdrive/v1",
  "operation_id": "01JEXAMPLEOP",
  "action": "inspect",
  "workspace": {
    "id": "01JEXAMPLE1425",
    "key": "PLATFORM-1425",
    "name": "Auth token refresh",
    "path": "/home/dev/projects/workspaces/PLATFORM-1425",
    "lifecycle": "parked"
  },
  "resource": {
    "id": "terminal",
    "type": "tuios",
    "ownership": "owned",
    "inputs": {"server": "work", "session": "wd-PLATFORM-1425"},
    "state": {}
  }
}
```

### JSON response envelopes

The response must contain `api_version`. Success for lifecycle actions:

```json
{
  "api_version": "workdrive/v1",
  "ok": true,
  "state": {"session_id": "optional-remote-id"},
  "message": "Session created"
}
```

Success for `inspect`:

```json
{
  "api_version": "workdrive/v1",
  "ok": true,
  "presence": "present",
  "health": "ok",
  "status": {"label": "Active", "tone": "info"},
  "display": {"title": "TUIOS session", "subtitle": "wd-PLATFORM-1425"},
  "state": {}
}
```

Unknown presence (e.g. offline daemon, remote API inaccessible) is **not** equivalent to `absent`. On error, prefer exit nonzero with a parseable error object; unknown failures are surfaced as `provider_error` with stderr sanitized:

```json
{
  "api_version": "workdrive/v1",
  "ok": false,
  "error": {"code": "unavailable", "message": "Cannot reach daemon", "retryable": true}
}
```

`validate`/`plan` may report `blockers` and `warnings` as arrays of typed objects. A no-op action need not emit state. Never persist provider-returned secrets into `state`; enforce an allowlist/secret scanning policy for examples and document that `state` is public local metadata.

### Optional action configuration

```toml
[resource_types.tuios.actions.open]
command = ["./scripts/tuios/open.sh"]
mode = "terminal"
timeout = "0s"             # only allowed for terminal actions
```

Shorthand string syntax is valid for an executable *path only*, not an unparsed shell command line, e.g. `park = "./scripts/tuios/park.sh"`. Empty string or missing key means no action. A list of arguments is used for commands that need parameters.

## 2.6 Resource definition schema / forms

```toml
# ~/.config/workdrive/resources/tuios.toml
[resource_types.tuios]
driver = "command"
label = "TUIOS Session"
ownership = "owned"
allow_multiple = false

[resource_types.tuios.actions]
create = "./scripts/tuios/create.sh"
inspect = "./scripts/tuios/inspect.sh"
park = "./scripts/tuios/park.sh"
resume = "./scripts/tuios/create.sh"
destroy = "./scripts/tuios/park.sh"

[resource_types.tuios.actions.open]
command = ["./scripts/tuios/open.sh"]
mode = "terminal"
timeout = "0s"

[resource_types.tuios.lifecycle]
on_create = "create"
on_park = "park"
on_switch = "ensure_open"
on_cleanup = "destroy"

[resource_types.tuios.status]
cache_ttl = "15s"

[[resource_types.tuios.fields]]
name = "server"
label = "TUIOS daemon"
type = "string"
default = "work"
required = true

[[resource_types.tuios.fields]]
name = "session"
label = "Session"
type = "string"
default_template = "wd-{{ .Workspace.Key }}"
required = true
```

Config allows `string`, `number`, `boolean`, `select` with static `options`, `path`, `secret` (not persistable as raw manifest value). Dynamic discovery via `search` is reserved for MVP 3. The UI validates required fields and type before calling scripts. `default_template` is evaluated against safe, documented template fields; no arbitrary template functions.

Profiles in MVP 2 can specify `default_resources` and `optional_resources`. A default `owned` resource is created during workspace provisioning; an optional resource is selected in the wizard. Resource instances can also be attached to an existing workspace. Profiles should not attempt to instantiate unknown types or types with invalid required fields.

## 2.7 TUIOS behavior and sample script requirements

Ship working, documented reference scripts for the configured TUIOS integration, using `tuios` executable and `jq` (or another script implementation; dependencies must be listed). The core must not contain TUIOS-specific logic. Support named daemon `--server work`, configurable daemon/session fields, and session path = workspace path.

Documented TUIOS actions:

- `create`: `tuios --server <server> new <session> --cwd <workspace-path> --detach` when absent; no duplicate creation when present.
- `inspect`: `tuios --server <server> ls --json`, match exact session name; account for saved/offline sessions and exit code `3` without incorrectly claiming an active daemon session.
- `park` and `destroy`: `tuios --server <server> kill-session <session>` if owned and present. This terminates shell processes and discards saved session state; do **not** use `detach`.
- `resume`: check/recreate a missing session.
- `open`: if inside a compatible TUIOS client, `tuios --server <server> switch-session --create --cwd <workspace-path> <session>`; from an outside terminal, attach (creating/ensuring first). Handle ambiguity with multiple clients and wrong-daemon context by reporting a clear error instead of silently nesting.

When a daemon is unreachable/unknown, do not automatically create a second conflicting session. For `switch`, perform `ensure` then `open` and mark workspace active only after success. Script must handle path/argument quoting safely.

Because `kill-session` destroys running terminal sessions, the park preview must explain that Claude/terminal processes in that session will stop. Worktree data and workspace files are not affected by the park action.

**Self-park / self-cleanup safety:** A `park` or `cleanup` command can be launched from a shell inside the very TUIOS session it intends to terminate. Workdrive must not naively kill the session and lose its controlling process before journaling completion. MVP 2 should detect this case, persist a pending operation, and either (a) hand execution to an independent controller process outside the target TUIOS session that can finish after the initiating client exits, or (b) require the user to switch to a different session/terminal and resume the parked operation. The first option is preferred for seamless UX, but the second is an acceptable explicit fallback for MVP 2. Never report success unless the independent controller confirms completion. Apply the same rule to cleanup.

## 2.8 MVP 2 CLI / TUI additions

```text
workdrive resource types
workdrive resource add <type> [workspace-key] [--set field=value ...]
workdrive resource remove <resource-id> [workspace-key]  # detach by default
workdrive resource inspect <resource-id> [workspace-key] [--json]
workdrive resource run <resource-id> <action> [workspace-key]
workdrive refresh [key]                                  # inspect resources
```

An `owned` instance must not be detached while its real external resource remains without an explicit `--orphan` acknowledgment through an interactive safety flow. `remove` should default to **detach only for linked resources**, but for owned resources ask whether to run destroy or leave the resource orphaned; never silently leak or delete.

TUI additions: resource picker/form, resource detail view, resource status columns/badges, freshness timestamp and stale/unavailable indicator, action-specific errors. `switch` chooses the profile's `switch_resource` when configured (TUIOS by default); otherwise retains MVP 1 shell behavior. Resource `inspect` runs asynchronously and is cached; a failing script must not crash the main dashboard.

## 2.9 MVP 2 acceptance tests

- [ ] Add a resource type solely through TOML + mock scripts, without recompiling Workdrive.
- [ ] Render a resource form from declarative field definitions; CLI `--set` prepopulates values.
- [ ] Create, inspect, park, resume, open and destroy a command-backed resource; persist opaque state safely.
- [ ] `park` leaves worktrees untouched and destroys only configured ephemeral owned resources.
- [ ] `switch` into a parked workspace recreates a missing TUIOS session exactly once and opens it; repeated switch is idempotent.
- [ ] Linked resource cleanup detaches without calling remote destroy.
- [ ] Missing/unknown provider or unknown presence yields visible warning, not accidental recreation or deletion.
- [ ] Malformed JSON, nonzero exit, timeout, cancellation and large output are handled with stable error codes and intact manifest.
- [ ] A failing `inspect` does not block workspace list; cached result is clearly marked stale.
- [ ] Terminal-mode `open` restores TUI on return; multiple-client/wrong-daemon errors are surfaced.
- [ ] No secrets appear in logs, manifests, output snapshots or SQLite fixtures.

**MVP 2 completion gate:** The original user workflow works end-to-end with local worktrees and a script-managed TUIOS session, without any application-specific resource logic in core code.

---

# MVP 3 — Advanced Extensibility, YouTrack Example & Diagnostics

## 3.1 Goal / deliverable

Make the command resource platform flexible enough for external issue trackers and future integrations: dynamic search, workspace status badges, custom actions, hooks, dependencies, richer profiles, imports, diagnostics, and scalable refresh. Deliver **YouTrack only as configuration plus scripts**, not as a built-in driver.

## 3.2 In scope

- Script-provided `search` action with query and structured results, used by TUI selectable fields.
- Optional `inspect_batch` action for refreshing multiple instances with one command; fallback to rate-limited `inspect`.
- Configurable, provider-neutral badges and secondary workspace metadata; status cache TTL/freshness/error states.
- YouTrack reference implementation with scripts for `create` (link/validate), `inspect`, `search`, `open` and optional `validate`.
- Custom resource actions displayed in detail view and invokable through CLI.
- Workspace lifecycle hooks `before_create`, `after_create`, `before_cleanup`, `after_cleanup` with blocking/nonblocking policy.
- Resource-instance dependencies, DAG validation and ordered provisioning/teardown.
- Support multiple instances of a resource type when allowed.
- Deterministic profile/config layering, overriding resource defaults, definition fingerprint and schema validation.
- `workdrive import <path> [--key ...]` with explicit ownership choices and safe adoption rules.
- `workdrive doctor`, registry rebuild, reconciliation and actionable repair previews.

## 3.3 Script-driven search contract

Request adds a `query` string, optional `limit` and cursor. Standard response:

```json
{
  "api_version": "workdrive/v1",
  "ok": true,
  "items": [
    {"id": "PLATFORM-1425", "label": "PLATFORM-1425", "description": "Fix token refresh", "values": {"issue_id": "PLATFORM-1425"}},
    {"id": "PLATFORM-1426", "label": "PLATFORM-1426", "description": "Update gateway routing", "values": {"issue_id": "PLATFORM-1426"}}
  ],
  "next_cursor": null
}
```

TUI behavior: debounce user input; show loading, empty results and provider errors; selecting an item fills declared resource fields from the returned `values`. Never hardcode what an issue ID means. CLI can always fall back to typed `--set issue_id=...` without search.

## 3.4 YouTrack as configuration only

The core knows no YouTrack endpoint, authentication method, state names or issue schema. The supplied example uses script calls to the YouTrack REST API. Its `create` action **attaches an existing issue** (validation/remote ID lookup), and never creates an actual YouTrack ticket. `destroy` is unset, and cleanup detaches local metadata. Credential names are provided in environment allowlists, not TOML values.

```toml
# ~/.config/workdrive/resources/youtrack.toml
[resource_types.youtrack]
driver = "command"
label = "YouTrack Issue"
ownership = "linked"
allow_multiple = true

[resource_types.youtrack.actions]
create = "./scripts/youtrack/create.sh"
inspect = "./scripts/youtrack/inspect.sh"
search = "./scripts/youtrack/search.sh"
open = "./scripts/youtrack/open.sh"
park = ""
resume = ""
destroy = ""

[resource_types.youtrack.lifecycle]
on_create = "create"
on_park = "keep"
on_switch = "refresh"
on_cleanup = "detach"

[resource_types.youtrack.status]
cache_ttl = "10m"

[resource_types.youtrack.environment]
pass = ["YOUTRACK_URL", "YOUTRACK_TOKEN"]

[[resource_types.youtrack.fields]]
name = "issue_id"
label = "Issue ID"
type = "string"
required = true
default_template = "{{ .Workspace.Key }}"
search_action = "search"
```

Example `inspect` response:

```json
{
  "api_version": "workdrive/v1",
  "ok": true,
  "presence": "present",
  "health": "ok",
  "status": {"label": "In Progress", "tone": "info", "resolved": false},
  "display": {
    "title": "Fix authentication token refresh",
    "subtitle": "PLATFORM-1425",
    "url": "https://youtrack.example/issue/PLATFORM-1425"
  },
  "state": {"remote_id": "2-1425"}
}
```

`resolved` is **optional presentation metadata**, not a workspace state transition. Resolution status comes from the script/API. A resolved issue can remain in an active workspace; neither parking nor cleanup runs automatically. On YouTrack network failure, show last cached status with stale indicator and permit all local operations. No UserTrack/YouTrack code paths in `internal/domain`, `internal/lifecycle` or TUI.

The YouTrack scripts should be documented reference implementations that use `curl`/`jq` (or an executable) and a local mock server for tests, with pagination for search and robust HTTP/auth/error handling. Do not require a real YouTrack account for the automated test suite.

## 3.5 Custom actions

```toml
[[resource_types.youtrack.custom_actions]]
name = "open-browser"
label = "Open issue in browser"
command = ["./scripts/youtrack/open.sh"]
mode = "terminal"
confirmation = "none"
```

Expose as `workdrive resource run <resource-id> <action> [workspace]`. Validate action names, prevent arbitrary commands from manifests, and require explicit confirmation for action definitions marked destructive.

## 3.6 Workspace hooks

```toml
[profiles.development.hooks]
before_create = "./hooks/before-create.sh"
after_create = "./hooks/after-create.sh"
before_cleanup = "./hooks/before-cleanup.sh"
after_cleanup = "./hooks/after-cleanup.sh"
```

Hooks use the script protocol with `resource = null` and `action = hook:<name>`. A `before_*` failure blocks the operation by default; `after_*` failures are recorded as warnings unless explicitly configured blocking. `after_cleanup` receives a snapshot of removed metadata since the workspace manifest may no longer exist. No hook should execute before a user approves a destructive cleanup, except pure `validate` or `plan` preflights. Hook timeouts, env allowlist and error reporting follow the command driver.

## 3.7 Dependencies and execution order

Instances may declare `depends_on = ["resource-id"]`; implicit workspace root dependency always exists. Validate DAG and reject cycles before mutating anything. Create/restore: topological order. Destroy: reverse topological order. Linked reference deletion from manifest is not a remote destroy. If dependencies cannot be satisfied, show blockers before operation begins. For example, a development server may depend on an `auth-service` worktree, while TUIOS depends only on the workspace path.

## 3.8 Profiles, config layering and versioning

Merge order: compiled safe defaults < global config < selected profile < instance inputs/CLI overrides (only for permitted fields). Resource definitions loaded from `resources/*.toml` are globally named; conflicting definitions fail validation rather than silently overriding. Resolve script paths relative to config directory; reject path traversal in includes, detect malformed schema and unknown lifecycle values.

Store a definition fingerprint/version with each resource instance. If a definition changes incompatibly, continue to show persisted data but block unsafe lifecycle actions until user reviews and reconciles. Provide an explicit config validation command. Schema migrations must be additive and tested; never silently reinterpret older policies.

## 3.9 Import, doctor and reconcile

- `workdrive import <dir> --key <key>`: wizard previews directory and any detected worktrees, offers `linked`/`adopted` classification, and writes manifest only after approval. Never assume ownership of existing foreign worktrees or delete them on cleanup without transfer.
- `workdrive doctor`: report config errors, missing `git`/`wt`, Worktrunk version/capabilities, missing scripts, missing allowed env vars (names only), unexecutable scripts, malformed manifests, invalid dependency graphs, duplicate keys, stale registry entries and resource health. Do not print secrets.
- `workdrive registry rebuild`: reindex workspace manifests under configured roots and report conflicts. Never remove directories during rebuild.
- `workdrive reconcile [key]`: inspect presence vs desired state and generate a human-readable repair plan; no destructive repair without explicit approval.

## 3.10 MVP 3 acceptance tests

- [ ] Add YouTrack using only a TOML definition plus scripts, with no Go changes; inspect/status/search work against a mock YouTrack server.
- [ ] A YouTrack issue may be omitted, added later, attached multiple times when configured, and detached without deleting the remote issue.
- [ ] Workspace badges show remotely supplied labels and tones, including cached/stale/unavailable states; a `Fixed` issue does not auto-clean the workspace.
- [ ] Dynamic `search` fills resource fields; errors do not block manual field entry.
- [ ] Custom resource actions run only defined executable commands; destructive ones require confirmation.
- [ ] Hooks obey failure policy and lifecycle ordering; before-cleanup can block before any deletion.
- [ ] Dependency cycles are rejected; resource create and destroy order is tested.
- [ ] Multiple instances of the same type are distinguishable by ID; deleting one doesn't affect another.
- [ ] Import an existing workspace without silently assuming ownership of data; registry rebuild preserves manifests.
- [ ] `doctor` gives actionable diagnostics when TUIOS/YouTrack scripts or `wt` are unavailable, without exposing secrets.
- [ ] Configuration changes don't silently cause deletion of resources that were created under an older definition.

**MVP 3 completion gate:** External trackers/resources can be integrated or replaced by changing config/scripts; the main dashboard can display their status without application-specific code.

---

## 3.11 Complete intended user configuration (after MVP 3)

Combine the MVP 1 global config with these profile settings and the two script resource definitions above. This example is the desired everyday workflow, not a requirement that all integrations be present in MVP 1.

```toml
# ~/.config/workdrive/profiles/development.toml
[profiles.development]
workspace_root = "~/projects/workspaces"
repository_selection = "prompt"
default_resources = ["tuios"]
optional_resources = ["youtrack"]
switch_resource = "tuios"

[profiles.development.worktree]
branch_template = "{{ .Workspace.Key }}"
path_template = "{{ .Workspace.Path }}/{{ .Repo.Name }}"
```

The default TUIOS resource is created when the wizard is confirmed, then `switch` opens it. The optional YouTrack resource uses the workspace key as the initial issue ID but allows override or omission. `park` kills only the owned TUIOS session. `cleanup` removes owned worktrees (with safety checks), kills any owned TUIOS session, detaches the YouTrack reference, and removes an otherwise-empty workspace. Original Git repositories and Git branches are preserved.

**Command examples:**

```bash
workdrive new PLATFORM-1425 --repo auth-service --repo user-service
workdrive switch PLATFORM-1425
workdrive park PLATFORM-1425
workdrive switch PLATFORM-1425
workdrive add repo PLATFORM-1425 --repo gateway
workdrive resource add youtrack PLATFORM-1425 --set issue_id=PLATFORM-1425
workdrive cleanup PLATFORM-1425
```

---

## 4. System-wide NFRs, safety policy and QA

### 4.1 Performance and responsiveness

- Target cached dashboard initial render in **< 200 ms** for 100 workspaces on a typical developer machine. Treat this as a performance target, not a correctness constraint.
- All filesystem scanning and script/API inspection occur off the Bubble Tea update thread, returning messages asynchronously.
- Batch/rate-limit inspections; no remote query on each keystroke or redraw. Use TTL caches and manual refresh.
- Avoid scanning entire home directory; enforce configured roots and depth; respect symlinks cautiously.

### 4.2 Idempotence, consistency and reliability

- Creating an already registered workspace with the same key must not create a duplicate; present existing workspace or a conflict.
- `switch`, `park`, `refresh` and cleanup retries must be idempotent where supported.
- Every side-effecting operation records planned steps and completed steps in a durable journal; on process interruption, recover by inspecting actual resources before retry.
- Use workspace-level cross-process locks to prevent cleanup racing with `add repo` or `switch`.
- File and DB writes must be atomic/recoverable; a registry index may be regenerated from manifests.
- When presence/status is `unknown`, never treat it as permission to create/delete.

### 4.3 Security

- Never interpolate untrusted workspace keys/paths into shell command strings; use argument vectors and safe templates. External scripts are explicitly trusted configuration, not auto-discovered from the workspace.
- Don't persist secrets in manifests/registry or echo them in logs. Inject only allowlisted environment variable names into scripts, not serialized credential values.
- Strictly validate canonical paths and resource ownership before deletion, including symlink escapes and TOCTOU rechecks immediately before destructive action.
- A custom resource's failure or malicious output must not authorize deleting paths outside the owned workspace root.
- No telemetry or network access by default. External network calls originate only from user-configured scripts.

### 4.4 UX and portability

- Primary supported OSes: macOS and Linux. Support ANSI terminals, keyboard navigation, small terminal widths, and noncolor output; avoid hardcoded OS-specific locations.
- Show informative errors when executable capabilities differ across installed Worktrunk/TUIOS versions. Keep integration version checks in adapters/scripts.
- Human-readable output by default and machine-readable `--json` where specified.
- Never require YouTrack just to create or enter a local workspace.

### 4.5 Testing standards

- Unit: template rendering, config layering, path validation, domain lifecycle, protocol decoding, data migrations, error mapping.
- Integration: temporary Git repositories, fake `wt` executable, fake command providers, simulated process failure/timeout, SQLite/manifests, Windows-style/special-character path edge cases even if Windows is out of scope.
- End-to-end (gated): real Worktrunk, TUIOS in an isolated named daemon, file cleanup. Never touch actual developer repositories.
- CI: `go test ./...`, `go vet ./...`, lint/format through mise. Provide realistic test fixtures and mocks.

---

## 5. Development workflow / acceptance gate for Codex

Implement incrementally, with an independently runnable and testable commit/checkpoint at the end of each MVP:

1. **MVP 1:** domain/config/persistence/CLI/worktree adapter/TUI/cleanup tests. Verify `new → add repo → park → switch → cleanup` with real temporary Git repos. Do not build YouTrack or a resource command runner yet.
2. **MVP 2:** introduce abstract resource types and command driver, migrate built-in worktree resource representation without changing core user behavior, add generic forms/statuses/CLI and TUIOS reference scripts. Verify `new → switch TUIOS → park (kills session) → switch (recreates) → cleanup`.
3. **MVP 3:** add search/batch/actions/hooks/dependencies/profiles/import/doctor and YouTrack config/scripts; verify asynchronous badges and linked-resource cleanup using a mock HTTP server.

At each checkpoint deliver:

- Compiling CLI with `--help` and example usage.
- Working tests and a short test report.
- Example config and documented prerequisites.
- Changelog or checklist of completed acceptance tests and outstanding deviations.
- Updated README demonstrating real workflows.

**Non-goals across all three MVPs:** writing code with Claude automatically, creating a Git hosting repository, cloning remote repositories automatically, automatically creating YouTrack tickets, deleting linked remote issues or GitLab MRs, remote workspace synchronization, a Workdrive daemon, auto-cleanup based on YouTrack status, and forcibly deleting dirty work without explicit user-directed recovery. These can be considered separately after the MVPs.

## 6. External CLI reference notes

The following are current integration contracts to validate with the user's installed versions; don't assume flags are supported without a runtime check:

- Worktrunk supports `wt switch --create <branch> --path <path>` and `wt -C <repo> ...`, with `--format json` for automation. The custom `--path` flag is marked experimental. Source: <https://worktrunk.dev/switch/>
- Worktrunk removal has `--no-delete-branch`, `--foreground`, `--format json`. Its default removal may run in the background, so explicitly request foreground completion. Source: <https://worktrunk.dev/remove/>
- TUIOS has `--server NAME` for isolated daemons, `new <session> --cwd <dir> --detach`, `ls --json`, `kill-session`, and `switch-session --create --cwd`. `ls --json` can include saved sessions when the daemon is down and return exit code 3; don't misinterpret this. Sources: <https://tuios.dev/docs/cli-reference> and <https://tuios.dev/docs/sessions>

---

**End of specification.**
