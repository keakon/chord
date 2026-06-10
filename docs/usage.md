# Usage

This page covers Chord's main modes, core interactions, and the features you will use most often day to day.

## Modes

Chord has two main usage paths:

- **Local TUI**: the default mode, running MainAgent in the current process
- **Headless control plane**: `chord headless` exposes a stdio JSONL interface for external gateways/bots

Most personal development workflows should start with the local TUI.

## TUI basics

After startup, the input box is focused by default. Type a message and press `Enter` to send.

Tool-call cards try to keep file paths concise: for file tools such as `read`, `edit`, `write`, and `delete`, paths inside the current working directory are shown as relative paths in the TUI, while paths outside that directory remain absolute.

The sidebar and info panel changed-file lists prioritize showing full `+N -N` line-change stats. When space is tight, filenames are truncated or omitted instead of cutting off the counts.

Tool arguments and results are displayed as terminal-safe plain text. Chord escapes embedded ANSI/control sequences from external output instead of executing them as terminal styling, and generic tool results that look like Markdown remain literal output rather than being reformatted as assistant Markdown.

Discovery tools use stable LLM-facing output caps before results enter session history: `grep` returns at most 120 matches and 12 KiB of text; `glob` returns at most 250 paths and 16 KiB of text. These caps are fixed rather than based on the current remaining context window, so the same tool call stays reproducible across model switches and unrelated history growth. The byte caps are the primary guard because context pressure tracks bytes/tokens more closely than line count: using Chord's rough `1 token ~= 3 bytes` estimate, 12 KiB keeps one Grep result around 4k tokens, while 16 KiB keeps one Glob result around 5.3k tokens. The match/path caps are secondary guards against floods of very short lines.

`read` and `write` tool cards show file contents as numbered, syntax-highlighted previews. `edit` cards render unified diffs with syntax highlighting when the file type is known; `.mdx` files fall back to Markdown highlighting, and red/green diff line backgrounds remain visible even for unsupported extensions. Long previews show the first 10 lines by default with a `[space] toggle expand/collapse` hint; focus the card and press `Space`, `Enter`, or `o` to expand or collapse it.

When Chord is running in the background, the terminal title shows a one-shot `✅` completion marker when the focused agent transitions from busy to idle. Focusing the terminal clears the marker; ordinary tab/window focus changes do not re-add it unless new background work later completes.

Common keys:

- `Esc`: switch to Normal mode; pressing `Esc` again in the running main view cancels the current turn
- `i`: return to insert/input mode
- `j` / `k`: move between message cards
- `gg` / `G`: jump to top / bottom
- `/`: search messages
- `Ctrl+T`: open the message directory
- `Ctrl+P`: switch the main role model pool
- `Ctrl+O`: open the MCP server selector
- `Ctrl+G`: export a diagnostics bundle
- `q`: press twice to quit
- `Ctrl+C`: press twice to quit

## Tool execution details

### Speculative (early) tool execution

To shorten the wait during a provider's finalize phase, Chord executes a small safe subset of tools *speculatively* while the model response is still streaming, as soon as a tool call's arguments are complete. This is always on and not configurable.

- Eligible: `read`, `grep`, `glob`, rollback-safe file mutations (`write`, `edit`, `delete`), and a conservative read-only `shell` subset (single command, no pipes/redirects/`&&`/`;`): `pwd`, `ls`, `cat`, `which`, and `git status|log|diff|show|branch|rev-parse`.
- Not eligible: non-read-only `shell`, interactive/control tools, or any call whose permission action is `ask`.
- Speculative file mutations are real on-disk writes/deletes, but Chord captures pre-state first and rolls them back if finalize discards the call. Conflicting speculative mutations on the same path within a turn are skipped for the finalized path, and read-like speculation is skipped while any unpromoted speculative mutation exists in the turn, so it never observes uncommitted state.
- Speculative results may show early in the UI, but they are only appended to the conversation context after finalize validation.

### How `edit` applies patches

`edit` takes the target file as a structured `path` argument; its `patch` argument carries hunk text (`@@` headers, leading-space context lines, `-` removed, `+` added). Stray Codex `apply_patch` envelope lines (`*** Begin Patch` / `*** End Patch`, and a leading `*** Update File:` matching `path`) are stripped; add/delete/move, multi-file patches, and mismatched update paths are rejected.

Matching is Codex-style and ordered: each hunk (and any attached `@@` function/class/test header) matches the first occurrence after the current search position. When a hunk matches multiple candidates, Chord applies the first and reports the matched line plus other candidate lines so the model can re-read if needed. A hunk with no context/removal lines fails because there is no insertion point.

While arguments stream, the `edit` card follows `write`-style path display: no path until Chord parses the structured `path`, then the path appears in the card header.

## File mentions (`@path`)

Type `@` in the composer at the start of a line or after a space to open file completion.

- Bare `@` uses a cached workspace index of text files. That index includes tracked files and untracked non-ignored files, but skips Git-ignored paths, hidden directories, binary extensions, and common noise directories.
- Once you start typing a root-level filename prefix such as `@A`, Chord also checks the current working directory directly. This allows root files such as `AGENTS.md` to complete even when they were excluded from the cached index by `.gitignore` or local Git excludes.
- If the query already looks like a path, such as `@docs/`, `@./`, `@~/`, or `@.config/`, Chord switches to direct filesystem completion for that directory instead of staying on the cached index. This path-mode completion can surface ignored paths when you explicitly type toward them.
- Hidden entries stay hidden by default. To see them, make the query itself explicit, for example `@.`, `@.env`, `@./.`, or `@.config/`.
- Completion is only input assistance. When you send the message, Chord reparses the final `@path` text; if you removed the mention before sending, that file is not attached.

## Sessions

Chord keeps persistent sessions for the current project.

Common workflows:

- `chord`: create a new session
- `chord --continue`: resume the most recent non-empty session for this project
- `chord --resume <session-id>`: resume a specific session
- `chord resume <session-id>`: resume a session by ID, auto-locating the chord-managed worktree it belongs to
- `chord import <source> [file]`: import an external session into Chord's session store
- `/new`: create a new session in the TUI
- `/resume`: pick a historical session in the TUI

When exiting, if the current session can be resumed, Chord prints the corresponding resume command.

### Importing external sessions

Chord can import an external agent session into a resumable Chord session.

Currently supported sources:

- `opencode`: JSON from `opencode export <sessionID>`
- `codex`: Codex rollout JSONL (typically under `~/.codex/sessions/**/rollout-*.jsonl`)
- `claude`: Claude Code transcript JSONL (typically under `~/.claude/projects/**/<sessionId>.jsonl`)

Example (OpenCode):

```bash
# OpenCode
opencode export <sessionID> > export.json
chord import opencode export.json
chord resume <sid>

# Codex (direct file)
chord import codex ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl

# Codex (by session id)
chord import codex --id <session-id> [--root ~/.codex/sessions]

# Claude Code (direct file)
chord import claude ~/.claude/projects/**/<sessionId>.jsonl

# Claude Code (by session id)
chord import claude --id <session-id> [--root ~/.claude/projects]
```

Notes:

- **Tools**: Chord always converts recognizable external tool calls to the closest current Chord tool card when their arguments can be normalized: `read`, `shell`, `grep`, `glob`, `edit`, `write`, and `delete` are used for matching Codex, Claude Code, and OpenCode records. Only unknown, malformed, or unsupported source records (no Chord mapping, missing call id, or un-normalizable arguments) remain visible as readable fallback cards instead of being dropped. Imported provenance is retained internally, so these converted cards are transcript/history only and do not restore Chord FileTracker snapshots; re-run `read` when you need fresh file context or stale-change warnings before editing imported files.
- **Reasoning**: Chord only imports Anthropic signed thinking as `thinking_blocks`. Non-signed reasoning is dropped by default (`--reasoning strict`); use `--reasoning visible` to include it as plain text.
- **Claude main-session reconstruction**: Claude imports rebuild the best-effort main non-sidechain conversation span instead of simply choosing the latest raw leaf. Compact boundaries participate in reconstruction, but are not rendered as ordinary transcript messages.
- **Claude sidechains**: sidechain / sub-agent transcript entries are excluded from the main imported session by default. When present, CLI output reports the skipped count, and `import-report.json` records Claude-specific diagnostics plus sidechain agent IDs when available.
- **Claude fallback rendering**: visible Claude artifacts without a safe Chord mapping are imported as readable fallback assistant text blocks when possible, rather than raw JSON blobs.
- The imported session contains an `import-report.json` with conversion warnings and stats.
- During runtime, Chord normalizes persisted history into a provider-safe wire view per request, so switching providers/models after import does not replay incompatible payloads.

Common flags:

- `--project <path>`: which project to write into (default: current directory)
- `--sid <id>`: specify session id (default: auto-generated)
- `--id <session-id>`: import by source session id instead of file path (supported for `codex` and `claude`)
- `--root <path>`: root directory for `--id` lookup
- `--reasoning off|visible|strict`: reasoning import policy (default: `strict`)
- `--dry-run`: parse and report only, no writes
- `--json`: machine-readable output
- `--force`: overwrite an existing `--sid`

## Worktrees

For working on multiple tasks in parallel without crosstalk, Chord can create and run inside dedicated git worktrees:

- `chord --worktree`: create or enter a chord-managed worktree (auto-named when no name is given)
- `chord --worktree feat-auth` / `chord worktree feat-auth`: create or enter the worktree named `feat-auth` (branch `chord/feat-auth`); combine with `--continue` or `--resume` to act on the worktree's own session history
- `chord headless -d <repo> --worktree feat-auth`: same in headless mode; the `ready` event payload includes the worktree's `name`, `branch`, `path`, and `repo_root`
- `chord worktree list`: list chord-managed worktrees of the current repository
- `chord worktree remove <name>`: delete the worktree and its sessions/cache/exports; the branch is preserved by default. Pass `--delete-branch` to delete only-if-merged or `--force` to force-remove a dirty worktree and its branch.
- `chord worktree finish <name>`: first merge the main-line target branch into the real worktree branch (default target: the main worktree's current branch), then squash the finished worktree state back onto that target branch as one commit, fast-forward the target branch to include it, and finally remove the worktree and delete its branch. Use `--onto <branch>` to pick the target branch, or `--check` to preview whether that target branch can merge cleanly into the worktree in a temporary worktree without mutating the real one. If that merge would hit conflicts, `finish` reports the conflicted files, keeps the target branch unchanged, and leaves the real worktree in that merge so you can resolve it and rerun. If a rebase or merge is already in progress in that worktree, `finish` exits early with an explicit “complete that operation first” hint.

Creating or entering a worktree changes the project Chord runs in. You can do that either with `chord --worktree <name>` or with `chord worktree <name>`. The `worktree` subcommand also owns management operations such as `list`, `remove`, and `finish`.

Worktrees live under `<state-dir>/worktrees/<repo-id>/<slug>` (outside the repository) and each gets its own project key, so sessions and caches are isolated automatically. Worktrees contain only tracked files; uncommitted changes in the main repository are not copied across.

## Local slash commands

These commands are handled by the local runtime and are not sent to the model as-is. In the TUI, type `/` to open completion; while the completion list is visible, `Tab` or `Enter` completes the selected command, and a later `Enter` runs or sends the completed command:

- `/new`: create a new session
- `/resume`: resume a session
- `/models`: view pool status or switch the current view's model pool (`main` view = current main role; `SubAgent` view = that agent)
- `/models --agent <name> <pool>`: directly set a named agent's pool
- `/mcp`: open the MCP server selector; `/mcp status` prints status; `/mcp enable|disable <server>` toggles manual servers. Runtime changes take effect for the next LLM request, not the currently in-flight request.
- `/compact`: manually trigger context compaction to summarize the current conversation as a structured archive; see [Configuration — Context compaction](./configuration.md#context-compaction)
- `/tier standard|fast|slow`: set the service tier for subsequent model requests (including later retry rounds that have not started yet). Bare `/tier` is not a status command; use the sidebar/status display for the current effective tier. If you enter a tier that the current provider/model does not support, Chord leaves the current tier unchanged and shows an error.
- `/yolo on|off`: temporarily bypass main-agent tool permissions while keeping handoff, delegate, cancel, and done permissions enforced. YOLO can be toggled while the agent is running; the execution-time permission bypass applies immediately to later tool calls, while the LLM-visible tool descriptions and permission prompt are refreshed on the next request.
- `/help`: toggle the in-app cheatsheet overlay (same as pressing `?` in Normal mode)

When a non-standard tier is actually active for the current provider/model, the sidebar/status area shows it normally. If a previously requested tier becomes unsupported after switching provider/model, the info panel still shows the requested tier in a dim strikethrough style so it remains visible but clearly ineffective. `Ctrl+R` skips unsupported tiers and cycles only through the tiers available to the current provider/model. Slash completion for `/tier` predicts the same next tier as `Ctrl+R`; when the only available tier is the already-active `standard`, `/tier` is omitted from slash completions.

The following commands have more interactive detail, expanded below.

### MCP selector

Press `Ctrl+O` to open the MCP server selector. It lists configured MCP servers, their connection state, and whether manual servers are currently enabled or disabled. Use `j` / `k` to move, `Enter` to toggle the selected manual server, `e` to enable, `d` to disable, and `Esc` to close.

The selector can be opened while the agent is running so you can inspect MCP state without waiting for the current turn to finish. Enable/disable actions are also allowed while running, but they are deferred: the current in-flight request keeps the MCP tool surface and prompt it started with, and the changed MCP state is reflected in the next LLM request. Auto-start MCP servers are always read-only in this selector; only servers configured with `manual: true` can be changed at runtime.

### `/export` — export the current session

Export the current session as Markdown (default) or JSON.

```text
/export                  # default: export as Markdown into the session artifacts directory
/export ~/out.md         # specify an output path
/export --json           # export as JSON
/export ~/out.json       # a .json path auto-selects JSON
```

The export includes every conversation message plus the current session usage statistics. On success, the TUI displays the saved path.

In the TUI info panel's `USAGE` block, `Context` shows the actual input-side token burden reported for the most recent model request. `Bytes` and `Messages` describe the conversation context that will be sent to the model: after request-level context reduction runs, `Bytes` shows the post-reduction request byte count followed by `↓` and the saved percentage. When a session is restored, Chord precomputes the same request-level reduction for display so `Bytes` starts with the post-reduction estimate instead of dropping after the next request; before any request surface can be prepared, it falls back to the current durable context estimate. `Bytes` counts the installed system prompt, message content, image payloads, and tool names/descriptions, while excluding JSON escaping overhead, tool-call argument JSON, thinking metadata, and request parameters such as stream settings or thinking budgets. These reductions are not persistent compaction: older tool results are usually replaced with shorter placeholder summaries for the request, while durable session history remains intact. `/compact`, automatic compaction, tool-output growth, and system prompt or tool-definition changes update the fallback durable estimate; new request preparation refreshes the actual sent request size, including while loop mode is active.

In the info panel's `USAGE` block, `Think` appears only when the provider reports reasoning/thinking tokens. These tokens are already included in output-token billing; the line is a visibility breakdown, not an additional token bucket.

### `/stats` — usage statistics overlay

Opens an overlay to browse usage data along two axes:

- **Scope**: `Session` (current session) or `Project` (aggregate project stats). Press `s` to toggle.
- **View**: `Overview`, `Models` (per-model breakdown), `Agents` (per-agent breakdown). Project scope additionally supports `Dates` (per-day breakdown). Press `Tab` / `Shift+Tab` to switch views.

Session Overview shows: LLM calls, input/output tokens, cache read/write tokens, reasoning tokens, and estimated cost. Models and Agents views display detailed per-dimension tables.

Project statistics are auto-aggregated from local sessions directories, with five time ranges: `today`, `7d`, `30d`, `90d`, `all`. Switching to Project may briefly show "loading" before data appears.

Any active search is automatically cleared when the overlay opens. Press `Esc` to close. You can also open it directly via the `$` key in Normal mode.

### `/rules` — permission rule manager

Opens an overlay for remembered permission rules. It opens even when no rules have been added yet, so you can add one manually.

- `a`: add a rule manually
- `↑` / `↓` or `j` / `k`: move cursor
- `d`: delete the current rule
- `o`: open the rule's backing config file in the OS editor
- `Esc` / `q`: close

When adding a rule manually, enter the tool name and pattern, then use `Ctrl+S` to cycle scope (`session` / `project` / `global`) and `Ctrl+A` to cycle action (`allow` / `ask` / `deny`). Tool and pattern are required. Patterns that do not match future tool calls are accepted but have no effect until they match.

Each rule shows its scope (`session` / `project` / `global`) and on-disk file path. `session` rules apply only to the current session; `project` rules are written to the current project's `.chord/agents/<role>.yaml`; `global` rules are written to the user config directory's `agents/<role>.yaml` (default: `~/.config/chord/agents/<role>.yaml`). These rules directly update the target agent's `permission` config, and deleting a rule removes it from the same agent config file.

The confirmation popup also supports adding a remembered rule with `M`. In the rule picker, press `E` to edit the suggested pattern before saving. Delete confirmations use conservative path-specific suggestions (exact paths and same-directory patterns) instead of a global wildcard.

### `/loop` — continuous execution mode

Continuous execution mode keeps the agent working after each round without you having to nudge it. Suitable for one-shot instructions like "implement feature X" — you send one message and the agent iterates, verifies, and pushes through until the work is done, genuinely blocked, or you explicitly confirm exit.

`/loop` is available only when the current MainAgent role can use the `done` tool. If `done` is denied or hidden for that role, `/loop` is unavailable.

Enabling:

```text
/loop on                           # enable; agent will try to finish all remaining tasks in the session
/loop on implement user auth       # enable with a specific task target
/loop off                          # return to normal mode
/loop                              # show current state
```

The text after `/loop on` is the task target sent to the agent. When omitted, it defaults to "Continue and finish all remaining tasks in the current session." Each enable defaults to 10 max iterations; exceeding that stops automatically.

**How it works:** after `/loop on`, send a task instruction (e.g. "implement user auth"). The agent follows this cycle:

1. **executing**: carrying out the task, calling tools to do real work
2. **assessing**: evaluating current progress, deciding the next step
3. **verifying**: running checks (tests, lint, etc.)
4. **continue or request exit**: if more work remains, the agent keeps going; if it believes the loop can stop, it must request exit through the `done` tool

When `done` is requested before the loop exit conditions are satisfied, Chord rejects that request and automatically makes the agent continue. When the exit conditions are satisfied, Chord shows a local confirmation dialog instead of stopping immediately. The `done` tool must include a non-empty `report` argument containing the final completion report, and that report is what the confirmation dialog shows. While the report argument is still streaming, the Done tool card shows the same live `chars received` progress as other streaming tool arguments; once the argument stream finishes, that temporary progress indicator is hidden. If you confirm exit, loop mode stops and the agent becomes idle; otherwise the loop keeps running.

`done` is deliberately treated as loop control rather than an ordinary permission-bypassable tool. `/loop` is available only when the active MainAgent role can use `done`, and YOLO does not override `done` permissions. This keeps roles that are not allowed to finish/exit from silently taking over loop termination, and it preserves the local confirmation gate that prevents premature completion.

Loop mode also guards against a stalled tool-call loop. If the MainAgent emits the same tool call three times in a row — same tool name and identical arguments — Chord rejects that tool result automatically, injects guidance to stop repeating the unchanged call and continue toward the loop target, and counts it as one loop interception. The check uses a sliding window: if the fourth call is still identical, it is rejected again immediately. Once the loop interception limit is reached, Chord shows the same local confirmation flow so you can decide whether to stop or continue.

Runtime-injected user continuation messages are used only after a terminal assistant turn that ended with an `end_turn` / `stop` / `done`-style stop reason and no tool calls. If the model already returned tool calls in that turn, loop continuation stays inside the tool-call flow: Chord records tool results, updates loop state, and may show loop guidance in the TUI, but it does not append a synthetic user message unless you manually send one.

**Using `/loop` + `done` to get more value from Codex quota:** loop mode does not create extra quota, but it helps spend existing quota on end-to-end progress instead of repeated human re-prompting. In normal mode, the model often stops after one local milestone and waits for your next message; that burns another turn later just to say "continue", rerun checks, or pick up unfinished cleanup. With `/loop`, the agent keeps iterating inside the same task until it either reaches a real stop condition or asks to exit through `done`. The `done` gate matters here: it prevents premature stopping, so the agent is pushed to finish the whole chain — implement → test → fix failures → verify again → summarize — before giving control back.

A good pattern for Codex-heavy work is:

1. turn on loop with a concrete target (`/loop on implement feature X with tests`)
2. give one complete instruction with success criteria
3. let the agent continue through edits, test failures, and follow-up fixes
4. only confirm the final `done` request when the work is actually complete

This usually improves quota efficiency for multi-step coding tasks because fewer turns are wasted on manual nudges like "continue", "run the tests too", or "fix the failing case and try again". Do **not** use `/loop` just to keep the model running aimlessly; if the task is exploratory, ambiguous, or likely to need frequent product decisions, normal mode is often cheaper and easier to control.

If the task is genuinely blocked, the agent can still report `<blocked>category: reason</blocked>`. You can always press `Esc` to cancel the current iteration.

**Status bar:** when enabled, the TUI status bar shows a `[↻]` marker.

**When to use:** multi-step tasks (generate code → write tests → debug → refine), iterative development. Not suitable for: one-shot queries or pure Q&A.

You can also define **custom** slash commands (per project or globally). See [Customization — Custom slash commands](./customization.md#custom-slash-commands).

## YOLO and protected control tools

YOLO is a convenience mode for trusted local work: it bypasses ordinary MainAgent permission checks so tools such as file edits, reads, shell commands, and web requests can run without repeated confirmations. It does **not** bypass permissions for `handoff`, `delegate`, `cancel`, or `done`.

Those four tools are protected because they control agent orchestration rather than just local side effects:

- `handoff` can transfer work/plans between roles, so it changes who is responsible for the task.
- `delegate` can start or manage delegated workstreams and may run work in parallel.
- `cancel` can interrupt the active turn.
- `done` completes a turn or requests loop exit and carries the final report.

Keeping these permissions enforced under YOLO prevents a broad "allow tools" switch from also granting workflow-control powers. In loop mode this matters especially for `done`: loop exit remains gated by the active role's `done` permission, loop exit-condition checks, and local confirmation, so YOLO cannot accidentally let the model terminate a long-running loop early.

Under YOLO, these protected tools still need explicit permissions. A broad default such as `"*": allow` is treated as part of the bypassed ordinary permission surface and does not by itself grant `handoff`, `delegate`, `cancel`, or `done`; configure those tools directly when a role should use them.

## Multi-agent focus switching

Chord supports cooperation between MainAgent and SubAgents.

- `Tab`: cycle the main agent mode (role) shown in the status bar (main view only)
- `Shift+Tab`: cycle the focused agent view between the main agent and subagents

In a SubAgent view, you can inspect that agent's context and output. Finished SubAgent views are read-only.

When `todo_write` is enabled but no `delegate` workflow is available, the todo list normally keeps a single `in_progress` item that represents the MainAgent's current directly executed focus.
When `delegate` workflow is available and multiple delegated workstreams are genuinely active, the todo list may contain multiple `in_progress` items, but each one should map clearly to a real live delegated workstream and use a unique `active_form` rather than work that is only planned, blocked on prerequisites, or merely waiting to start.

## Images and PDFs

Currently supported:

- Paste images from the clipboard
- Attach image and PDF files to the currently focused agent's message when the active model supports that input type
- View images directly in supported terminals; PDFs are sent to the model and shown as file chips in the transcript, but are not previewed inline
- Edit historical user messages that contain images or PDFs; tail messages reopen in the current session, while earlier messages fork a new session, and path-restored attachments are reloaded when the edited message is sent again
- Let the model use the built-in `view_image` tool to load a local PNG/JPEG into context when the tool is permitted, the first model in the effective model pool supports image input, and that first model does not use the OpenAI Chat Completions API. The tool uses the same local-path permission handling as `read`.

`view_image` availability is decided from the first model in the effective model pool so the tool surface stays stable when fallback routing changes models. For OpenAI models, prefer the Responses API when tools need to return images or files: OpenAI Chat Completions can accept images in user messages, but its `role: "tool"` messages are text-only. Once a conversation contains image/PDF tool results, Chord skips fallback candidates that cannot safely replay them, including models without image input support and OpenAI Chat Completions. Conversations that have not used image/PDF tool results can still fall back to Chat Completions normally. Tool-returned images appear in the TUI as thumbnails on the corresponding tool result card and can be opened from Normal mode like user-attached images.

Common actions:

- `Ctrl+V` / `Cmd+V` in the main composer: prefer clipboard image input. If an image is found, Chord adds it as an attachment and inserts a placeholder like `[image1]` at the cursor; any pasted text provided by the terminal is inserted after the placeholder. If no image is found, Chord pastes text. After paste completes, the cursor is kept immediately after the inserted placeholder/text.
- `Ctrl+V` / `Cmd+V` in confirmation-dialog text fields: always paste text, never attach images
- Inline image attachments are capped at 5 per composer message
- Typing literal placeholder text such as `[image1]` does not attach an image by itself; only Chord-inserted inline image placeholders are attachment-backed
- `@` file completion includes image/PDF files only when the current model supports that input type. Manually typed image/PDF `@` references are still accepted as attachments; unsupported attachments are ignored at send time with a warning.
- To attach an image or PDF by path, enter the path in the composer and configure a custom `insert_attach_file` key binding
- PDFs that appear to be encrypted are marked with a warning; Chord still allows sending them because provider-side parsing is authoritative.
- `Enter` / `o` / `Space`: open the image in the current user message or tool result in Normal mode

## Copying text

- Drag in the transcript to select text inside the TUI
- `yy` copies the focused message card; tool cards are copied as Markdown with `# Tool call`, `## Arguments`, `## Result`, and `## Diff` sections. Done rejection reasons are copied in a separate `## Rejection reason` section.
- `Cmd+C`: copy the current transcript selection in macOS terminals that forward the key to Chord; when a confirmation dialog input is focused, copies that input instead
- `Ctrl+C`: remains reserved for cancel / quit and is not used for transcript copy

## Headless

`chord headless` is useful for:

- bot / gateway integration
- automation scripts
- external control-plane access without a local TUI

It uses:

- stdin: one JSON command per line
- stdout: one JSON event per line

See [Headless](./headless.md) for details.

## Daily usage tips

- Start with a minimal provider config to confirm requests work
- Add LSP when you need stronger code awareness
- Add MCP or Hooks only when you need external tool integration
- Keep high-risk tools as `ask`; do not globally `allow` them by default

## Related

- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Customization](./customization.md)
- [Troubleshooting](./troubleshooting.md)
