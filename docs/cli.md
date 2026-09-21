# CLI Reference

Find commands, options, and examples for startup, authentication, sessions, cleanup, and worktree management.

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
| `chord acp`                      | Serve the Agent Client Protocol over stdio for ACP clients       |
| `chord doctor config`            | Validate global/project config files                            |
| `chord doctor models`            | Diagnose configured provider/model calls                         |
| `chord doctor skills`            | Diagnose skill discovery, loading, and visibility                |
| `chord cleanup status`           | Inspect state/cache/log sizes managed by the path locator        |
| `chord cleanup <kind>`           | Clean `sessions` / `cache` / `logs` / `project` (dry-run by default) |
| `chord worktree list`            | List chord-managed worktrees of the current repository           |
| `chord worktree remove <name>`   | Remove a chord-managed worktree                                  |
| `chord worktree finish <name>`   | Merge the target branch into the real worktree, squash the result back as one commit, then remove the worktree |
| `chord resume <session-id>`      | Resume a session by ID, auto-locating its worktree               |
| `chord import <source> [file]`   | Import an external session into Chord's session store and convert recognizable external tools to current Chord tool cards |
| `chord sessions project <id>`    | Project a persisted session into per-turn JSONL facts (read-only) |
| `chord completion <shell>`       | Generate shell completion scripts for `bash`, `fish`, `powershell`, or `zsh` |
| `chord help [command]`           | Show command help                                                |

## Global flags

These flags are accepted by every command and are merged with environment variables and `config.yaml` (CLI flag wins, then env var, then config file).

| Flag             | Description                                                                                                      | Env var                | Default                                                                          |
| ---------------- | ---------------------------------------------------------------------------------------------------------------- | ---------------------- | -------------------------------------------------------------------------------- |
| `--api-base`     | Override the provider base URL for this invocation; when set, it takes precedence over each provider's `api_url` | `CHORD_API_BASE`       | empty                                                                            |
| `--config-home`  | Config home directory containing `config.yaml`, `auth.yaml`, `agents/`, `skills/`, `commands/`                   | `CHORD_CONFIG_HOME`    | `$XDG_CONFIG_HOME/chord` if set, else `~/.config/chord`                          |
| `--state-dir`    | Durable runtime state (sessions, exports, logs, project registry, worktree metadata)                             | `CHORD_STATE_DIR`      | `$XDG_STATE_HOME/chord` if set, else `~/.local/state/chord`                      |
| `--cache-dir`    | Rebuildable cache (runtime caches, transient artifacts)                                                          | `CHORD_CACHE_DIR`      | `$XDG_CACHE_HOME/chord` if set, else `~/.cache/chord`                            |
| `--sessions-dir` | Override the sessions root only                                                                                  | `CHORD_SESSIONS_DIR`   | `<state-dir>/sessions`                                                           |
| `--logs-dir`     | Override the logs directory only                                                                                 | `CHORD_LOGS_DIR`       | `<state-dir>/logs`                                                               |
| `-h`, `--help`   | Show help for the current command                                                                                | —                      | false                                                                            |
| `-v`, `--version`| Print build and runtime version information, then exit                                                           | —                      | false                                                                            |

For the full directory layout, see [Paths](./paths.md). For all environment variables, see [Environment variables](./environment.md).

`-v/--version` is available on the root command. Subcommands expose `-h/--help` and the same path / API override flags shown above.

## `chord` (default: TUI)

Runs the local TUI in the current directory. That directory becomes the session working directory: relative file paths, omitted `shell` workdirs, and omitted `grep` / `glob` search roots resolve from it. When `--worktree` or `chord resume` switches into a chord-managed worktree, that worktree path becomes the session working directory instead; file tools do not need to understand git worktrees separately. The session working directory is injected before the first user message (and again after context compaction) so the model sees the same path base that tools use. User-facing tool cards may display paths relative to it for readability, while raw tool-call arguments and session exports preserve the model's original paths for auditing.

On the first run, if global `config.yaml` is missing and Chord can get a controlling TTY, it starts a one-time setup wizard before opening the TUI; see [Quickstart](./quickstart.md#2-first-run) for what it asks, what it writes, and how it behaves without a controlling TTY. `help`, `version`, and non-root subcommands do not trigger the wizard.

### Flags

| Flag                        | Description                                                                                                                                                                                  |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-c`, `--continue`          | Resume the most recent non-empty session for this project that is not already open elsewhere                                                                                                 |
| `-r`, `--resume <id>`       | Resume a specific session ID of the current project (for a session in another chord-managed worktree, use `chord resume <id>`)                                                              |
| `--fork-history[=N]`        | Fork the session named by `--resume` at a compaction boundary and resume the fork instead: omit N for the latest applied boundary or pass a `history-N` number (e.g. `=2`). Only valid with `--resume`, and the session must belong to the current project — use `chord resume <id> --fork-history` to fork a session in another worktree. Forking works while the source session is open elsewhere (see [Resuming sessions](#resuming-sessions)). |
| `--yolo`                    | Start with YOLO mode enabled: ordinary tools skip their permission checks and confirmations; handoff, delegate, cancel, done, and compact_context keep following their configured rules                                                                                 |
| `-w`, `--worktree [name]`   | Create or enter a chord-managed git worktree by name (auto-named when no name is given). Combine with `--continue` / `--resume` to act on the worktree's own session history.                |

`--continue` and `--resume` are mutually exclusive; `--fork-history` applies only with `--resume` and cannot be combined with `--continue` or `--worktree`.

`--continue` picks the most recently active non-empty session that this process can open. Chord orders sessions by the newer of two modification times, `main.jsonl` and an existing `usage-summary.json`; it does not scan full transcripts or build a separate project-level index. Filesystem timestamps can become misleading after copying or restoring session files, and activity that updates neither file may not affect the order.

A session that another running Chord process already owns is skipped, and the next candidate is used; the skip is announced with a one-time notice (a toast in the TUI, a `skipped_locked_sessions` field in the headless ready envelope, and the log), so the switch is never silent. If every session is owned elsewhere, Chord starts a new one. `--resume <id>` names one specific session instead, so it reports an error rather than substituting another when that session is already open.

### Resuming sessions

Both entry points run the same resume pipeline; they differ only in how the session is located:

- `chord --resume <id>` (alias `-r`) resumes a session **of the current project**: the session must live in the project the current directory belongs to, and Chord does not switch directories. It composes with `--continue` / `--worktree` and is the form scripts and headless use. If the session belongs to another chord-managed worktree, the resume fails with a not-found error.
- `chord resume <id>` resumes a session **by ID from anywhere**: it reads the repository index, finds which chord-managed worktree (or the main repository) the session belongs to, switches into it, and resumes there.

Rule of thumb: when you are already inside the session's project use `chord --resume`; when you are anywhere else, or do not know which worktree the session lives in, use `chord resume <id>`.

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

Sign in after the base configuration is in place. This command is for `preset: codex` OAuth providers and stores credentials under `~/.config/chord/auth.yaml`. Chord also keeps machine-managed shared OAuth runtime state in `~/.config/chord/auth.state.json` so quota/reset caching does not constantly rewrite `auth.yaml`. Without a provider name, Chord auto-selects the only configured codex provider, or prompts you to choose when multiple are configured. The first-run wizard can complete this same Codex OAuth sign-in flow during setup; `chord auth codex` remains the direct command when you want to sign in again later.

During normal model requests, OAuth responses such as HTTP 401/403 `token_invalidated`, revoked tokens, expired refresh tokens, or deactivated accounts are treated as permanent credential failures. Chord marks the matching OAuth runtime-state entry as expired, deactivated, or invalidated (using account/user metadata first, then refresh-token hash fallback), removes that credential from the selectable key pool, and refreshes the TUI `Keys` count. Retry entries in the error panel include the OAuth email/account and a masked `key=...` label that shows a short prefix and suffix of the credential for human-friendly identification. Use `chord auth` to sign the account in again, or `chord auth state clean` to remove unusable entries.

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

List expired, deactivated, or invalidated OAuth runtime-state entries from `~/.config/chord/auth.state.json`. This command does not report orphan state entries whose matching OAuth credential was removed from `auth.yaml`; use `chord auth state clean` to remove both invalid and orphan state.

```bash
chord auth state list
```

### `chord auth state clean`

Remove invalid OAuth runtime-state entries from `~/.config/chord/auth.state.json`, orphan state entries whose OAuth credential no longer exists in `auth.yaml`, and matching expired / deactivated / invalidated OAuth credentials from `~/.config/chord/auth.yaml`.

Typical use cases:

- clear shared cached state and matching credentials for expired / deactivated / invalidated accounts;
- keep `auth.state.json` and `auth.yaml` in sync after rotating or retiring accounts;
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

## `chord acp`

Serve the Agent Client Protocol over stdio, so an ACP client such as Zed can drive Chord as its agent. stdout carries JSON-RPC only; Chord's logs and any stray stdio output go to `chord.log`.

There are no flags: the client sends the working directory with `session/new`, and model, permissions, MCP servers, and session storage all come from Chord's own configuration. One process serves one session.

### Examples

```bash
# Launched by the ACP client; running it by hand expects JSON-RPC on stdin
chord acp
```

See [ACP Agent Mode](./acp.md) for client setup, what the client sees, and current limits.

## `chord doctor config`

Check the global and project `config.yaml` files for unrecognized keys, wrongly typed values, malformed YAML, and invalid setting values (such as an unknown `retry_backoff` or a negative diagnostics threshold). The command reports every problem it finds in one pass instead of stopping at the first one.

Chord's config loader logs these problems and starts anyway, treating the offending value as not configured. This command surfaces them explicitly so you can validate a config file without reading the log.

### Flags

| Flag      | Description                            |
| --------- | -------------------------------------- |
| `--json`  | Emit a machine-readable JSON report    |

The global config is always checked; the project config (`.chord/config.yaml`) under the current working directory is checked when present. Any problem makes the command exit with status 2, which is convenient for scripts and CI.

### Examples

```bash
# Validate global + project config
chord doctor config

# Machine-readable report for scripts
chord doctor config --json
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

## `chord doctor skills`

Explain why a configured skill never reaches the model. It reuses the runtime discovery order and parser, but keeps invalid and shadowed files as their own rows instead of skipping them silently. It also audits the directories the runtime glob traverses, so an unreadable directory or a broken symlink shows up as a scan issue instead of looking empty. Each row reports four independent dimensions: `integrity` (whether the runtime keeps the file), `load` (whether the body reads back), `visibility` (whether the `builder` ruleset hides the skill), and `resources` (health of declared `resources` frontmatter entries), plus a `shadowed` flag when a higher-priority directory owns the name. Loading and reading a skill says nothing about whether the model will pick it.

Exit codes follow the `doctor` family: `1` when any skill fails integrity or load, `2` when the check itself cannot run. Scan problems — an unreadable directory, a broken symlink, a scan path that is not a directory — exit `2` and are listed in the report (`scan_issues`) instead of aborting it. Denied skills, shadowed duplicates, resource problems, and an empty skill set do not change the exit code.

### Flags

| Flag       | Description                                                              |
| ---------- | ------------------------------------------------------------------------ |
| `--json`   | Emit a machine-readable JSON report                                      |
| `--strict` | Also fail when the ruleset is unavailable or a check did not run         |

### Examples

```bash
# Diagnose skill discovery, loading, and visibility
chord doctor skills

# Machine-readable report, or fail on unchecked rows
chord doctor skills --json
chord doctor skills --strict
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

Clean a specific kind of managed data. **Defaults to a dry run**: pass `--yes` to actually delete.

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
would remove /Users/me/.local/state/chord/sessions/project-b (490 B)
would remove 1 sessions, 1 empty project dirs, total 263.5 MB
dry-run: pass --yes to delete
```

The last line summarizes what would be removed. Session directories and empty project dirs (which hold only a leftover `project.json`) are counted separately, so the byte total is not read as belonging to the empty dirs. With `--yes`, the same lines use `removed` instead of `would remove`, with no `dry-run` trailer.

```text
removed /Users/me/.local/state/chord/sessions/project-a/202605120001 (263.5 MB)
removed 1 sessions, total 263.5 MB
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

Resume a session by ID. Unlike `chord --resume`, this command can locate the session even when the original worktree differs from the current directory; it auto-detects which chord-managed worktree the session belongs to and switches into it. For when to use each entry point, see [Resuming sessions](#resuming-sessions).

```bash
chord resume 20260428064910975
```

### Forking a session at a compaction boundary

With `--fork-history`, the command no longer resumes the original session. Instead it forks the session at one of its compaction boundaries (copying the history as it was at that moment into a brand-new session) and resumes that fork. The original session is never modified and does not need to be closed: forking reads only the session's archive files and writes a new session directory, so it works even while the source session is open in another Chord process.

```bash
chord resume 20260428064910975 --fork-history       # fork at the latest applied boundary
chord resume 20260428064910975 --fork-history=2     # fork at the 2nd applied boundary
```

- A compaction boundary is a `[Context Summary]` checkpoint applied mid-session; each one kept the pre-compaction transcript as `main.pre-compress-N.jsonl`, with a `history-N.status.json` record marking it applied once the rewrite finished. Forking to an applied boundary N reproduces that generation's session state: the fork's `main.jsonl` is created record for record from `main.pre-compress-N.jsonl`, transcript records copied unchanged, including its leading checkpoint summary card, which was part of the real state you saw then (forking the 2nd boundary keeps the 1st summary card at the top). The earlier `history-1..N-1.md` compaction archives are copied alongside so the checkpoint's history map still resolves: the model can read the archives on demand to look up anything archived before that boundary. The boundary's own archive `history-N.md` is not copied: the forked `pre-compress-N` transcript still carries those messages inline, so copying it would only duplicate content. Earlier content is browsed through those archives rather than stitched back into the transcript.
- Message text (user input, assistant replies, tool calls and results, diffs) is preserved verbatim: session ids, paths, and commands inside the content are historical facts and are never rewritten. Image/PDF attachments are copied into the new session and their references updated.
- The fork is a fresh session: usage/token statistics and runtime state start from zero. `session-meta.json` records `forked_from` and keeps the source session's worktree provenance and manually enabled MCP servers.
- The new session id is printed and the fork is resumed automatically, entering the TUI like any other session. The session must have at least one applied compaction; requesting a boundary outside the available range fails with the list of valid `history-N` values.

**What is not copied:** the fork carries the main-session transcript plus the compaction archives, and nothing else. Sub-agent transcripts, delegated task records, mailbox state, background jobs, artifacts, and other runtime state under the source session's `subagents/` / `artifacts/` / `snapshot.json` are owned by the live source session and deliberately not copied: that state cannot be faithfully reconstructed at an earlier point in time, and usage statistics are tied to the source session as well. Consequences: browsing history is unaffected, but if you keep working in the fork, tasks delegated before the fork remain visible as message cards only, and they cannot be queried or resumed there, so any later work that depends on those agents cannot continue. New sub-agent and task activity started inside the fork works normally.

## `chord import <source> [file]`

Import an external agent session into a resumable Chord session. Currently supported sources: `opencode`, `codex`, `claude`.

For Claude Code imports, Chord reconstructs the best-effort **main non-sidechain conversation** instead of blindly importing the newest raw leaf. Compact boundaries are used for reconstruction, not rendered as visible transcript messages. Sidechain/sub-agent entries are excluded from the main imported session by default; when detected, CLI output reports the skipped count and `import-report.json` records Claude-specific diagnostics, including sidechain agent IDs when present.

Recognizable imported tool calls are always converted to the closest current Chord tool card when their arguments can be normalized, including file mutations as `edit`, `apply_patch`, `write`, or `delete`. Only records without a usable mapping (no Chord mapping, missing call id, or un-normalizable arguments) remain visible as readable fallback messages instead of raw JSON. Converted imported tools do not restore Chord FileTracker snapshots; re-run `read` when you need fresh file context or stale-change warnings before editing imported files.

### Flags

| Flag                      | Description                                                                                                       |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `--project <path>`        | Project to write into (default: current directory)                                                                |
| `--sid <id>`              | Specify the Chord session id (default: auto-generated)                                                            |
| `--id <session-id>`       | Import by source session id instead of file path (supported for `codex` and `claude`)                             |
| `--root <path>`           | Root directory for `--id` lookup (codex default `~/.codex/sessions`, claude default `~/.claude/projects`)         |
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

## `chord sessions project <session-id>`

Project a persisted session into per-turn facts as JSONL, one line per turn: turn boundaries, tool outcomes with result digests, tool-attributed file changes, and compaction boundaries. It is read-only: the session directory is never written, so it works while the session is open in another process. Use it when reviewing a session, preparing evidence for a completion report, or feeding structured facts to another tool instead of grepping the raw transcript.

Turn causes are downgraded on purpose: a turn started by your message reports `user_message`; anything else reports `inferred` (a synthetic starter such as a compaction checkpoint or a background result) or `unknown` (no leading user message at all). The projection never guesses between a user continue and a background wake: they are observationally identical in the persisted history.

Each turn carries `turn_index`, `trigger`, bounded `user_text` / `assistant_final_text` (over-long text is cut with `truncated: true` and a `ref` back to the source message), `tool_calls` (name, status, recovery state, duration, bounded args, result digest, and a `ref` shaped as `session_id + message_index + tool_call_id`), `file_changes` (path, op, added/removed lines, `exact | partial | unknown` attribution), plus `compaction_boundary` and the message index range it came from. Compaction summaries are boundary markers, not facts: the turn they open carries no user text. A tool result interrupted by a restore keeps its persisted status but is flagged `result_unknown` — check that flag before reading it as success or failure. Refs may expire after the session is deleted or compacted; digests let you detect that. An empty `file_changes` list means "no recorded change", never "no change": shell effects without file metadata stay unattributed.

### Flags

| Flag                  | Description                                                        |
| --------------------- | ------------------------------------------------------------------ |
| `--out <path>`        | Write the JSONL projection to this file instead of stdout. A path inside the session directory — or a hard link to a file in it — is rejected, so a projection can never overwrite the session it reads |
| `--session-dir <path>`| Project this session directory directly instead of resolving `<session-id>` |
| `--max-bytes <n>`     | Raise or lower the JSONL size cap (bytes). A projection above the cap fails instead of being silently cut. `0` keeps the default (256 KiB) |

### Examples

```bash
chord sessions project 20260428064910975 > projection.jsonl
chord sessions project 20260428064910975 --out projection.jsonl
```

## `chord completion <shell>`

Generate a shell completion script for `bash`, `fish`, `powershell`, or `zsh`. Use the generated script according to your shell's normal completion loading rules.

```bash
chord completion zsh
chord completion bash
chord completion fish
chord completion powershell
```

## `chord help [command]`

Show command help. This is equivalent to passing `--help` to the command.

```bash
chord help
chord help doctor models
chord doctor models --help
```

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
- [ACP Agent Mode](./acp.md)
