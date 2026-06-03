# Changelog

This project follows Semantic Versioning-style releases. Before 1.0, releases may include breaking changes.

## Unreleased

### Features

- Added multimodal input support for PDFs across Gemini, Anthropic, OpenAI Responses, and OpenAI Chat providers, including TUI attachment chips and session recovery.
- Added the built-in `view_image` tool so models with image-input support can load local PNG/JPEG files into context using the same local-path permission handling as `read`.

### Improvements & Fixes

- Improved `edit` patch tolerance for blank context lines inside hunks, reducing failed model-generated edits.
- Fixed an OAuth credential refresh crash when the active auth state uses a negative credential-index sentinel.
- `@` file completion now treats supported image/PDF files as attachments, hides unsupported media types for the current model, and marks unsupported or encrypted attachments in the composer/transcript.
- Switching to a model without image/PDF input support now filters unsupported historical binary parts before provider requests while preserving historical tool-call structure.
- Fixed the info panel's context `Bytes` display so fresh sessions start at zero user context and restored sessions immediately show the same post-reduction size and savings estimate used for the next request.

## 0.6.2 - 2026-06-02

### Highlights

- Built-in tool names are now snake_case (`ApplyPatch` → `edit`, `WebFetch` → `web_fetch`, `TodoWrite` → `todo_write`, …) — update any permission rules that referenced the old names.
- Smarter context reduction that trims stale output sooner on long tool chains while staying prompt-cache friendly.

### Breaking Changes

- All built-in model-visible tool names changed to snake_case (for example `ApplyPatch` → `edit`, `WebFetch` → `web_fetch`, `TodoWrite` → `todo_write`). There are no compatibility aliases — update permission rules, hook tool filters, skill `allowed_tools`, and any saved integrations that used the old PascalCase names. Imported sessions still recognize source names like Codex `apply_patch` and map them to `edit`.

### Improvements & Fixes

- Context reduction now trims stale tool output sooner on long single-turn tool chains (age is measured by overall progress, not just later user messages), while warmup protection avoids re-trimming low-pressure prompts so prompt caching stays effective. `context.reduction` accepts `true` or `{}` for the default tuning and exposes finer keys (`cache_aware_min_usage`, `warmup_message_limit`, `min_incremental_saved_tokens`, `high_pressure_usage`, `force_prune_usage`); `context.reduction: false` is now rejected instead of silently using defaults.
- The sidebar's reduction savings now show the current request's total saved messages/bytes/tokens and stay visible after the turn goes idle.
- `edit` gives clearer guidance when a patch fails to apply — pointing out copied line-number gutters, indentation drift, stale file content, or mismatched function/class anchors.

## 0.6.1 - 2026-06-01

### Highlights

- New `edit` tool applies patch hunks to existing files, greatly reducing the exact-string match failures of the old `Edit`.
- YOLO mode: temporarily bypass permission prompts with `--yolo`, `/yolo on|off`, or `Ctrl+Y`.
- Git status sidebar showing branch, changed/staged/stash counts, and ahead/behind.
- More resilient Codex auth and quota handling, plus automatic recovery from WebSocket state errors.

### Breaking Changes

- **Permissions:** remembered permission rules now save into agent config files — project rules in `<project>/.chord/agents/<role>.yaml`, global rules in `<config-home>/agents/<role>.yaml` — instead of a separate permissions overlay. Rules previously written to `.chord/permissions/<role>.yaml` are no longer loaded; move any you still need. The built-in planner now allows `write`/`edit` only under `.chord/plans/*` by default.
- **Config:** the HTTP `User-Agent` override moved to a provider-level `user_agent`; the old Anthropic transport field was removed. Requests now default to `User-Agent: chord/<version>` unless overridden.
- **Config:** removed the unused `context.reduction.model_pool` and `maintenance.size_check_interval_hours` settings. Context reduction stays deterministic and does not call a model; use `context.compaction.model_pool` for LLM-backed compaction.
- **Config:** removed the `supports_fast` model field — migrate models to `supported_service_tiers: [fast]` (or omit it for preset/provider defaults).
- **Compatibility:** removed remaining pre-1.0 fallbacks — Codex import accepts only the current rollout schema, `--config` is no longer an alias for `--config-home`, and headless model switching accepts only `set_current_model_pool`.

### Features

- `edit` replaces the old `Edit` tool with native single-file patch hunks for localized changes (`write` still does full-file writes, `delete` removes whole files). It enforces read-before-edit, tolerates Codex `apply_patch` envelope markers (`*** Begin Patch` / `*** Update File:` / `*** End Patch`), and reports candidate lines when a hunk matches multiple places. If a file changed since you last read it, edits validate against the current content and back up risky overwrites under the session directory (capped per file and per session).
- YOLO mode (`--yolo`, `/yolo on|off`, `Ctrl+Y`) temporarily bypasses main-agent permission prompts; Handoff, Delegate, Cancel, and Done still require approval, and the status bar shows a YOLO pill while it's on.
- `/rules` now opens even when no rules exist and can add session/project/global allow/ask/deny rules; the remembered-rule picker lets you edit the suggested pattern before saving.
- Git status sidebar: a compact, collapsible Git summary (branch or detached commit, worktree name, changed/staged/stash counts, ahead/behind) that refreshes asynchronously without blocking rendering.
- LSP: Python diagnostics gain a quick `ruff` backend; large files use it automatically instead of blocking on full analysis. New top-level `diagnostics.*` config controls per-backend commands and output limits.
- Headless: a new `local_shell` command/event runs `!`-style local commands, and `Handoff` emits a structured `handoff_request` event with a `handoff` approve/deny command.
- Service tier (`/tier`, `Ctrl+R`) now propagates to SubAgents.
- Thinking translations persist per block to the session directory and restore on resume.
- Consistent mouse text selection across cards, viewers, and the composer (double-click selects a word, triple-click a line); `yy` copies a tool card as structured Markdown.
- Session import converts recognizable external tools (`read`, `shell`, `grep`, `glob`, `edit`, `write`, `delete`) to the closest Chord tool cards.

### Improvements & Fixes

- `@` file completion falls back to root-directory prefix matching, so files like `AGENTS.md` complete even when excluded from the Git index by `.gitignore`.
- `Grep` and `Glob` have lower default result caps and byte ceilings so broad searches don't crowd out more relevant context.
- Codex: access tokens must carry a parseable account ID, and token/refresh-token auth failures are classified as unrecoverable without redundant refresh attempts. `key_order: smart` treats only fully-used (100%) windows as exhausted — 99% still counts as usable — and compares short vs. weekly windows separately. WebSocket state-mismatch 400s now reset the chain and retry once as a full send; usage-limit errors skip the slow HTTP fallback and go straight to key/quota handling.
- Duplicate credential slots that share one access token now update cooldown, recovering, quota-exhausted, and success state together, so an exhausted token can't be re-selected through another slot.
- Compatible gateways: transient-looking 400s (e.g. "Concurrency limit exceeded") cool the key, rotate, and keep retrying; official-API request-shape 400s still stop immediately. API 400s are treated as model-level failures so the client can try another configured model.
- Resumed sessions repair structurally broken turns before sending to the provider, and orphan tool results without a matching tool call are dropped.
- OAuth slots are marked expired only after a real auth failure, not from local `expires` metadata; Codex runtime state reloads automatically when `auth.state.yaml` changes (so another Chord process's updates apply without a restart).
- Loop mode: `Done` no longer has a verification-status gate (open TODOs and active subagents still block); automatic and manual compaction now run in loop mode so long sessions can continue past the context budget. `/compact` runs in the background and applies at the next safe point, even mid-turn.
- The info panel's Bytes/Messages now reflect what will actually be sent to the model, with a post-reduction percentage.
- LSP diagnostics wait for fresh results after edits (fewer false positives from servers like gopls) and are capped to concise, priority-ordered blocks.
- Plan execution passes the plan as an `@<plan-path>` file mention instead of inlining it into the system prompt.
- Confirmation and `Question` deny reasons preserve your full text, including line breaks.
- TUI polish: `gg`/`G` move focus to the first/last card, restored tool cards keep their state and diffs, the `Ctrl+T` message directory renders inline with paging, completions show custom-command scope and `/tier` parity, overlays drop undocumented shortcut keys, and scrolling/focus recovery is smoother in large sessions.
- `chord cleanup sessions` also removes empty per-project session directories.
- `git stash show -p`, `git stash list --patch`, and other read-only stash subcommands are no longer blocked as interactive.

## 0.6.0 - 2026-05-20

### Highlights

- Request-time context reduction (`context.reduction`) keeps long sessions lean before compaction is needed.
- First-run setup wizard when `config.yaml` is missing.
- Loop mode uses `Done` (with a required completion report) as its single exit path.
- `chord import` brings in sessions from Claude Code, Codex, and OpenCode.
- `chord worktree finish` reworked to merge-then-squash, with a non-mutating `--check` preflight.

### Breaking Changes

- **Config:** renamed `context.compact_threshold` to `context.compaction.threshold`; there is no compatibility alias.
- **Config:** removed `context.auto_compact`. Automatic compaction is now enabled when `context.compaction.threshold > 0`; set it to `0` to disable.
- **Config:** removed `context.compact_model`. Compaction now only accepts `context.compaction.model_pool`; when unset, it clones the current agent model pool instead of a single model.
- **Headless:** removed the external `tool_result` event. Non-loop `Done` reports now use the dedicated `done_completion` event; loop-mode `Done` exit requests keep using `confirm_request` with explicit `done_report` / `done_reason` fields.

### Features

- Deterministic request-time context reduction under `context.reduction`, including stale tool-result pruning thresholds, kept off in loop mode.
- First-run setup wizard for the default `chord` command: it writes a minimal `config.yaml` (plus `auth.yaml` when needed), can complete Codex OAuth login, reuses matching existing credentials, and prints the exact paths it used.
- `Done` now requires a non-empty `report` with the full completion report. In loop mode it's the only exit path: premature calls are rejected back to the model, and valid exits open a confirmation dialog showing the report.
- `chord import` for external sessions from Claude Code, Codex, and OpenCode, writing a resumable session plus `import-report.json`.

### Improvements & Fixes

- Source builds and release artifacts now require Go 1.26.3+ (a patched toolchain that closes reachable standard-library vulnerabilities).
- OAuth account status now lives in `auth.state.yaml`, with a new `invalidated` status, and is kept out of `auth.yaml`.
- `chord worktree finish` first merges the target branch into the worktree branch to surface conflicts there, then squashes the result back onto the target as a single commit. `--check` previews that merge without touching the real worktree or target branch, and `finish` refuses to start if a rebase or merge is already in progress.
- Automatic input-method switching only runs for the foreground tab/window, so background tabs no longer clobber the active tab's IME.
- Hardened local file/path safety (rejecting device-style paths) and made config/auth writes atomic.
- Image paste de-duplicates key and terminal paste events so one paste no longer inserts two copies.
- A background confirmation alert keeps blinking the terminal title until you focus the window; fixed stale `TOKENS` in the info panel after compaction.

## 0.5.3 - 2026-05-11

### Features

- Replaced `chord test-providers` with `chord doctor models`: exact `provider/model[@variant]` checks, model-pool audits, all-model/all-pool modes, per-target timeouts, JSON output, and optional `--retry`.
- Project `.chord/config.yaml` now merges consistently across startup, auth login, and diagnostics; malformed project config is reported instead of silently ignored. New `stream_retry_rounds` can bound public LLM retry rounds for automation.

### Improvements & Fixes

- Resumed sessions rebuild durable `Read` file-state, so later `Edit`/`Write` keep the read-before-write safety check without falsely requiring every file to be re-read.
- When `limit.input` is omitted, compaction and model-pool fallback sizing reserve the output budget from `limit.context`, reducing oversized-prompt retries.
- `chord doctor models` reuses refreshed OAuth credential status across targets, avoiding stale-token false failures.
- Fixed Markdown preview syntax highlighting so trailing list/heading lines keep their color in `Read`/`Write` cards and fenced code blocks.

## 0.5.2 - 2026-05-11

### Breaking Changes

- Renamed the model-visible command tool from `Bash` to `Shell` (no runtime alias). Update permission rules (`permission.Shell`), hook tool filters, skill `allowed_tools`, saved/imported tool calls, headless consumers, and any prompts referring to the old `Bash` name before upgrading.

### Features

- `chord worktree finish --check`: a temporary isolated rebase preflight that shows whether the worktree would finish cleanly, without mutating the real worktree or leaving it mid-rebase.
- `Write` tool cards now show the written file as a numbered, syntax-highlighted preview, like `Read` cards.

### Improvements & Fixes

- Sidebar file list renamed `EDITED FILES` → `CHANGED FILES`; deleted files render struck-through with no fake `-1` line stat.
- Default shortcuts realigned: `Ctrl+P` is the model selector, the message directory moved to `Ctrl+T`, and the default `Ctrl+F` image-attach binding was removed (set `insert_attach_file` to restore it).
- API `402` quota/payment errors now behave like per-key rate limits — cool the exhausted key and try other keys before falling back.
- Non-interactive Shell/Spawn guardrails let plain `read`/`select` stdin proceed while still blocking TTY-dependent commands.
- Codex usage polling and OAuth browser/device-code logins cancel promptly on Ctrl+C or shutdown.
- Reduced Ghostty/cmux stale-cell artifacts after tab focus or resize recovery.

## 0.5.1 - 2026-05-09

### Features

- Manual MCP server control for `manual: true` servers: `/mcp` (`status`, `enable`, `disable`) and an MCP selector (`Ctrl+O`) to connect or disconnect on-demand servers at runtime. Auto-start servers stay read-only.

### Improvements & Fixes

- Fixed the initial LLM client not using the builder agent's full model pool, so first-request failures now correctly fall back across models, not just API keys.
- `Write` cards show a clear line/byte summary instead of a misleading "only a few lines shown" diff for full-file writes.
- Thinking translation tries the next model in the pool when one returns an empty result.
- `Bash` and `Spawn` reject high-confidence interactive commands and run with non-interactive defaults; timeout/cancel now escalates to force-kill so stuck child processes don't hang calls.
- Codex usage polling wakes proactively when WebSocket streams go quiet, keeping the RATE LIMIT panel current.
- Upgraded the Bubble Tea rendering stack and fixed Ghostty/cmux stale-cell artifacts after focus/resize recovery.

## 0.5.0 - 2026-05-08

### Features

- `chord import` for external sessions from Claude Code, Codex, and OpenCode, writing a resumable session plus `import-report.json`.
- Request-time model compatibility normalization safely replays or downgrades provider-specific history (Anthropic signed thinking, structured tools) when switching providers/models.

### Improvements & Fixes

- Agent configs use `mode: main` for MainAgent roles and `mode: subagent` for SubAgents (`sub_agent`/`sub` also accepted); hook `agent_kind` filters use `main`/`subagent`.
- Fixed a potential hang when a tool batch/turn is cancelled while waiting for the shared execution quota — Chord now emits a cancelled tool result so the UI can't get stuck.
- `chord worktree finish` gives step-by-step recovery commands on rebase conflicts and exits early if a rebase is already in progress.
- Fixed `/models` pool switching while busy so queued messages pick up the new pool at the next request boundary.
- Tool/Bash spinner animation advances one frame per tick (no skipped frames); background sessions keep the same cadence.
- Fixed THINKING card ordering when reasoning is followed quickly by assistant text.
- A background agent going busy → idle now shows a one-shot `✅` completion marker in the terminal title.

## 0.4.0 - 2026-05-07

### Highlights

- Git worktree support: `chord --worktree [name]` creates or enters an isolated worktree with its own sessions, cache, and exports.
- Google Gemini as a first-class provider.

### Features

- `chord --worktree [name]` (on the default command and `chord headless`) creates or enters a chord-managed git worktree, isolating sessions/cache/exports per worktree; combine with `--continue`/`--resume`. Auto-named when empty, and an existing worktree branch is reused.
- `chord worktree list` / `remove <name>` to manage worktrees, and `chord resume <session-id>` locates and resumes a session in whatever worktree it belongs to.
- `worktree.branch_prefix` config overrides the default `chord/` branch prefix (malformed refs are rejected at startup).
- Google Gemini as a first-class provider (`type: generate-content`): streaming text/tool/thinking output, inline images, function-calling tools, and `Retry-After` handling.

### Improvements & Fixes

- `session-meta.json` gains worktree fields; existing sessions stay compatible.
- Local slash commands (`/export`, `/models`) always run on the main event loop, fixing a stuck "busy" state when submitted during an LLM retry.
- Slash completion keeps the selected command visible when scrolling past 8 entries; `/new` clears the sidebar file list.

## 0.3.0 - 2026-05-07

### Highlights

- Runtime model pools: group models into named pools and switch the active pool at runtime via `/models` or the TUI selector.

### Breaking Changes

- Agent model configuration must now reference one or more top-level `model_pools`; the flat per-agent `models` list is no longer accepted. Every agent needs at least one pool, and the first listed pool is the default.

### Features

- Model pools with `/models` (`status`, `<pool>`, `--agent <name> <pool>`); switching while busy applies at the next request boundary without waiting for full idle.
- Build identity in diagnostics and `chord --version` (commit, dirty state, build/VCS time, Go version, executable mtime).

### Improvements & Fixes

- SKILLS sidebar no longer shows failed or unknown skills as loaded, and drops the legacy "(loaded)" suffix.
- Codex RATE LIMIT panel no longer sticks at "1s" after a window resets; it hides the timer and refreshes usage promptly.
- Deferred diagnostics/export status cards appear right after the current assistant block instead of waiting for idle.
- Fixed `Cmd+V` paste in permission-confirmation edit and deny-reason inputs.

## 0.2.0 - 2026-05-05

### Breaking Changes

- LSP `options` passed via `workspace/configuration` must now be shaped by section for every server (for Pyright, use nested `python` / `python.analysis` keys instead of flat top-level keys).
- Headless: removed the `notification` envelope type — render the ready/idle state from the `idle` envelope instead.
- SubAgent `Complete` dropped the `blockers_remaining` field; report non-blocking caveats via `remaining_limitations` and real blockers via Escalate or the `blocked` mailbox path.

### Features

- Pyright LSP auto-discovers project-local `.venv`/`venv`/`env` interpreters (Unix/WSL and Windows layouts) and normalizes relative interpreter paths against the LSP root.
- New session-scoped `SaveArtifact` / `ReadArtifact` tools and structured SubAgent completion handoff (files changed, verification run, remaining limitations, risks, follow-ups, artifact references).
- Loop `verify` assessments inject a dedicated `LOOP VERIFY` notice with explicit verification guidance; documented `/loop on [target]`.

### Improvements & Fixes

- Automatic (threshold-driven) compaction now continues working on the compacted context instead of going idle; manual `/compact` still returns to idle.
- More accurate session preview/title after compaction, using explicit metadata instead of inferring from text. (Sessions compacted by older versions may still show polluted titles until re-compacted.)
- Tool cards render as terminal-safe plain text (escaped ANSI, no false Markdown rendering); fixed background artifacts around emoji/ZWJ graphemes and inconsistent padding.
- Fixed `ee`/fork editing so images restored from session history are reloaded and re-sent; mouse drag-selection copy keeps the final character.
- Fixed long-session transcript clipping/drift that could hide the last cards or misalign mouse selection.
- Local-path tools (`Read`/`Write`/`Edit`/`Delete`/`Grep`/`Glob`) show project-relative paths when possible.
- Logs switched to plain `golog` output; the previous pseudo-structured `level=... msg=... key=value` format is no longer emitted.
- Additional Ghostty/cmux stale-display fixes after rapid scroll/resize/focus changes.

## 0.1.0 - 2026-04-29

- Initial public release of Chord.
- A terminal coding agent with a local-first runtime, Vim-like navigation, session management, model/provider configuration, tool execution, LSP integration, image input, and headless control support.
- Cross-platform release builds for macOS, Linux, and Windows.
