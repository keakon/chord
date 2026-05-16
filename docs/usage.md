# Usage

This page covers Chord's main modes, core interactions, and the features you will use most often day to day.

## Modes

Chord has two main usage paths:

- **Local TUI**: the default mode, running MainAgent in the current process
- **Headless control plane**: `chord headless` exposes a stdio JSONL interface for external gateways/bots

Most personal development workflows should start with the local TUI.

## TUI basics

After startup, the input box is focused by default. Type a message and press `Enter` to send.

Tool-call cards try to keep file paths concise: for file tools such as `Read`, `Write`, and `Edit`, paths inside the current working directory are shown as relative paths in the TUI, while paths outside that directory remain absolute.

Tool arguments and results are displayed as terminal-safe plain text. Chord escapes embedded ANSI/control sequences from external output instead of executing them as terminal styling, and generic tool results that look like Markdown remain literal output rather than being reformatted as assistant Markdown.

`Read` and `Write` tool cards show file contents as numbered, syntax-highlighted previews. `Edit` cards render unified diffs with syntax highlighting when the file type is known; `.mdx` files fall back to Markdown highlighting, and red/green diff line backgrounds remain visible even for unsupported extensions. Long previews show the first 10 lines by default with a `[space] toggle expand/collapse` hint; focus the card and press `Space`, `Enter`, or `o` to expand or collapse it.

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

- **Tools**: by default, Codex/OpenCode tool calls/results are imported as plain text to avoid cross-provider tool protocol issues. Claude tool history uses `--tool-mode auto` by default: it keeps structured tool calls only when signed thinking is present; otherwise it downgrades to text.
- **Reasoning**: Chord only imports Anthropic signed thinking as `thinking_blocks`. Non-signed reasoning is dropped by default (`--reasoning strict`); use `--reasoning visible` to include it as plain text.
- The imported session contains an `import-report.json` with conversion warnings and stats.
- During runtime, Chord normalizes persisted history into a provider-safe wire view per request, so switching providers/models after import does not replay incompatible payloads.

Common flags:

- `--project <path>`: which project to write into (default: current directory)
- `--sid <id>`: specify session id (default: auto-generated)
- `--id <session-id>`: import by source session id instead of file path (supported for `codex` and `claude`)
- `--root <path>`: root directory for `--id` lookup
- `--tool-mode auto|text|structured`: tool import policy (default depends on source)
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
- `/mcp`: open the MCP server selector; `/mcp status` prints status; `/mcp enable|disable <server>` toggles manual servers while idle
- `/compact`: manually trigger context compaction
- `/help`: toggle the in-app cheatsheet overlay (same as pressing `?` in Normal mode)

The following commands have more interactive detail, expanded below.

### MCP selector

Press `Ctrl+O` to open the MCP server selector. It lists configured MCP servers, their connection state, and whether manual servers are currently enabled or disabled. Use `j` / `k` to move, `Enter` to toggle the selected manual server, `e` to enable, `d` to disable, and `Esc` to close.

The selector can be opened while the agent is running so you can inspect MCP state without waiting for the current turn to finish. While running, it is read-only: enable/disable actions are disabled until the agent is idle. Auto-start MCP servers are always read-only in this selector; only servers configured with `manual: true` can be changed at runtime.

### `/export` — export the current session

Export the current session as Markdown (default) or JSON.

```text
/export                  # default: export as Markdown into the session artifacts directory
/export ~/out.md         # specify an output path
/export --json           # export as JSON
/export ~/out.json       # a .json path auto-selects JSON
```

The export includes every conversation message plus the current session usage statistics. On success, the TUI displays the saved path.

### `/stats` — usage statistics overlay

Opens an overlay to browse usage data along two axes:

- **Scope**: `Session` (current session) or `Project` (aggregate project stats). Press `s` to toggle.
- **View**: `Overview`, `Models` (per-model breakdown), `Agents` (per-agent breakdown). Project scope additionally supports `Dates` (per-day breakdown). Press `Tab` / `Shift+Tab` to switch views.

Session Overview shows: LLM calls, input/output tokens, cache read/write tokens, reasoning tokens, and estimated cost. Models and Agents views display detailed per-dimension tables.

Project statistics are auto-aggregated from local sessions directories, with five time ranges: `today`, `7d`, `30d`, `90d`, `all`. Switching to Project may briefly show "loading" before data appears.

Any active search is automatically cleared when the overlay opens. Press `Esc` to close. You can also open it directly via the `$` key in Normal mode.

### `/rules` — session rule manager

Opens an overlay listing rules added during the current session via the confirmation popup's "allow and remember" path.

- `↑` / `↓` or `j` / `k`: move cursor
- `d`: delete the current rule
- `o`: open the rule's backing config file in the OS editor
- `Esc` / `q`: close

Each rule shows its scope (`session` / `project` / `global`) and on-disk file path. These are **dynamically added temporary rules** that complement the pre-written permission rules in `config.yaml` — they never modify the original config files.

### `/loop` — continuous execution mode

Continuous execution mode keeps the agent working after each round without you having to nudge it. Suitable for one-shot instructions like "implement feature X" — you send one message and the agent iterates, verifies, and pushes through until the work is done, genuinely blocked, or you explicitly confirm exit.

`/loop` is available only when the current MainAgent role can use the `Done` tool. If `Done` is denied or hidden for that role, `/loop` is unavailable.

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
4. **continue or request exit**: if more work remains, the agent keeps going; if it believes the loop can stop, it must request exit through the `Done` tool

When `Done` is requested before the loop exit conditions are satisfied, Chord rejects that request and automatically makes the agent continue. When the exit conditions are satisfied, Chord shows a local confirmation dialog instead of stopping immediately. The `Done` tool must include a non-empty `report` argument containing the final completion report, and that report is what the confirmation dialog shows. If you confirm exit, loop mode stops and the agent becomes idle; otherwise the loop keeps running.

Runtime-injected user continuation messages are used only after a terminal assistant turn that ended with an `end_turn` / `stop` / `done`-style stop reason and no tool calls. If the model already returned tool calls in that turn, loop continuation stays inside the tool-call flow: Chord records tool results, updates loop state, and may show loop guidance in the TUI, but it does not append a synthetic user message unless you manually send one.

**Using `/loop` + `Done` to get more value from Codex quota:** loop mode does not create extra quota, but it helps spend existing quota on end-to-end progress instead of repeated human re-prompting. In normal mode, the model often stops after one local milestone and waits for your next message; that burns another turn later just to say "continue", rerun checks, or pick up unfinished cleanup. With `/loop`, the agent keeps iterating inside the same task until it either reaches a real stop condition or asks to exit through `Done`. The `Done` gate matters here: it prevents premature stopping, so the agent is pushed to finish the whole chain — implement → test → fix failures → verify again → summarize — before giving control back.

A good pattern for Codex-heavy work is:

1. turn on loop with a concrete target (`/loop on implement feature X with tests`)
2. give one complete instruction with success criteria
3. let the agent continue through edits, test failures, and follow-up fixes
4. only confirm the final `Done` request when the work is actually complete

This usually improves quota efficiency for multi-step coding tasks because fewer turns are wasted on manual nudges like "continue", "run the tests too", or "fix the failing case and try again". Do **not** use `/loop` just to keep the model running aimlessly; if the task is exploratory, ambiguous, or likely to need frequent product decisions, normal mode is often cheaper and easier to control.

If the task is genuinely blocked, the agent can still report `<blocked>category: reason</blocked>`. You can always press `Esc` to cancel the current iteration.

**Status bar:** when enabled, the TUI status bar shows a `[↻]` marker.

**When to use:** multi-step tasks (generate code → write tests → debug → refine), iterative development. Not suitable for: one-shot queries or pure Q&A.

You can also define **custom** slash commands (per project or globally). See [Customization — Custom slash commands](./customization.md#custom-slash-commands).

## Multi-agent focus switching

Chord supports cooperation between MainAgent and SubAgents.

- `Tab`: cycle the main agent mode (role) shown in the status bar (main view only)
- `Shift+Tab`: cycle the focused agent view between the main agent and subagents

In a SubAgent view, you can inspect that agent's context and output. Finished SubAgent views are read-only.

## Images

Currently supported:

- Paste images from the clipboard
- Attach image files to the currently focused agent's message
- View images directly in supported terminals
- Edit/fork historical user messages that contain images; path-restored images are reloaded when the edited message is sent again

Common actions:

- `Ctrl+V` / `Cmd+V` in the main composer: prefer clipboard image input. If an image is found, Chord adds it as an attachment and inserts a placeholder like `[image1]` at the cursor; any pasted text provided by the terminal is inserted after the placeholder. If no image is found, Chord pastes text. After paste completes, the cursor is kept immediately after the inserted placeholder/text.
- `Ctrl+V` / `Cmd+V` in confirmation-dialog text fields: always paste text, never attach images
- Inline image attachments are capped at 5 per composer message
- Typing literal placeholder text such as `[image1]` does not attach an image by itself; only Chord-inserted inline image placeholders are attachment-backed
- To attach an image by path, enter the path in the composer and configure a custom `insert_attach_file` key binding
- `Enter` / `o` / `Space`: open the image in the current message in Normal mode

## Copying text

- Drag in the transcript to select text inside the TUI
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
