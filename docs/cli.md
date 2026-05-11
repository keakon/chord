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
| `chord test-providers`           | Smoke-test configured providers with a minimal request           |
| `chord cleanup status`           | Inspect state/cache/log sizes managed by the path locator        |
| `chord cleanup <kind>`           | Clean `sessions` / `cache` / `logs` / `project` (dry-run by default) |
| `chord worktree list`            | List chord-managed worktrees of the current repository           |
| `chord worktree remove <name>`   | Remove a chord-managed worktree                                  |
| `chord worktree finish <name>`   | Rebase a worktree branch onto main, fast-forward, then remove    |
| `chord resume <session-id>`      | Resume a session by ID, auto-locating its worktree               |
| `chord import <source> [file]`   | Import an external session into Chord's session store            |

## Global flags

These flags are accepted by every command and are merged with environment variables and `config.yaml` (CLI flag wins, then env var, then config file).

| Flag             | Description                                                                                                      | Env var                | Default                                                                          |
| ---------------- | ---------------------------------------------------------------------------------------------------------------- | ---------------------- | -------------------------------------------------------------------------------- |
| `--api-base`     | Override the base URL passed to providers when the provider config does not set its own `api_url`                | `CHORD_API_BASE`       | empty                                                                            |
| `--config-home`  | Config home directory containing `config.yaml`, `auth.yaml`, `agents/`, `skills/`, `commands/`                   | `CHORD_CONFIG_HOME`    | `$XDG_CONFIG_HOME/chord` if set, else `~/.config/chord`                          |
| `--state-dir`    | Durable runtime state (sessions, exports, logs, project registry, worktree metadata)                             | `CHORD_STATE_DIR`      | `$XDG_STATE_HOME/chord` if set, else `~/.local/state/chord`                      |
| `--cache-dir`    | Rebuildable cache (runtime caches, transient artifacts)                                                          | `CHORD_CACHE_DIR`      | `$XDG_CACHE_HOME/chord` if set, else `~/.cache/chord`                            |
| `--sessions-dir` | Override the sessions root only                                                                                  | `CHORD_SESSIONS_DIR`   | `<state-dir>/sessions`                                                           |
| `--logs-dir`     | Override the logs directory only                                                                                 | `CHORD_LOGS_DIR`       | `<state-dir>/logs`                                                               |

`--config` is a hidden alias of `--config-home` kept for backward compatibility; new scripts should use `--config-home`.

For the full directory layout, see [Paths](./paths.md). For all environment variables, see [Environment variables](./environment.md).

## `chord` (default — TUI)

Runs the local TUI in the current directory. The first run creates `.chord/` in the project as needed and registers the project under `<state-dir>/projects/<project-key>.json`.

### Flags

| Flag                        | Description                                                                                                                                                                                  |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-c`, `--continue`          | Resume the most recent non-empty session for this project                                                                                                                                    |
| `-r`, `--resume <id>`       | Resume a specific session ID for this project                                                                                                                                                |
| `--worktree [name]`         | Create or enter a chord-managed git worktree by name (auto-named when no name is given). Combine with `--continue` / `--resume` to act on the worktree's own session history.                |

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

Sign in with a `preset: codex` OAuth provider and store credentials under `~/.config/chord/auth.yaml`. Without a provider name, Chord auto-selects the only configured codex provider, or prompts you to choose when multiple are configured.

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

`chord auth login [provider]` is a hidden subcommand kept as an explicit alias for the same flow.

## `chord headless`

Run Chord without a TUI. Input is JSON commands on stdin, output is JSON envelopes on stdout. See [Headless](./headless.md) for the full protocol.

### Flags

| Flag                        | Description                                                            |
| --------------------------- | ---------------------------------------------------------------------- |
| `-d`, `--session-dir <dir>` | Project directory the headless session targets (default: current dir)  |
| `-c`, `--continue`          | Continue the latest session in the target directory                    |
| `-r`, `--resume <id>`       | Resume a specific session ID in the target directory                   |
| `--worktree [name]`         | Create or enter a chord-managed worktree before starting               |

### Examples

```bash
chord headless
chord headless -d /path/to/repo --continue
chord headless -d /path/to/repo --worktree feat-auth
```

## `chord test-providers`

Send a minimal request to each configured provider and report success / failure. Useful as an auth and connectivity smoke test. The command uses the same merged global + project config view as normal runtime startup.

### Flags

| Flag                  | Description                            |
| --------------------- | -------------------------------------- |
| `--provider <name>`   | Test only the named provider; errors if the name is not in the merged config |

### Examples

```bash
chord test-providers
chord test-providers --provider openai
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
state_dir: /Users/me/.local/state/chord (12345678 bytes)
cache_dir: /Users/me/.cache/chord (4567890 bytes)
logs_dir:  /Users/me/.local/state/chord/logs (123456 bytes)
sessions:  42 across 7 projects
```

### `chord cleanup sessions | cache | logs | project`

Clean a specific kind of managed data. **Defaults to a dry run** — pass `--yes` to actually delete.

| Flag                        | Description                                                                                  |
| --------------------------- | -------------------------------------------------------------------------------------------- |
| `--older-than <duration>`   | Only consider entries older than this duration (Go duration syntax, e.g. `720h` for 30 days) |
| `--yes`                     | Actually delete; without this flag the command only previews what would be removed           |

| Kind        | What it cleans                                                                              |
| ----------- | ------------------------------------------------------------------------------------------- |
| `sessions`  | Old session directories under `<state-dir>/sessions/<project-key>/`                         |
| `cache`     | Rebuildable cache under `<cache-dir>/runtime/`                                              |
| `logs`      | Rotated logs under `<state-dir>/logs/`                                                      |
| `project`   | Orphan project entries (project directories that no longer exist on disk)                   |

### Examples

```bash
# Preview what would be removed
chord cleanup sessions --older-than 720h

# Actually remove sessions older than 30 days
chord cleanup sessions --older-than 720h --yes

# Clear all rebuildable cache (will reload on next run)
chord cleanup cache --yes
```

## `chord worktree`

Manage chord-owned git worktrees. Note that creating or entering a worktree is a startup-level action — it lives on the `chord --worktree` flag rather than under this subcommand. `chord worktree` only owns pure management operations.

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

Rebase the worktree branch onto the main line, fast-forward that main branch to include it, then remove the worktree and delete its branch.

| Flag             | Description                                                                                                                |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------- |
| `--onto <branch>`| Target main branch to rebase onto and fast-forward (default: the main worktree's current branch)                            |
| `--force`        | Relax clean-tree checks; pass `--autostash` to git rebase; force-delete the branch when reclaiming                          |
| `--check`        | Preview whether the rebase would succeed cleanly in a temporary worktree without mutating the real worktree or branch       |

If a rebase hits conflicts, `finish` exits with explicit recovery hints (`git status`, `git rebase --show-current-patch`, then choose `--skip` / `--continue` / `--abort`) and keeps the worktree and branch so you can resolve and re-run.

If a rebase is already in progress in the worktree, `finish` exits early instead of starting another rebase.

Use `--check` when you want a conflict preflight without leaving the real worktree in a half-rebased state.

### Examples

```bash
chord worktree list
chord worktree remove feat-old --delete-branch
chord worktree finish feat-auth --onto main
```

## `chord resume <session-id>`

Resume a session by ID. Unlike `chord --resume`, this command can locate the session even when the original worktree differs from the current directory — it auto-detects which chord-managed worktree the session belongs to and switches into it.

```bash
chord resume 20260428064910975
```

## `chord import <source> [file]`

Import an external agent session into a resumable Chord session. Currently supported sources: `opencode`, `codex`, `claude`.

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
