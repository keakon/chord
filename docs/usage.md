# Usage

This page covers Chord's main modes, core interactions, and the features you will use most often day to day.

## Modes

Chord has two main usage paths:

- **Local TUI**: the default mode, running MainAgent in the current process
- **Headless control plane**: `chord headless` exposes a stdio JSONL interface for external gateways/bots

Most personal development workflows should start with the local TUI.

## TUI basics

After startup, the input box is focused by default. Type a message and press `Enter` to send.

Tool cards show terminal-safe previews. File paths inside the session working directory are displayed as relative paths; external paths remain absolute. Long file and diff previews start collapsed: focus the card and press `Space`, `Enter`, or `o` to expand or collapse it.

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
- `Ctrl+E`: open the error panel (view all errors from the current session)
- `Ctrl+G`: export a diagnostics bundle
- `q`: press twice to quit
- `Ctrl+C`: press twice to quit

### Error panel

Press `Ctrl+E` in normal mode to open the error panel, which shows all errors encountered during the current session. This includes:

- **Intermediate retry errors**: API errors that triggered a key rotation, model fallback, or stream retry (e.g., 429 rate limits, 503 service unavailable, context length exceeded, timeouts). These are recorded silently and only appear in the error panel, keeping the conversation flow clean.
- **Final errors**: errors that exhausted all retries and appear as red error blocks in the conversation.

Each error record shows:

- Timestamp (HH:MM:SS)
- Provider and model (e.g., `Anthropic/claude-opus-4-8`)
- Masked API key label (e.g., `key=sk-a...xyz9`, showing a short prefix and suffix for safe identification)
- HTTP status code (when available)
- Error code and type (when provided by the API)
- Error message (wrapped to panel width)

Example error entry:

```text
14:25:38  Anthropic/claude-opus-4-8  key=sk-a...xyz9  HTTP 503  code=model_not_found
  No available channel for model sample/model under group default
```

Navigation:

- `j` / `k`: scroll one line
- `Ctrl+F` / `Ctrl+B`: page down / up
- `g` / `G`: jump to top / bottom
- `Esc`: close the panel

The error panel keeps the most recent 80 errors in a ring buffer (newest first). Use it to diagnose why a model fallback occurred or which keys are hitting rate limits.

## File mentions (`@path`)

Type `@` in the composer at the start of a line or after a space to open file completion.

- Bare `@` uses a cached workspace index of text files. That index includes tracked files and untracked non-ignored files, but skips Git-ignored paths, hidden directories, binary extensions, and common noise directories.
- Once you start typing a root-level filename prefix such as `@A`, Chord also checks the session working directory directly. This allows root files such as `AGENTS.md` to complete even when they were excluded from the cached index by `.gitignore` or local Git excludes.
- If the query already looks like a path, such as `@docs/`, `@./`, `@~/`, or `@.config/`, Chord switches to direct filesystem completion for that directory instead of staying on the cached index. This path-mode completion can surface ignored paths when you explicitly type toward them.
- Hidden entries stay hidden by default. To see them, make the query itself explicit, for example `@.`, `@.env`, `@./.`, or `@.config/`.
- Add a 1-based line suffix to include only part of a text file: `@path:42` injects line 42, and `@path:10-20` injects lines 10 through 20. Completion replaces only the path segment, so a suffix you typed is preserved when accepting a file match. If a real filename contains the numeric colon suffix, such as `note:12`, the filename takes precedence over line-range parsing.
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
- `/rename <title>`: set the current session's display title; bare `/rename` clears it

When exiting, if the current session can be resumed, Chord prints the corresponding resume command.

`/new` resets session state such as conversation history, todos, and usage. Runtime preferences such as the current model pool, service tier, and MCP state stay active until the process exits.

Custom titles are shown in the session picker and terminal title. They are metadata only: `/rename` does not change the session ID, directory, transcript, or resume command.

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

- Recognized external tool calls become readable Chord tool cards. Unsupported records remain visible as fallback text instead of being silently dropped.
- Imported tool cards represent history only. Re-run `read` before editing a file when you need current contents and stale-change protection.
- Signed Anthropic thinking is preserved. Other reasoning is omitted by default; use `--reasoning visible` to import it as plain text.
- Claude sidechain/sub-agent entries are excluded from the main session. Import warnings, skipped records, and conversion statistics are written to `import-report.json`.

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
- `chord worktree finish <name>`: update the worktree from the target branch, squash its result back as one commit, then remove the worktree and branch. Use `--onto <branch>` to choose the target or `--check` for a non-mutating conflict check. On conflict, the target branch stays unchanged and the worktree is left ready for you to resolve the merge and rerun `finish`.

Creating or entering a worktree changes the project Chord runs in. You can do that either with `chord --worktree <name>` or with `chord worktree <name>`. The `worktree` subcommand also owns management operations such as `list`, `remove`, and `finish`.

Worktrees live under `<state-dir>/worktrees/<repo-id>/<slug>` (outside the repository) and each gets its own project key, so sessions and caches are isolated automatically. Worktrees contain only tracked files; uncommitted changes in the main repository are not copied across.

## Local slash commands

These commands are handled by the local runtime and are not sent to the model as-is. In the TUI, type `/` to open completion; while the completion list is visible, `Tab` or `Enter` completes the selected command, and a later `Enter` runs or sends the completed command:

- `/new`: create a new session
- `/resume`: resume a session
- `/rename <title>`: set the current session's display title; bare `/rename` clears it without changing the session ID
- `/models`: view pool status or switch the current view's model pool (`main` view = current main role; `SubAgent` view = that agent)
- `/models --agent <name> <pool>`: directly set a named agent's pool
- `/mcp`: open the MCP server selector; `/mcp status` prints status; `/mcp enable|disable <server>` toggles manual servers. Runtime changes take effect for the next LLM request, not the currently in-flight request.
- `/compact`: manually trigger context compaction to summarize the current conversation as a structured archive; see [Context management — Compaction](./context-management.md#context-compaction)
- `/tier standard|fast|slow`: set the service tier for subsequent model requests (including later retry rounds that have not started yet). Bare `/tier` is not a status command; use the sidebar/status display for the current effective tier. If you enter a tier that the current provider/model does not support, Chord leaves the current tier unchanged and shows an error.
- `/yolo on|off`: temporarily bypass main-agent tool permissions while keeping handoff, delegate, cancel, and done permissions enforced. YOLO can be toggled while the agent is running; the execution-time permission bypass applies immediately to later tool calls, while the LLM-visible tool descriptions and permission prompt are refreshed on the next request.
- `/help`: toggle the in-app cheatsheet overlay (same as pressing `?` in Normal mode)

When a non-standard tier is active, the sidebar shows it. If a model switch makes the selected tier unavailable, it appears dimmed and struck through. `Ctrl+R` cycles only through tiers supported by the current provider and model.

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

### Reading the info panel `USAGE` block

- `Context` shows the actual input-side token burden reported for the most recent model request.
- `Bytes` and `Messages` describe the conversation context that will be sent to the model. After request-level context reduction runs, `Bytes` shows the current request's post-reduction context byte count followed by `↓` and the percentage saved relative to that request's unreduced context: `(bytes before reduction - bytes after reduction) / bytes before reduction`. This is not a cumulative value across requests; savings from frozen reduced summaries still count whenever those summaries are used in the current request. When a session is restored, Chord precomputes the same reduction for display, so `Bytes` starts at the post-reduction estimate instead of dropping after the next request; before any request surface can be prepared, it falls back to the current durable context estimate.
- `Bytes` counts the installed system prompt, message content, image payloads, and tool names/descriptions. It excludes JSON escaping overhead, tool-call argument JSON, thinking metadata, and request parameters such as stream settings or thinking budgets.
- These reductions are not persistent compaction: older tool results are usually replaced with shorter placeholder summaries for the request, while durable session history remains intact. `/compact`, automatic compaction, tool-output growth, and system prompt or tool-definition changes update the fallback durable estimate; new request preparation refreshes the actual sent request size, including while loop mode is active.
- When `Cache R` shows a percentage, it is cache-read tokens divided by input-side prompt tokens plus separately reported cache-write tokens. Output tokens are excluded because prompt caching applies only to the input side.
- `Think` appears only when the provider reports reasoning/thinking tokens. These tokens are already included in output-token billing; the line is a visibility breakdown, not an additional token bucket.

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

When the agent asks to finish, Chord checks the loop exit conditions and shows a local confirmation containing the completion report. Confirm to stop, or reject to keep the loop running. YOLO mode does not bypass this confirmation or the `done` permission.

The `done` tool remains on the same available tool surface when loop mode is toggled; `/loop` does not dynamically add or remove it. In normal mode, the agent must finish with a regular assistant response unless another explicit runtime or workflow requirement demands a tool-based completion signal. Merely completing the work or having `done` available does not justify calling it. Loop mode uses the current runtime's tool-call requirements and continuation instructions to make `done` the explicit exit request. Running `/loop off` returns subsequent work to normal response behavior and cancels any loop continuation that had not yet been sent to the model.

Loop mode also detects repeated identical tool calls. It interrupts a stalled sequence and, after repeated interceptions, asks whether to stop or continue.

A good pattern is:

1. turn on loop with a concrete target (`/loop on implement feature X with tests`)
2. give one complete instruction with success criteria
3. let the agent continue through edits, test failures, and follow-up fixes
4. only confirm the final `done` request when the work is actually complete

This reduces manual prompts such as “continue” or “run the tests too.” Do **not** use `/loop` just to keep the model running: normal mode is easier to control when the task is exploratory, ambiguous, or likely to need product decisions.

If the task is genuinely blocked, the agent can still report `<blocked>category: reason</blocked>`. You can always press `Esc` to cancel the current iteration.

**Status bar:** when enabled, the TUI status bar shows a `[↻]` marker.

**When to use:** multi-step tasks (generate code → write tests → debug → refine), iterative development. Not suitable for: one-shot queries or pure Q&A.

You can also define **custom** slash commands (per project or globally). See [Customization — Custom slash commands](./customization.md#custom-slash-commands).

## Multi-agent focus switching

Chord supports cooperation between MainAgent and SubAgents.

- `Tab`: cycle the main agent mode (role) shown in the status bar (main view only)
- `Shift+Tab`: cycle the focused agent view between the main agent and subagents

In a SubAgent view, you can inspect that agent's context and output and submit new input. Completed, failed, and cancelled states describe the previous turn; they do not make the view read-only.

Card numbers are local to the viewed agent: the main transcript and every SubAgent transcript each start at `#1` and advance independently. Switching agent views rebuilds that agent's complete available history, including earlier instances of a rehydrated delegated task, so `Ctrl+B` / `PgUp`, `gg`, search, and the message directory can navigate earlier cards instead of exposing only a live tail.

After resuming a session, every restored agent is idle rather than pretending work from the previous process is still active. Any previous SubAgent state—including waiting, completed, failed, and cancelled—can be continued manually: focus that SubAgent and submit an empty input to continue from its existing context, or submit text to start a follow-up turn. Chord first reacquires a SubAgent concurrency slot and marks it running. An empty input starts a new turn without appending a synthetic user message. Mailboxes restored from the session remain queued while idle and are delivered only as part of this explicit manual continue or input action; mailbox events produced during normal live execution are dispatched immediately to the owning agent.

When `todo_write` is enabled but no `delegate` workflow is available, the todo list normally keeps a single `in_progress` item that represents the MainAgent's current directly executed focus.
When `delegate` workflow is available and multiple delegated workstreams are genuinely active, the todo list may contain multiple `in_progress` items, but each one should map clearly to a real live delegated workstream and use a unique `active_form` rather than work that is only planned, blocked on prerequisites, or merely waiting to start.

## Images and PDFs

Currently supported:

- Attach images and PDFs from the system clipboard with `Ctrl+V` or `Alt+V`
- Attach image and PDF files to the currently focused agent's message when the active model supports that input type
- View images directly in supported terminals; PDFs are sent to the model and shown as file chips in the transcript, but are not previewed inline
- Edit historical user messages that contain images or PDFs; tail messages reopen in the current session, while earlier messages fork a new session, and path-restored attachments are reloaded when the edited message is sent again
- Let the model use the built-in `view_image` tool to load a local PNG/JPEG into context when the tool is permitted, the first model in the effective model pool supports image input, and that first model does not use the OpenAI Chat Completions API. The tool uses the same local-path permission handling as `read`.

`view_image` availability follows the first model in the effective pool. For OpenAI models, use the Responses API when tools need to return images or files; Chat Completions accepts images in user messages but not in tool results. After an image/PDF tool result enters the conversation, Chord skips fallback models that cannot replay it safely.

Common actions:

- `Ctrl+V` or `Alt+V` in the main composer: asynchronously read an image or PDF from the system clipboard. PNG/JPEG are accepted directly; BMP/WebP are normalized to PNG/JPEG. Images get an inline placeholder such as `[image1.png]`; PDFs are added as file attachments. Pressing Enter while the read is pending asks you to wait, so an immediate submit cannot lose the attachment. Use `Alt+V` in Windows Terminal and WSL sessions hosted by it.
- `Cmd+V`, right-click paste, menu paste, and other terminal paste events: paste text only and never inspect clipboard attachments.
- `Cmd+V` in confirmation-dialog text fields: paste text only.
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
- [Edit tools](./edit-tools.md)
- [Customization](./customization.md)
- [Troubleshooting](./troubleshooting.md)
