# CLI Reference

This page lists every Chord command, subcommand, and flag.

For installation and first-time setup, start with the [Quickstart](./quickstart.md).

## Synopsis

```text
chord [global flags] [command] [command flags] [args]
```

Without a command, `chord` runs the local TUI in the current directory.

## Command summary

| Command                          | Purpose                                                          |
| -------------------------------- | ---------------------------------------------------------------- |
| `chord`                          | Run the local TUI                                                |
| `chord auth [provider]`          | Sign in with a `preset: codex` OAuth provider                    |
| `chord headless`                 | Run without TUI; stdio JSON control plane                        |
| `chord doctor models`            | Diagnose configured provider/model calls                         |
| `chord cleanup status`           | Inspect state/cache/log sizes managed by the path locator        |
| `chord cleanup <kind>`           | Clean `sessions` / `cache` / `logs` / `project` (dry-run by default) |
| `chord worktree list`            | List chord-managed worktrees of the current repository           |
| `chord worktree remove <name>`   | Remove a chord-managed worktree                                  |
| `chord worktree finish <name>`   | Merge the target branch into the real worktree, squash the result back as one commit, then remove the worktree |
| `chord resume <session-id>`      | Resume a session by ID, auto-locating its worktree               |
| `chord import <source> [file]`   | Import an external session into Chord's session store and convert recognizable external tools to current Chord tool cards |

## Global flags

These flags are accepted by every command and are merged with environment variables and `config.yaml` (CLI flag wins, then env var, then config file).

| Flag             | Description                                                                                                      | Env var                | Default                                                                          |
| ---------------- | ---------------------------------------------------------------------------------------------------------------- | ---------------------- | -------------------------------------------------------------------------------- |
| `--api-base`     | Override the base URL passed to providers when the provider config does not set its own `api_url`. The `CHORD_API_BASE` env var appears in `--help` but is not read by the current build; only this flag or a per-provider `api_url` takes effect | `CHORD_API_BASE`       | empty                                                                            |
| `--config-home`  | Config home directory containing `config.yaml`, `auth.yaml`, `agents/`, `skills/`, `commands/`                   | `CHORD_CONFIG_HOME`    | `$XDG_CONFIG_HOME/chord` if set, else `~/.config/chord`                          |
| `--state-dir`    | Durable runtime state (sessions, exports, logs, project registry, worktree metadata)                             | `CHORD_STATE_DIR`      | `$XDG_STATE_HOME/chord` if set, else `~/.local/state/chord`                      |
| `--cache-dir`    | Rebuildable cache (runtime caches, transient artifacts)                                                          | `CHORD_CACHE_DIR`      | `$XDG_CACHE_HOME/chord` if set, else `~/.cache/chord`                            |
| `--sessions-dir` | Override the sessions root only                                                                                  | `CHORD_SESSIONS_DIR`   | `<state-dir>/sessions`                                                           |
| `--logs-dir`     | Override the logs directory only                                                                                 | `CHORD_LOGS_DIR`       | `<state-dir>/logs`                                                               |

For the full directory layout, see [Paths](./paths.md). For all environment variables, see [Environment variables](./environment.md).

## `chord` (default — TUI)

Runs the local TUI in the current directory. On the first run, if global `config.yaml` is missing and Chord can get a controlling TTY, it starts a one-time setup wizard before opening the TUI. The wizard writes `config.yaml` and, when needed, `auth.yaml`, reuses matching existing `auth.yaml` credentials when possible, can complete Codex OAuth sign-in during setup, then shows the exact paths it used. The API-key provider path accepts endpoints ending in `/responses`, `/messages`, `/chat/completions`, or `/models`, and recommends starter provider/model defaults from that suffix. Redirected stdin alone does not disable the wizard: if Chord can still open the controlling TTY, setup runs there. If no controlling TTY is available, Chord exits with a clear initialization error instead of waiting for input. `help`, `version`, and non-root subcommands do not trigger the wizard.

### Flags

| Flag                        | Description                                                                                                                                                                                  |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-c`, `--continue`          | Resume the most recent non-empty session for this project                                                                                                                                    |
| `-r`, `--resume <id>`       | Resume a specific session ID for this project                                                                                                                                                |
| `--yolo`                    | Start with YOLO mode enabled: temporarily bypass main-agent tool permissions except handoff, delegate, cancel, and done                                                                                 |
| `-w`, `--worktree [name]`   | Create or enter a chord-managed git worktree by name (auto-named when no name is given). Combine with `--continue` / `--resume` to act on the worktree's own session history.                |

`--continue` and `--resume` are mutually exclusive.

### Examples

```bash
# Plain start
chord

# Resume the most recent session
chord --continue

# Resume a specific session
chord --resume 20260428064910975

# Create / enter a chord-managed worktree
chord --worktree feat-auth

# Resume the latest session inside a worktree
chord --worktree feat-auth --continue
```

## `chord auth [provider]`

Sign in after the base configuration is in place. This command is for `preset: codex` OAuth providers and stores credentials under `~/.config/chord/auth.yaml`. Chord also keeps machine-managed shared OAuth runtime state in `~/.config/chord/auth.state.yaml` so quota/reset caching does not constantly rewrite `auth.yaml`. Without a provider name, Chord auto-selects the only configured codex provider, or prompts you to choose when multiple are configured. The first-run wizard can complete this same Codex OAuth sign-in flow during setup; `chord auth codex` remains the direct command when you want to sign in again later.

### Flags

| Flag             | Description                                                                                                      |
| ---------------- | ---------------------------------------------------------------------------------------------------------------- |
| `--device-code`  | Use device-code flow (paste a one-time code into the provider's web page) instead of the local browser callback. Useful for SSH / headless / WSL where opening a browser locally is not possible. |

### Examples

```bash
# Auto-select a configured codex provider
chord auth

# Explicitly choose a provider name
chord auth codex

# Headless / SSH environments
chord auth codex --device-code
```

### `chord auth refresh <provider>`

Refresh every refresh-token backed OAuth credential for a `preset: codex` provider. The command prints one line per credential as refreshed, failed, or skipped; skipped credentials include API keys and OAuth entries without a refresh token. Any failed refresh makes the command return an error after processing the remaining credentials.

Successful refreshes update `auth.yaml` and synchronize the matching runtime entry in `~/.config/chord/auth.state.json` while preserving quota/reset hints.

```bash
chord auth refresh codex
```

### `chord auth state list`

List expired, deactivated, or invalidated OAuth runtime-state entries from `~/.config/chord/auth.state.yaml`. This command does not report orphan state entries whose matching OAuth credential was removed from `auth.yaml`; use `chord auth state clean` to remove both invalid and orphan state.

```bash
chord auth state list
```

### `chord auth state clean`

Remove invalid OAuth runtime-state entries from `~/.config/chord/auth.state.yaml`, orphan state entries whose OAuth credential no longer exists in `auth.yaml`, and matching expired / deactivated / invalidated OAuth credentials from `~/.config/chord/auth.yaml`.

Typical use cases:

- clear shared cached state and matching credentials for expired / deactivated / invalidated accounts;
- keep `auth.state.yaml` and `auth.yaml` in sync after rotating or retiring accounts;
- remove unusable OAuth credentials after Chord marks them expired, deactivated, or invalidated.

```bash
chord auth state clean
```

## `chord headless`

Run Chord without a TUI. Input is JSON commands on stdin, output is JSON envelopes on stdout. See [Headless](./headless.md) for the full protocol.

### Flags

| Flag                        | Description                                                            |
| --------------------------- | ---------------------------------------------------------------------- |
| `-d`, `--session-dir <dir>` | Project directory the headless session targets (default: current dir)  |
| `-c`, `--continue`          | Continue the latest session in the target directory                    |
| `-r`, `--resume <id>`       | Resume a specific session ID in the target directory                   |
| `-w`, `--worktree [name]`   | Create or enter a chord-managed worktree before starting               |

### Examples

```bash
chord headless
chord headless -d /path/to/repo --continue
chord headless -d /path/to/repo --worktree feat-auth
```

## `chord doctor models`

Run lightweight diagnostics for configured model calls using the same provider transport path as normal LLM requests. It loads `config.yaml` / `auth.yaml`, resolves each selected target to a canonical `provider/model[@variant]` ref, applies model and variant tuning, and reports success, latency, text chunks, token usage when available, and Responses transport (`http` or `websocket`). The command uses the same merged global + project config view as normal runtime startup.

By default, Chord tests one representative model per configured provider. The representative is stable: the first model referenced by any `model_pools` entry for that provider, or the provider's first model by name when no pool references it. Each diagnostic target makes one request attempt by default; use `--retry` only when you explicitly want to retry transient failures. When a provider has multiple credentials, diagnostics intentionally use only the first credential so later keys cannot hide that credential's failure.

### Flags

| Flag                  | Description                                                                                              |
| --------------------- | -------------------------------------------------------------------------------------------------------- |
| `--provider <name>`   | Test only the named provider's representative model, or provide the provider for a bare `--model` value   |
| `--model <ref>`       | Test one model. Use `provider/model[@variant]`, or `model[@variant]` only together with `--provider`      |
| `--pool <name>`       | Test every model ref in the named `model_pools` entry independently, preserving pool order                |
| `--all-models`        | Test all configured models for `--provider` (must be combined with `--provider`)                          |
| `--all-pools`         | Test every configured model pool                                                                         |
| `--timeout <duration>`| Per-model request timeout (default `30s`)                                                                |
| `--retry <count>`     | Maximum request attempts per target (default `1`; client/auth errors such as 400/401/403 are not retried) |
| `--fail-fast`         | Stop after the first failed request or configuration error                                                |
| `--json`              | Emit a machine-readable JSON report                                                                      |

`--model`, `--pool`, and `--all-pools` are mutually exclusive. Pool checks do not use fallback: each pool entry is requested independently so an unavailable fallback target is not hidden by a later successful model.

### Examples

```bash
# Smoke-test all configured providers with representative models
chord doctor models

# Test one provider's representative model
chord doctor models --provider openai

# Test an exact model or variant
chord doctor models --model openai/gpt-5.5
chord doctor models --model openai/gpt-5.5@high
chord doctor models --provider openai --model gpt-5.5@high

# Audit a model pool or all pools
chord doctor models --pool thinking
chord doctor models --all-pools --json

# Test every configured model for one provider
chord doctor models --provider openai --all-models --fail-fast
```

## `chord cleanup`

Inspect or clean state, cache, and log directories managed by the path locator.

### `chord cleanup status`

Print sizes for state, cache, and logs directories, plus session and project counts. Read-only.

```bash
chord cleanup status
```

Sample output:

```text
state_dir: /Users/me/.local/state/chord (29.6 GB)
cache_dir: /Users/me/.cache/chord (847 B)
logs_dir: /Users/me/.local/state/chord/logs (263.5 MB)
sessions: 42 across 7 projects
```

### `chord cleanup sessions | cache | logs | project`

Clean a specific kind of managed data. **Defaults to a dry run** — pass `--yes` to actually delete.

| Flag                        | Description                                                                                  |
| --------------------------- | -------------------------------------------------------------------------------------------- |
| `--older-than <duration>`   | Only consider entries older than this duration (Go duration syntax, e.g. `720h` for 30 days) |
| `--yes`                     | Actually delete; without this flag the command only previews what would be removed           |

| Kind        | What it cleans                                                                                                  |
| ----------- | --------------------------------------------------------------------------------------------------------------- |
| `sessions`  | Old session directories under `<state-dir>/sessions/<project-key>/`; removes project session dirs left with only `project.json` after their sessions are gone |
| `cache`     | Rebuildable cache under `<cache-dir>/runtime/`                                                                  |
| `logs`      | Rotated logs under `<state-dir>/logs/`                                                                          |
| `project`   | Orphan project entries (project directories that no longer exist on disk)                                       |

### Examples

```bash
# Preview what would be removed
chord cleanup sessions --older-than 720h

# Actually remove sessions older than 30 days
chord cleanup sessions --older-than 720h --yes

# Clear all rebuildable cache (will reload on next run)
chord cleanup cache --yes
```

Sample output:

```text
would remove /Users/me/.local/state/chord/sessions/project-a/202605120001 (263.5 MB)
dry-run: pass --yes to delete
```

## `chord worktree`

Manage chord-owned git worktrees. Use `chord worktree <name>` (or `chord --worktree <name>`) to create or enter a worktree and start a session there; use this command's subcommands for management operations such as `list`, `remove`, and `finish`.

Worktrees live under `<state-dir>/worktrees/<repo-id>/<slug>` (outside the repository) and each gets its own project key, so sessions and caches are isolated automatically.

### `chord worktree list`

List chord-managed worktrees of the current repository.

### `chord worktree remove <name>`

Delete the worktree directory and its sessions, cache, and exports. The branch is preserved by default.

| Flag                | Description                                                                                                       |
| ------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `--force`           | Remove even when the worktree has uncommitted changes; force-delete the branch                                    |
| `--delete-branch`   | Also delete the worktree's branch. Without `--force`, the branch is only deleted if it has been merged.           |

### `chord worktree finish <name>`

Merge the target branch into the real worktree branch first, then squash the finished worktree state back onto that target branch as a single commit, fast-forward that target branch to include the squashed result, and finally remove the worktree and delete its branch.

| Flag               | Description                                                                                                                       |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------- |
| `--onto <branch>`  | Target branch to merge into the worktree and squash back onto (default: the main worktree's current branch)                      |
| `--check`          | Preview whether the target branch can merge cleanly into the worktree in a temporary worktree; a real finish may leave the real worktree in a merge state while you resolve conflicts |
| `-m, --message <message>` | Override the generated squash commit message for the final finish commit                                                     |

If merging the target branch into the worktree would hit conflicts, `finish` exits with conflict details, keeps the target branch unchanged, and leaves the real worktree in that merge for you to resolve and re-run.

If a rebase or merge is already in progress in the worktree, `finish` exits early instead of starting another merge on top of it.

Use `--check` when you want a conflict preflight without mutating the real worktree, branch, or target branch. A real `finish` is intentionally not side-effect free: if the merge from the target branch conflicts, Chord keeps the real worktree in that merge state so you can resolve it and rerun `finish`.

Pass `-m/--message` when you want to override the generated squash message with a final commit message you wrote yourself.

A real `finish` that needs to create the squashed commit also requires git commit identity (`user.name` / `user.email`, or `GIT_AUTHOR_*` / `GIT_COMMITTER_*`). `--check` does not require commit identity because it stops after the merge preflight.

### Examples

```bash
chord worktree list
chord worktree remove feat-old --delete-branch
chord worktree finish feat-auth --onto main
chord worktree finish feat-auth --onto main -m "feat(auth): finalize auth flow"
```

## `chord resume <session-id>`

Resume a session by ID. Unlike `chord --resume`, this command can locate the session even when the original worktree differs from the current directory — it auto-detects which chord-managed worktree the session belongs to and switches into it.

```bash
chord resume 20260428064910975
```

## `chord import <source> [file]`

Import an external agent session into a resumable Chord session. Currently supported sources: `opencode`, `codex`, `claude`.

For Claude Code imports, Chord reconstructs the best-effort **main non-sidechain conversation** instead of blindly importing the newest raw leaf. Compact boundaries are used for reconstruction, not rendered as visible transcript messages. Sidechain/sub-agent entries are excluded from the main imported session by default; when detected, CLI output reports the skipped count and `import-report.json` records Claude-specific diagnostics, including sidechain agent IDs when present.

Recognizable imported tools are displayed as the closest current Chord tool card when possible, including file mutations as `edit`, `write`, or `delete`. Tools without a usable mapping remain visible as unsupported tool cards or readable fallback assistant messages instead of raw JSON. Converted imported tools do not restore Chord FileTracker read/write state, so re-run `read` before continuing file edits from an imported session.

### Flags

| Flag                      | Description                                                                                                       |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `--project <path>`        | Project to write into (default: current directory)                                                                |
| `--sid <id>`              | Specify the Chord session id (default: auto-generated)                                                            |
| `--id <session-id>`       | Import by source session id instead of file path (supported for `codex` and `claude`)                             |
| `--root <path>`           | Root directory for `--id` lookup (codex default `~/.codex/sessions`, claude default `~/.claude/projects`)         |
| `--tool-mode <mode>`      | Tool import policy: `auto`, `text`, or `structured` (default depends on source)                                   |
| `--reasoning <mode>`      | Reasoning import policy: `off`, `visible`, or `strict` (default `strict`)                                         |
| `--dry-run`               | Parse and report only; do not write a session                                                                     |
| `--json`                  | Machine-readable JSON summary                                                                                     |
| `--force`                 | Allow overwriting an existing `--sid`                                                                             |

### Examples

```bash
# OpenCode export
opencode export <sessionID> > export.json
chord import opencode export.json
chord resume <sid>

# Codex by file
chord import codex ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl

# Codex by session id
chord import codex --id <session-id>

# Claude Code by file
chord import claude ~/.claude/projects/**/<sessionId>.jsonl

# Claude Code by session id
chord import claude --id <session-id>
```

See the [Importing external sessions](./usage.md#importing-external-sessions) section for the full notes on tool/reasoning policy, conversion warnings, and provider-safe wire normalization.

## Running from source

When you run from source, use the package path (not `main.go`):

```bash
go run ./cmd/chord/
go run ./cmd/chord/ headless
go run ./cmd/chord/ --worktree feat-auth
```

`go run cmd/chord/main.go` will not pick up the rest of the `main` package and is not supported.

## Related

- [Quickstart](./quickstart.md)
- [Usage](./usage.md)
- [Configuration & Auth](./configuration.md)
- [Paths](./paths.md)
- [Environment variables](./environment.md)
- [Headless](./headless.md)
