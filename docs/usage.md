# Usage

<!-- description: Everyday usage: input modes, key bindings, sessions, slash commands, worktrees, images, and headless mode. -->

Keep daily TUI work moving: send messages, read tool cards, recover sessions, and steer long tasks. Chord runs either as the local TUI or as the `chord headless` control plane; most of this page is about the TUI.

For image generation and explicit reference editing, configure a separate image target; see [Image generation and editing](image-generation.md).

## File delivery and preview

The model uses `present` to declare final reports, images, web pages, and other files. A successful tool card lists the files. Select the card and press Enter, or click it, to choose a file. Declaring files does not launch applications or establish that their contents or tests have been verified.

Use the arrow keys to select a file, Tab to select Preview / Open / Open with / Copy path, and Enter to act. `o` opens with the system default association, `a` selects another associated application, and `p` copies the path. HTML offers source preview; PDF and other binary formats use external applications. Static HTML opens at its original location so relative resources remain reachable. Chord does not build a page or start a development server automatically.

The text viewer supports `j/k` or arrows, PgUp/PgDn, Home/End, horizontal arrows, `w` for wrapping, `m` for Markdown/source, `/` for literal search, `n/N` for the next/previous match, `g` for a source line, `r` to reload, `l` to select local links or images in the document, and `y` to copy loaded source. Esc leaves search or line entry first, then returns to the file list and closes the viewer. Preview does not send model requests or change the input draft. Source references recognize explicit Markdown local links and line fragments such as `#L12`. Relative references in older messages without a recorded working directory are unavailable.

“Current file” stays bound to the original target and may show subsequent edits. Preview, reload, and external opening report changes detected against the delivery. Files larger than 8 MiB are checked by size and modification time only and are marked as having unverified contents. “Declared version” refers to a session artifact whose full digest and size must match before opening. Missing or overwritten versions remain unavailable; Chord does not substitute a same-named file. An HTML entry digest does not cover CSS, scripts, images, or other resources. Delivery metadata survives session restore, compaction, and forks. JSON export carries metadata only; importing it does not restore missing artifact bytes.

Text preview loads at most 1 MiB and 20,000 source lines; rendered output retains at most 10,000 lines. Markdown rendering accepts at most 128 KiB. Limits produce a truncation or source-mode notice. `y` copies only loaded source; use an external application to inspect the complete file.

macOS lists system associations; Windows uses the native Open with chooser; Linux prefers GIO and supports default opening only when just `xdg-open` is available. Selecting an application does not change default associations. SSH, headless desktops, and remote sessions cannot open server files on the local desktop; the original path can still be copied. Programs, scripts, and system shortcuts cannot be launched through these actions.


## How to use this page

You do not need to read this page from top to bottom:

- **First task:** [TUI basics](#tui-basics) covers sending, tool cards, and approvals; set rules in [Permissions & Safety](./permissions-and-safety.md) before the first edit.
- **Long tasks:** [`/loop`](#loop-continuous-execution-mode) keeps implement, test, and fix moving without nudging.
- **Parallel work:** [Worktrees](#worktrees) puts each task in its own checkout while sessions stay shared per repository; resume, fork, and import live under [Sessions](#sessions).

## Modes

Chord has two main usage paths:

- **Local terminal interface**: the default mode for sending tasks, inspecting results, and handling approvals
- **Headless mode**: use `chord headless` to control Chord from a script, gateway, or chat bot

Most personal development workflows should start with the local TUI.

## TUI basics

After startup, the input box is focused by default. Type a message and press `Enter` to send.

Tool cards show terminal-safe previews. File paths inside the session working directory are displayed as relative paths; external paths remain absolute. Tool path arguments accept `~/...` prefixes, which expand against the home directory. Collapsible cards start compact, keeping only key facts such as ranges, counts, command intent, and terminal status; full output, diagnostics, truncation details, and artifact references appear when expanded. A `▸` / `▾` beside the terminal-state marker means the card folds: focus it and press `Space`, `Enter`, or `o`.

`write`, `edit`, `apply_patch`, `todo_write`, `delete` and `handoff` cards are always expanded, so their body (file content, diff, todo list, error detail or plan path) is what you see, and they carry no disclosure marker. `read`, `grep`, `glob`, successful `shell` output and generic tool calls fold. `yy` copies the complete card content either way.

When Chord is running in the background, the terminal title shows a one-shot `✅` completion marker when the focused agent transitions from busy to idle. Focusing the terminal clears the marker; ordinary tab/window focus changes do not re-add it unless new background work later completes.

The status bar uses compact activity icons instead of repeating phase labels: `✶` means preparing, `⠋` frames mean connecting, `◷` waiting for response headers, `◌` waiting for the first token, `↓` receiving, `↻` retrying, `⇄` switching API keys, `⏸` cooling down, `⚙` executing tools, and `■` / `▪` compacting. Connecting frames change every 500ms and compaction icons every second; other activity icons remain static, with fixed theme colors. Elapsed time, received bytes, event counts (`events`), and retry/cooldown countdowns update once per second. When space is tight, event counts are omitted first, followed by elapsed time. State transitions, completion, and user actions appear immediately. Detailed fallback and provider errors remain in the error panel. Question dialogs keep custom drafts when you return to the options; `PgUp` / `PgDown` and the wheel read long questions. The model-pool selector supports `/` filtering. See the [key bindings reference](keybindings.md).

The sidebar MODEL section and status bar show the current request target, including fallback attempts and the target of a retry wait. After a request ends, they show the next model, including a model-pool change waiting for the next request. The variant, key count, rate limits, and effective service tier follow that target. Context usage and its warning colors follow the model whose context budget is already in effect; while a switch is pending, that budget can still belong to the previous model.

Common keys:

- `Esc`: switch to Normal mode; pressing `Esc` again in the running main view cancels the current turn
- `Ctrl+X`: stop the current operation, keeping the unsent draft and session; also leave loop mode
- `i`: return to insert/input mode
- `j` / `k`: move between message cards
- `gg` / `G`: jump to top / bottom
- `/`: search messages
- `Ctrl+T`: open the message directory
- `Ctrl+P`: switch the main role model pool
- `Ctrl+O`: open the MCP server selector
- `Ctrl+E`: open the error panel (view the errors recorded so far)
- `Ctrl+G`: export a diagnostics bundle
- `q`: press twice to quit
- `Ctrl+C`: press twice to quit

### Answering questions

Questions open automatically. If another dialog is active, they wait in a shared queue with permission confirmations, completion approvals, and Handoff decisions. Blocking decisions take priority, followed by completion approvals and nonblocking questions; requests at the same priority keep their arrival order. The current dialog stays open while you read or edit it. Nonblocking questions let the agent continue independent work while the answer dialog is open. The dialog keeps your input while it stays open, including when a submission fails.

Asynchronous answers, answer revisions, and system decisions appear as decision cards in the conversation. These cards stay fully visible without a collapse control, with the question, selected answers, and option descriptions on separate lines. Multiple selections are shown individually, custom answers retain line breaks, and defaults adopted by the system are clearly labeled. Question tool cards show readable receipts such as “Questions sent; awaiting answers.”; this receipt confirms that the questions were sent, not that you answered or authorized an action.

Arrow keys or `j` / `k` move between options. Numbers submit a single choice; for multiple choices, toggle with numbers or Space and submit with `Enter`. `Tab` switches between custom text and options; `Shift+Enter` or `Ctrl+J` inserts a newline. Use `PgUp` / `PgDn` or the wheel to read long questions; once the question cannot scroll further they move the conversation behind it, and the wheel follows the pointer. A question stays on screen until you submit it, decline it with `Esc` (you do not want to answer), or withdraw the requirement with `Ctrl+W`. Permission confirmations retain their own controls: `Esc` denies execution, and in completion approvals it opens the rejection-reason editor.

At most 32 questions may remain unanswered, with a combined limit of 1 MiB for JSON-encoded question items. Resolve existing questions before adding more when either limit is reached. Closed decisions and historical revisions remain available.

Required decisions never time out. A default can be adopted only when the model supplies an explicit valid option and `question_auto_select_timeout` is positive. The setting defaults to `0` (disabled); `60` means one minute. Timing starts when the answer dialog and its default are visible. Automatic display does not cancel the timer. Starting to edit or navigate a question requests permanent cancellation; after acknowledgment you can type at your own pace. Automatic adoption is labeled as a system decision, and never means you answered, consented, or authorized an operation.

`question_timeout` applies only to optional questions. It does not limit required or default-choice questions. While Core continues running, client disconnects do not pause an armed timer. Explicit task pauses and Core shutdown suspend timing; resumption requires a new answer entry. IM channels that cannot detect text entry disable the question timer before offering an answer interface.

Declining an answer does not withdraw the requirement. `Ctrl+W` explicitly withdraws it, as does `/question withdraw <id>`. Use `/question replace <old-id> <new-id>` to associate a replacement decision. `/question cancel-task` cancels the current task and its questions; `/new-task <text>` starts an independent task without inheriting old completion gates. An answer such as “do not execute” is a valid decision and does not authorize execution.

### Error panel

Press `Ctrl+E` in normal mode to open the error panel, which lists the errors encountered so far. This includes:

- **Intermediate retry errors**: API errors that triggered a key rotation, model fallback, or stream retry (e.g., 429 rate limits, 503 service unavailable, context length exceeded, timeouts). These are recorded silently and only appear in the error panel, keeping the conversation flow clean. Switching to a different fallback model is the exception: the notification appears as soon as the fallback starts, naming the reason and the target model. While the next attempt is pending, the status bar stays compact and shows only the retry icon with its elapsed time or countdown.
- **Final errors**: errors that exhausted all retries and appear as red error blocks in the conversation.

Each error record shows:

- Timestamp (HH:MM:SS)
- Provider and model (e.g., `Anthropic/claude-opus-5`)
- Masked API key label (e.g., `key=sk-a...xyz9`, showing a short prefix and suffix for safe identification)
- HTTP status code (when available)
- Error code and type (when provided by the API)
- Error message (wrapped to panel width)

Example error entry:

```text
14:25:38  Anthropic/claude-opus-5  key=sk-a...xyz9  HTTP 503  code=model_not_found
  No available channel for model sample/model under group default
```

Navigation:

- `j` / `k`: scroll one line
- `Ctrl+F` / `Ctrl+B`: page down / up
- `g` / `G`: jump to top / bottom
- `y`: copy every recorded error to the clipboard, with full dates and unwrapped messages (`Cmd+C` / `Super+C` does the same)
- `Esc`: close the panel

The error panel keeps the most recent 80 errors in a ring buffer (newest first), and the buffer lives in process memory: it starts empty when Chord launches and clears when you start or resume a session; a forked session keeps the records it inherits. Use it to diagnose why a model fallback occurred or which keys are hitting rate limits.

## Info panel

The right panel keeps a stable order: agents, background jobs, and todos appear and disappear during a session, so they render after the model, usage, integration, and repository sections and never shift the information above them. A focused agent has a `▸` marker alongside its status icon, so its running, waiting, or failed state remains visible.

Enter `/status` to open the same live information across the full conversation area, including in narrow terminals. Use `j` / `k` or the arrow keys to scroll, `PgUp` / `PgDn` to page, and `g` / `G` to jump to the top or bottom. `Tab` / `Shift+Tab` selects a section; `Enter` or `Space` folds or expands it. Mouse scrolling and clicks work as in the right panel. `Esc` or `q` returns to the conversation.

Enter `/context` to inspect the focused task’s latest request: input composition, request-level reduction, and local cache-prefix changes with service-reported cache usage. See [Explain the latest request](context-management.md#explain-the-latest-request).

### `AGENTS` block

Each child task shows its current state: `running`, `awaiting owner` (waiting for its coordinating agent), `awaiting child`, `idle`, `delivered`, `failed`, `cancelled`, or `unknown`. The header counts each category separately and excludes the main agent. `delivered` means that attempt has handed back its result; it does not mean the overall task has passed review.

A task awaiting its owner can show one line from its current coordination request. Click the task or its reason to open that agent's conversation and read the full request. The reason disappears when the request is resolved or the task resumes. Narrow panels omit the row's state label when it cannot fit; the status icon remains visible.

### `USAGE` block

- `Context` shows the input-side token burden of the most recent model request, as reported by the provider. A `≈` prefix marks the single estimate used when that response omitted usage; a session that has not had a measured response yet shows `0`.
- `Bytes` and `Messages` describe the conversation context that will be sent to the model. After request-level context reduction runs, `Bytes` shows the current request's post-reduction context byte count followed by `↓` and the percentage saved relative to that request's unreduced context: `(bytes before reduction - bytes after reduction) / bytes before reduction`. This is not a cumulative value across requests; savings from frozen reduced summaries still count whenever those summaries are used in the current request. When a session is restored, Chord precomputes the same reduction for display, so `Bytes` starts at the post-reduction estimate instead of dropping after the next request; before any request surface can be prepared, it falls back to the current durable context estimate.
- `Bytes` counts the installed system prompt, message content, image payloads, and tool names/descriptions. It excludes JSON escaping overhead, tool-call argument JSON, thinking metadata, and request parameters such as stream settings or thinking budgets.
- These reductions are not persistent compaction: older tool results are usually replaced with shorter placeholder summaries for the request, while durable session history remains intact. `/compact`, automatic compaction, tool-output growth, and system prompt or tool-definition changes update the fallback durable estimate; new request preparation refreshes the actual sent request size, including while loop mode is active.
- When `Cache R` shows a percentage, it is cache-read tokens divided by input-side prompt tokens plus separately reported cache-write tokens. Output tokens are excluded because prompt caching applies only to the input side.
- `Think` appears only when the provider reports reasoning/thinking tokens. These tokens are already included in output-token billing; the line is a visibility breakdown, not an additional token bucket.
- `Calls` counts the real LLM requests issued by the focused agent (main agent, running SubAgent, or parked task). It comes from the persisted usage ledger, so it survives session restore and is not reset by context compaction.

### `TIME` block

`TIME` reports cumulative wall-clock durations for the focused agent: `Model` (LLM streaming), `Tools` (tool execution), `Cooldown` (key/model cooldown waits), and `User wait` (waits for your confirmations or answers). These are sums of operation intervals, not exclusive slices of elapsed session time; parallel operations can therefore contribute to more than one bucket at once. Each bucket's percentage uses the sum of the displayed buckets as its denominator.

- Buckets under one second are hidden, including from the percentage split; if every bucket is sub-second the whole section is hidden.
- `Model` includes time spent streaming compaction drafts. When a confirmation dialog, Question prompt, or Handoff selector is pending, tool cards show execution time only: confirmation, answer, and handoff-decision waits are recorded under `User wait`, never under `Tools`.
- The section follows the focused agent (main agent, running SubAgent, or parked task) and is rebuilt from the session's usage ledger after restore or resume.

## Background jobs

Some work outlives the turn that started it: long shell commands, including the ones SubAgents launch. While such a command is running or stopping, Chord tracks it as a job, and the turn can finish without stopping it.

### JOBS block

Whenever at least one job is running or stopping, the info panel gains a `JOBS` block. Each job takes one line, indented two columns, with a label (the job description, or the command when there is no description), the elapsed time, and a trailing `x`. Jobs started by SubAgents are listed right alongside your own.

With nothing running or stopping, the block is omitted entirely.

### Narrow terminals

When the terminal is too narrow for the info panel, the status bar carries a plain-text counter pill instead, such as `2 agents · 1 job`. It shows both counts while there is room; when space runs short, Chord drops the agents half first and then hides the pill. Click the pill to open the JOBS list overlay; with the info panel visible, the same list is already on screen. With no jobs to show, Chord tells you so instead of opening an empty overlay.

The pill and the `x` are click targets, so a terminal without mouse reporting cannot reach them. `ctrl+j` opens the same list from Normal mode at any width; inside it, `j` / `k` (or the mouse wheel) move the selection and `Enter` opens the confirmation for the selected row.

### Stopping a job

Click a job's trailing `x` to open a confirmation dialog. It lists the job id, label, command, owner, status, elapsed time, the time of the last output, and the most recent output lines; when output was discarded, it also reports how many bytes were dropped and the path to the full log.

Only `y` confirms. `n` and `esc` cancel, and `Enter` is not bound. After you confirm, that line switches to stopping and its `x` disappears.

Stopping a job yourself does not raise a toast. When the job ends, its owner still receives the result, stating plainly that you stopped it; a job that had outlived the turn that started it arrives as a JOB RESULT card.

`Esc`, `Ctrl+X`, and `Ctrl+C` do not stop a background job, including one still running inside the current turn: a long command that outran the foreground budget has already become a job. Confirming the stop dialog (reached by clicking `x`, or with `ctrl+j` then `j` / `k` and `Enter`) is the only way to stop one. Jobs do not outlive Chord itself: quitting or switching sessions terminates all of them.

Cancelling a turn does not mute the jobs it left behind either. When one finishes later, its result still arrives and opens a new turn — Chord wakes the main agent for every delivery rather than dropping a result nobody asked for — so a cancelled turn can be followed by a short result turn per job that completes. Stop those jobs, or quit Chord, if you want the session to stay quiet.

### Terminal title

The terminal title spinner keeps turning while only background jobs are running. With the window unfocused, that spinner is the only sign of life.

## Editing a composer selection

Drag to select text in the main composer. `Backspace` or `Delete` removes the entire selection; typing, inserting a newline, or pasting text replaces it. `Enter` still sends the message; use `Shift+Enter` or `Ctrl+J` for a newline. Left and Right collapse the selection to its start and end, respectively. `Esc` clears it and leaves Insert mode.

- `Shift+Left` / `Shift+Right` extend the selection; add `Alt` or `Ctrl` to extend by word. `Shift+Up` / `Shift+Down` extend by display row. Ordinary Up and Down still navigate input history.
- Images and large-paste placeholders are selected as complete objects. Touching any part selects and edits the whole object; deleting an image placeholder also removes its attachment.
- `Ctrl+Z` undoes the entire deletion or replacement, restoring text, the cursor, placeholders, and attachments.
- If text, the cursor, or the selection changes while `Cmd+V` reads the clipboard, Chord preserves the current draft and asks you to paste again. Empty clipboard contents or a failed read leave the draft intact.

Resizing and scrolling keep the same source text selected. Switching agents, recalling history, and sending clear the selection. These operations require the terminal to forward mouse events to Chord; native terminal selections support copying only.

## File mentions (`@path`)

Type `@` in the composer at the start of a line or after a space to open file completion.

- Bare `@` uses a cached workspace index of text files. That index includes tracked files and untracked non-ignored files, but skips Git-ignored paths, hidden directories, binary extensions, and common noise directories.
- Once you start typing a root-level filename prefix such as `@A`, Chord also checks the session working directory directly. This allows root files such as `AGENTS.md` to complete even when they were excluded from the cached index by `.gitignore` or local Git excludes.
- If the query already looks like a path, such as `@docs/`, `@./`, `@~/`, or `@.config/`, Chord switches to direct filesystem completion for that directory instead of staying on the cached index. This path-mode completion can surface ignored paths when you explicitly type toward them.
- Hidden entries stay hidden by default. To see them, make the query itself explicit, for example `@.`, `@.env`, `@./.`, or `@.config/`.
- Add a 1-based line suffix to include only part of a text file: `@path:42` injects line 42, and `@path:10-20` injects lines 10 through 20. Completion replaces only the path segment, so a suffix you typed is preserved when accepting a file match. If a real filename contains the numeric colon suffix, such as `note:12`, the filename takes precedence over line-range parsing.
- Completion is only input assistance. When you send the message, Chord reparses the final `@path` text; if you removed the mention before sending, that file is not attached.

## Sessions

In the session picker, press `d` to open deletion confirmation. Press `y` to delete the selected session permanently, or `n` / `Esc` to cancel. `Enter` does not confirm deletion. Long confirmation content can be read with `PgUp` / `PgDown` or the mouse wheel; the decision actions remain visible.

Chord keeps persistent sessions for the current project.

Common workflows:

- `chord`: create a new session
- `chord --continue`: resume the most recent non-empty session for this project
- `chord --resume <session-id>`: resume a specific session of the current project
- `chord resume <session-id>`: resume a session by ID from any directory. It auto-locates the chord-managed worktree the session belongs to and switches into it; add `--fork-history[=N]` to resume a fork at a compaction boundary instead (default: latest applied boundary; the fork reproduces that generation with its compaction archives and starts usage and runtime state fresh)
- `chord import <source> [file]`: import an external session into Chord's session store
- `/new`: create a new session in the TUI
- `/resume`: pick a historical session in the TUI
- `/rename <title>`: set the current session's display title; bare `/rename` clears it

When exiting, if the current session can be resumed, Chord prints the corresponding resume command.

Usage statistics use the current `usage.jsonl` ledger format. Session restore and summaries do not migrate older usage schemas; an invalid or stale `usage-summary.json` is rebuilt from the ledger.

`/new` resets session state such as conversation history, todos, and usage. Runtime preferences such as the current model pool, service tier, and MCP state stay active until the process exits.

Custom titles are shown in the session picker and terminal title. They are metadata only: `/rename` does not change the session ID, directory, transcript, or resume command.

### Taking over a live session

On macOS and Linux, a TUI can take over a session owned by a local gateway, and a gateway can take over a TUI session. Both processes must run as the same OS user in the same project workspace. Plain headless sessions, ACP sessions, and transfers between two TUIs or two gateways are not supported.

Start a TUI with the session to take over:

```bash
chord --takeover <session-id>
```

In an idle TUI, use `/takeover <session-id>`. The target TUI must have no running work, queued inputs, or unsent drafts. Chord waits for model requests, tools, and background jobs that have already started, saves their results and queued inputs, then transfers the session. It does not cancel that work. The restored session stays paused until you explicitly continue or send a new request; pending inputs keep their order and belong to their original conversation.

The TUI shows the paused queue hint when there are pending inputs. With no pending inputs, it shows the normal idle status; restoration never starts model work automatically. During a transfer, you can still answer an existing question or explicitly stop active work, including background jobs through `Ctrl+J`.

Add `--takeover-timeout 30s` to the startup command, or `--timeout 30s` to `/takeover`, to limit how long you wait. A timeout leaves the transfer running at the source. A timeout of `0` returns immediately after acceptance. Use the returned transfer ID to check progress or retry a failed save:

```bash
chord takeover status --session <session-id> --transfer-id <transfer-id> --json
chord takeover retry-save --session <session-id> --transfer-id <transfer-id> --json
```

An accepted transfer cannot be cancelled or redirected. If the waiting target exits, check the status and use `chord --resume <session-id>` after the source releases the session. This also restores the session paused. For a session that is already offline, use ordinary resume directly.

### Editing after resuming a session

When you resume a session, Chord rebuilds only the safety state needed to allow later edits. It does not re-read files or reload their contents into the conversation. If you edit a file that was changed on disk since the session ended, or whose read history was not durable, the next `edit` / `apply_patch` on that file may be blocked and the agent will re-`read` it first. This is intentional: it keeps edits from being applied against stale file contents.

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
- `--id <session-id>`: import by the source tool's own session id instead of file path; for Codex this is the id `codex resume` prints (supported for `codex` and `claude`)
- `--root <path>`: root directory for `--id` lookup
- `--reasoning off|visible|strict`: reasoning import policy (default: `strict`)
- `--dry-run`: parse and report only, no writes
- `--json`: machine-readable output
- `--force`: overwrite an existing `--sid`

## Worktrees

For working on multiple tasks in parallel without crosstalk, Chord can create and run inside dedicated git worktrees:

- `chord --worktree`: create or enter a chord-managed worktree (auto-named when no name is given)
- `chord --worktree feat-auth` / `chord worktree feat-auth`: create or enter the worktree named `feat-auth` (branch `chord/feat-auth`); combine with `--continue` or `--resume` to keep working on the repository's latest session with the worktree as the working directory
- `chord headless -d <repo> --worktree feat-auth`: same in headless mode; the `ready` event payload includes the worktree's `name`, `branch`, `path`, and `repo_root`
- `chord worktree list`: list chord-managed worktrees of the current repository
- `chord worktree remove <name>`: delete the worktree and its runtime cache; the branch and the repository's session history are preserved. Pass `--delete-branch` to delete the branch only-if-merged or `--force` to force-remove a dirty worktree and its branch.
- `chord worktree finish <name>`: update the worktree from the target branch, squash its result back as one commit, then remove the worktree and branch. Use `--onto <branch>` to choose the target or `--check` for a non-mutating conflict check. On conflict, the target branch stays unchanged and the worktree is left ready for you to resolve the merge and rerun `finish`.

Creating or entering a worktree changes the directory Chord runs in. You can do that either with `chord --worktree <name>` or with `chord worktree <name>`. The `worktree` subcommand also owns management operations such as `list`, `remove`, and `finish`. These commands are the normal way to start a worktree session.

Worktree commands need `git` on `PATH`. When Chord cannot find it, creating or entering a worktree, `list`, `remove`, and `finish` refuse with a message that names the missing git binary instead of reporting a repository error. A session that recorded a checkout can still be resumed: Chord reports that it cannot verify the checkout, resumes in the repository checkout it resolved, and keeps the record, so a later resume switches back once git is available.

**Where they live.** By default, worktrees land under `<state-dir>/worktrees/<repo-id>/<slug>`, outside the repository. `worktree.root` moves them: a relative value resolves against the main repository root, so `root: .chord/worktrees` puts checkouts at `<repo>/.chord/worktrees/<slug>`. When the directory is inside the repository, Chord keeps a `.gitignore` containing `*` there so the checkouts never show up as untracked files; that file only keeps `git status` clean, and chord's own `grep` / `glob` skip that root. Other tools are not aware of it: an in-repo checkout is a second copy of the tree on disk, so index- or scan-based tools (LSP indexing, a `docker` build context, test runners that sweep the repository) may pick it up too. Leaving the default location avoids that.

**How the status bar shows it.** A session working in a managed worktree shows the checkout as `<repo> · <worktree>` in the status bar instead of a path: the checkout lives outside the repository, so a shortened path would hide the repository name or imply a location that does not exist. The full path is in the info panel's GIT section as `Worktree Path`, and double-clicking the status bar region copies the checkout path.

**What a worktree contains.** Only tracked files. Uncommitted changes in the main checkout are not copied, and neither is gitignored content: a local `AGENTS.md`, `.chord/config.yaml`, agents, skills, plans, and memory stay in the main checkout, and a session in a worktree reads them from there. The exceptions are `AGENTS.md` and project skills: when the checkout carries its own copies, those win, so a branch can ship its own instructions and skills. Everything else behaves as it does in the main checkout — same sub-agents, same memory. To copy specific gitignored files, such as local env files or machine-specific config, list their patterns in a repository-root `.worktreeinclude` (gitignore syntax): Chord copies the matching gitignored files on creation and never overwrites a tracked file. Without that file, `.env*` is copied. Copying happens once, at creation: later edits are not synced in either direction, and since `.env*` is copied by default a local credential can end up in every checkout, so keep the patterns to what a worktree actually needs.

**Sessions are shared per repository.** All checkouts of a repository share one session store, so a session you started in a worktree shows up and can continue from the main checkout, and the other way around. Runtime cache stays per checkout, and exports follow their sessions. A session records the checkout it was working in, and `/resume` marks it on that session's row, so the list stays searchable by checkout name: `chord resume <id>`, `chord --resume <id>`, and `chord --continue` switch back to it, and a session that continues in a worktree records it. When the recorded worktree is gone they warn: `chord resume` continues in the main checkout, while `--resume` and `--continue` continue in the checkout Chord was started from. `chord worktree remove` and `chord worktree finish` never delete the repository's session history.

**A checkout is not exclusive.** Creating or entering a worktree reuses the same directory whenever it already exists, and nothing prevents two sessions from working in one checkout: both see the same uncommitted changes, and either can overwrite the other's files. Run one worktree per task that proceeds in parallel, and treat a checkout as a single writer's directory while it has uncommitted work.

**Permissions follow the session, not the checkout.** Permission rules apply to every checkout of a repository: a rule such as `write src/**: allow` in the main checkout also allows writes to `<worktree>/src/`, and you cannot write a rule that allows a single checkout only. Chord evaluates repository paths through their repository-relative spelling, so an absolute path rule never matches them; worktrees are for parallel work, not for narrowing permissions. Hooks, agent configuration, permission rules, and worktree creation settings are read when a session starts, from the main checkout; resuming a session recorded in another checkout does not reload them — to pick up config edited on a worktree branch, start a new session in that checkout.

## Local slash commands

These commands are handled by the local runtime and are not sent to the model as-is. In the TUI, type `/` to open completion. `Tab` completes the highlighted command without running it. `Enter` completes it when the input is not already that command, then runs or sends it in the same keypress. Narrowing the list by typing keeps the highlighted row: `Enter` runs that command, not the first match:

- `/new`: create a new session
- `/resume`: resume a session
- `/takeover <session-id> [--timeout <duration>]`: take over a live gateway session
- `/rename <title>`: set the current session's display title; bare `/rename` clears it without changing the session ID
- `/models`: view pool status or switch the current view's model pool (`main` view = current main role; `SubAgent` view = that agent)
- `/models --agent <name> <pool>`: directly set a named agent's pool
- `/role`: open a role-picker dialog and switch the active main agent (builder, planner, and custom main-mode roles), the dialog form of `Alt+R`; `/role <name>` switches directly without the dialog, and `/role status` prints the current role and the available roles
- `/mcp`: open the MCP server selector; `/mcp status` prints status; `/mcp enable|disable <server>` toggles manual servers. Runtime changes take effect for the next LLM request, not the currently in-flight request.
- `/skill <name> [args]`: load a skill explicitly, including one kept out of the model's catalog by `disable-model-invocation`; bare `/skill` opens the skill selector. See [Skills: Explicit loads](./customization.md#explicit-loads).
- `/compact`: manually trigger context compaction to summarize the current conversation as a structured archive; while the agent is busy with `context.compaction.model_driven` enabled, the model is also asked to checkpoint immediately and its checkpoint can replace the runtime result; see [Context management: Compaction](./context-management.md#context-compaction)
- `/tier standard|fast|slow`: set the service tier for subsequent model requests (including later retry rounds that have not started yet). Bare `/tier` is not a status command; use the sidebar/status display for the current effective tier. If you enter a tier that the current provider/model does not support, Chord leaves the current tier unchanged and shows an error.
- `/yolo on|off`: temporarily bypass the main agent's permission checks for ordinary tools. While YOLO is on, file edits, shell commands, and similar calls run directly: `ask` rules do not raise a confirmation and `deny` rules do not block. The bypass is one-way: it only widens permissions, so a tool that works with YOLO off keeps working with YOLO on, and switching YOLO off restores the original permissions. It also lifts the active role's own permission config: on the main agent a role is a permission preset rather than a capability boundary, so a read-only role such as `planner` becomes writable while YOLO is on. The full scope, including why SubAgents are the deliberate exception, is in [Permissions & Safety: Special permission semantics](./permissions-and-safety.md#special-permission-semantics). `handoff`, `delegate`, and `cancel` keep following their configured rules: `allow` stays allowed, `deny` keeps rejecting, an `ask` rule passes without raising the confirmation dialog, and wildcard defaults behave exactly as they do with YOLO off. `done` and `compact_context` keep their dedicated semantics. YOLO can be toggled while the agent is running; the execution-time change applies immediately to later tool calls, while the LLM-visible tool descriptions and permission prompt are refreshed on the next request. SubAgents inherit the mode while it is on: their `ask` decisions (ordinary tools and mechanism tools alike) stop prompting too, while their `deny` rules keep rejecting. Toggling YOLO applies to a SubAgent's later calls immediately.
- `/help`: toggle the in-app cheatsheet overlay (same as pressing `?` in Normal mode)

When a non-standard tier is active, the sidebar shows it. If a model switch makes the selected tier unavailable, it appears dimmed and struck through. In Normal mode, `Ctrl+R` cycles only through tiers supported by the current provider and model.

The following commands have more interactive detail, expanded below.

### Project Memory

Chord's optional cross-session project memory (stable preferences, project facts, and reusable workflows) has its own page: [Project Memory](./project-memory.md). It covers what gets stored, how the summary loads into a session, how to enable automatic extraction, and how to review or remove entries. Use `/memory` to browse, search, copy or remove records. `/memory organize [request]` generates a preview for all active records; only explicit approval applies it.

### MCP selector

Press `Ctrl+O` to open the MCP server selector. It lists configured MCP servers, their connection state, and whether manual servers are currently enabled or disabled. Use `j` / `k` to move, `Enter` to toggle the selected manual server, `e` to enable, `d` to disable, and `Esc` to close.

The selector can be opened while the agent is running so you can inspect MCP state without waiting for the current turn to finish. Enable/disable actions are also allowed while running, but they are deferred: the current in-flight request keeps the MCP tool surface and prompt it started with, and the changed MCP state is reflected in the next LLM request. Only servers configured with `manual: true` can be enabled or disabled; details provide authentication and reconnect actions for automatic servers too.

When you ask the agent which MCP tools it can use, its answer describes the tools visible to its current role, not every connected server. Use the selector or `/mcp status` for connection state.

External files, web pages, command output, images, and MCP descriptions/results are reference data, not authorization to change the task or execute commands. Agent instructions preserve that distinction, including for delegated workers, but they are not a security sandbox: configure tool permissions conservatively and review sensitive operations.

### MCP sign-in and connection diagnostics

HTTP MCP servers can use managed OAuth with authorization code and PKCE. The default `auth: auto` keeps static `Authorization` headers working; otherwise it uses saved credentials when available and starts anonymously when none are saved. `auth: oauth` requires a saved login. `auth: none` disables managed OAuth while retaining configured headers. stdio servers do not use OAuth.

```yaml
mcp:
  search:
    url: https://mcp.example.com/mcp
    auth: oauth
    oauth:
      issuer: https://auth.example.com
      client_id: sample-client
      token_endpoint_auth_method: none
      scopes: [files:read]
      callback_port: 41937
```

The `oauth` block is optional. A configured `client_id` requires an `issuer` declared by the server. Pre-registered public clients use `none`; confidential clients use `client_secret_basic` or `client_secret_post` and a `client_secret_env` variable name. The secret's value is read only when needed for token exchange or refresh. Do not combine managed OAuth configuration with a static `Authorization` header. Callback ports must be between 1 and 65535; the default is 41937.

```sh
chord mcp list
chord mcp login search
chord mcp login search --reauth
chord mcp logout search
chord doctor mcp search
chord doctor mcp search --connect --json
```

Add `--agent explorer` to select servers visible to that role, including its private servers. `list` reports local credential facts without connecting. The default doctor check is also offline: it checks configuration, header environment references, and saved credentials without starting stdio servers or creating credential storage. `--connect` performs initialize and tool discovery on a temporary connection; it may refresh and durably confirm credentials, so it can update shared credential files. It does not register a client, open a browser, or call a business tool. Exit codes are 0 for success, 1 for an unavailable connection or credential problem, and 2 for invalid arguments or configuration.

Login saves credentials before validating the connection and complete tool catalog. A validation failure leaves the saved credentials available for diagnosis; `credential_saved: true` and the validation result are reported separately. `--reauth` starts a new authorization while preserving the existing account until replacement is committed. `--no-browser` prints the authorization URL and still waits for a callback. Login and logout do not change whether a manual server is enabled, and logout does not revoke the remote account's consent.

In the MCP selector, press `D` for details. Authentication, current operations, connection, tool catalog, and required scopes are separate. Details offer login, reauthorization, logout, reconnect, and cancellation. Closing details does not cancel a login; use its cancel action or switch sessions. `/mcp login|logout|reconnect|status <server> [--agent <definition>]` is also available; `status` may omit the server. Automatic servers support authentication and reconnect actions; only manual servers support enable/disable.

Credentials are stored under `<config-home>/mcp/oauth/` in files restricted to the current user. Worktrees of one repository share a login. Aliases with the same owner, endpoint, and OAuth client configuration also share it; logout affects those aliases. Different repositories, private-role owners, issuers, client IDs, authentication methods, or callback configurations use separate slots. There is one current account per slot. Changing scopes or a server's display name does not erase the login. A missing-scope response can be addressed by signing in again; requested scopes are combined with the additional requirement. OAuth does not change Chord's tool permission rules, and failed business calls are not replayed automatically.

For automatic registration, Chord uses client metadata when the authorization server supports it, otherwise dynamic registration when offered. Client metadata requires callback port 41937. If that port is occupied, stop the conflicting login or use a pre-registered client with a configured callback port. For an SSH host, forward the port before logging in, for example `ssh -L 41937:127.0.0.1:41937 host`, then open the printed URL in your local browser. Headless and gateway clients show status and host-side login instructions; they do not open remote authorization flows.

OAuth discovery and token requests require direct access to public HTTPS authorization servers and do not use environment proxies. Private-network authorization servers are unsupported. Only an explicitly configured loopback MCP resource may use HTTP; this exception does not apply to its authorization server. Custom MCP headers are sent only to the MCP endpoint, never to discovery or token endpoints. See [MCP troubleshooting](./troubleshooting.md#mcp-never-becomes-ready).

### `/export`: export the current session

Export the current session as Markdown (default) or JSON.

```text
/export                  # default: export as Markdown into the session artifacts directory
/export ~/out.md         # specify an output path
/export --json           # export as JSON
/export ~/out.json       # a .json path auto-selects JSON
```

The export includes every conversation message plus the current session usage statistics. On success, the TUI displays the saved path.

### `/stats`: usage statistics overlay

Opens an overlay to browse usage data along two axes:

- **Scope**: `Session` (current session) or `Project` (aggregate project stats). Press `s` to toggle.
- **View**: `Overview`, `Models` (per-model breakdown), `Agents` (per-agent breakdown). Project scope additionally supports `Dates` (per-day breakdown). Press `Tab` / `Shift+Tab` to switch views.

Session Overview shows: LLM calls, input/output tokens, cache read/write tokens, reasoning tokens, estimated cost, and, once any context compaction has run, its lifecycle counts (for example `applied`, `skipped/model_driven`). Models and Agents views display detailed per-dimension tables.

Project statistics are auto-aggregated from local sessions directories, with five time ranges: `today`, `7d`, `30d`, `90d`, `all`. Switching to Project may briefly show "loading" before data appears.

Any active search is automatically cleared when the overlay opens. Press `Esc` to close. You can also open it directly via the `$` key in Normal mode.

### `/rules`: permission rule manager

Opens an overlay for remembered permission rules. It opens even when no rules have been added yet, so you can add one manually.

- `a`: add a rule manually
- `↑` / `↓` or `j` / `k`: move cursor
- `d`: delete the current rule
- `o`: open the rule's backing config file in the OS editor
- `Esc` / `q`: close

When adding a rule manually, enter the tool name and pattern, then use `Ctrl+S` to cycle scope (`session` / `project` / `global`) and `Ctrl+A` to cycle action (`allow` / `ask` / `deny`). Tool and pattern are required. Patterns that do not match future tool calls are accepted but have no effect until they match.

Each rule shows its scope (`session` / `project` / `global`) and on-disk file path. `session` rules apply only to the current session; `project` rules are written to the current project's `.chord/agents/<role>.yaml`; `global` rules are written to the user config directory's `agents/<role>.yaml` (default: `~/.config/chord/agents/<role>.yaml`). These rules directly update the target agent's `permission` config, and deleting a rule removes it from the same agent config file.

The confirmation popup also supports adding a remembered rule with `M`. In the rule picker, press `E` to edit the suggested pattern before saving. Delete confirmations suggest reusable parent-directory rules rather than exact-file rules, ranking directories that cover more paths still needing approval first. A global `*` catch-all is always available. When every requested path lies inside the repository, the picker also keeps a `**` option for follow-up deletes anywhere in the repository (or under the working directory when no repository resolves). The broad `**` and `*` options are never preselected.

### `/loop`: continuous execution mode

Continuous execution mode keeps the agent working after each round without you having to nudge it. Suitable for one-shot instructions like "implement feature X": you send one message and the agent iterates, verifies, and pushes through until the work is done, genuinely blocked, or you explicitly confirm exit.

`/loop` is available only when the current MainAgent role can use the `done` tool: that is, `done` is registered and no rule denies it. A wildcard-only `"*": deny` does not deny it: mounting `done` is what entering loop mode does, so the loop itself is the authorization. Write `done: deny` to keep a role out of loop mode; `/loop on` is then refused with a toast.

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

The `done` tool is mounted only while a loop is running. Outside a loop it is absent from the tool surface entirely, so ordinary sessions do not carry its definition and the model is never asked to choose between answering directly and calling a completion tool; in normal mode the agent simply finishes with a regular assistant response. Enabling `/loop on` mounts it: on models that support Chord's request-only dynamic tool mounts (Responses-family models and Kimi dynamic tools) it is late-mounted for the next request at no prompt-cache cost, and everywhere else the tool surface is rebuilt once, which breaks prompt-cache reuse for that one request. Chord skips the mount when `done` is already present, so it never injects a duplicate. Loop mode then uses the current runtime's tool-call requirements and continuation instructions to make `done` the explicit exit request. Running `/loop off` takes `done` back off the surface, returns subsequent work to normal response behavior, and cancels any loop continuation that had not yet been sent to the model.

Loop mode also detects repeated identical tool calls. It interrupts a stalled sequence and, after repeated interceptions, asks whether to stop or continue.

A good pattern is:

1. turn on loop with a concrete target (`/loop on implement feature X with tests`)
2. give one complete instruction with success criteria
3. let the agent continue through edits, test failures, and follow-up fixes
4. only confirm the final `done` request when the work is actually complete

This reduces manual prompts such as "continue" or "run the tests too." Do **not** use `/loop` just to keep the model running: normal mode is easier to control when the task is exploratory, ambiguous, or likely to need product decisions.

If the task is genuinely blocked, the agent can still report `<blocked>category: reason</blocked>`. You can always press `Esc` to cancel the current iteration.

**Status bar:** when enabled, the TUI status bar shows a `[↻]` marker.

**When to use:** multi-step tasks (generate code → write tests → debug → refine), iterative development. Not suitable for: one-shot queries or pure Q&A.

You can also define **custom** slash commands (per project or globally). See [Customization: Custom slash commands](./customization.md#custom-slash-commands).

## Multi-agent focus switching

Chord supports cooperation between MainAgent and SubAgents.

- `Shift+Tab`: cycle the focused view between the main agent and subagents in both Insert and Normal modes
- `Alt+R`: cycle the main agent role while viewing main; the status bar shows the active role

In a SubAgent view, you can inspect that agent's context and output and submit new input. Completed, failed, and cancelled states describe the previous turn; they do not make the view read-only.

Card numbers are local to the viewed agent: the main transcript and every SubAgent transcript each start at `#1` and advance independently. Switching agent views rebuilds that agent's complete available history, including earlier instances of a rehydrated delegated task, so `Ctrl+B` / `PgUp`, `gg`, search, and the message directory can navigate earlier cards instead of exposing only a live tail.

After resuming a session, every restored agent is idle rather than pretending work from the previous process is still active. Any previous SubAgent state (including waiting, completed, failed, and cancelled) can be continued manually: focus that SubAgent and submit an empty input to continue from its existing context, or submit text to start a follow-up turn. Chord first reacquires a SubAgent concurrency slot and marks it running. An empty input starts a new turn without appending a synthetic user message. Mailboxes restored from the session remain queued while idle and are delivered only as part of this explicit manual continue or input action; mailbox events produced during normal live execution are dispatched immediately to the owning agent.

When `todo_write` is enabled, the todo list may contain multiple `in_progress` items while the agent is genuinely switching between distinct active workstreams. Each active item must use a unique `active_form`; delegated tasks should map to separate live workstreams. Do not mark work as `in_progress` when it is only planned, blocked on prerequisites, or merely waiting to start.

## Images and PDFs

Currently supported:

- Attach images and PDFs from the system clipboard with `Ctrl+V` or `Alt+V`
- Attach image and PDF files to the currently focused agent's message when the active model supports that input type
- View images directly in supported terminals; PDFs are sent to the model and shown as file chips in the transcript, but are not previewed inline
- Edit historical user messages that contain images or PDFs; tail messages reopen in the current session, while earlier messages fork a new session, and path-restored attachments are reloaded when the edited message is sent again
- Let the model use the built-in `view_image` tool to load a local PNG/JPEG/WebP/GIF/BMP/TIFF image into context (normalized to PNG or JPEG, scaled down to 2000px on the longest edge, first frame for animated WebP/GIF/TIFF) when the tool is permitted and the first model in the effective model pool supports image input. The tool uses the same local-path permission handling as `read`.

`view_image` availability follows image input support in the first model of the effective pool. Both Chat Completions and the Responses API can send tool-returned images to models that support image input. After an image or PDF tool result enters the conversation, Chord skips fallback models that do not support the corresponding input type.

Common actions:

- `Ctrl+V` or `Alt+V` in the main composer: asynchronously read an image or PDF from the system clipboard. PNG/JPEG are accepted directly; WebP/GIF/BMP/TIFF are normalized to PNG or JPEG and scaled down to 2000px on the longest edge (animated WebP/GIF/TIFF use their first frame; HEIC/HEIF/AVIF/SVG are rejected with a conversion hint). Images get an inline placeholder such as `[image1.png]`; PDFs are added as file attachments. Pressing Enter while the read is pending asks you to wait, so an immediate submit cannot lose the attachment. Use `Alt+V` in Windows Terminal and WSL sessions hosted by it.
- `Cmd+V`, right-click paste, menu paste, and other terminal paste events: paste text only and never inspect clipboard attachments.
- `Cmd+V` in confirmation-dialog text fields: paste text only.
- Inline image attachments are capped at 5 per composer message
- Typing literal placeholder text such as `[image1]` does not attach an image by itself; only Chord-inserted inline image placeholders are attachment-backed
- `@` file completion includes image/PDF files only when the current model supports that input type. Manually typed image/PDF `@` references are still accepted as attachments; unsupported attachments are ignored at send time with a warning.
- To attach an image or PDF by path, enter the path in the composer and configure a custom `insert_attach_file` key binding
- PDFs that appear to be encrypted are marked with a warning; Chord still allows sending them because provider-side parsing is authoritative.
- `generate_image` and `view_image` tool cards show images directly, without a result JSON pane or a fold toggle. Important warnings, image scaling, and unsuccessful outcomes remain visible.
- `Enter` / `o` / `Space`: open the image in the current user message or tool result in Normal mode
- In the composer, click a real image placeholder once to select the whole attachment, then delete or type to replace it; `Ctrl+Z` restores the edit. Double-click the same placeholder to preview it before sending. Typed placeholder text and PDFs do not open image previews.
- In image preview, use `Left` / `Right` to browse the images present when it opened. Press `Esc` or click outside the preview to close it; composer previews return to the same draft in Insert mode, with the cursor restored and the selection cleared. Reading and preparing images happens in the background. If loading fails, press `r` to retry or close the preview to continue editing.
- Image preview requires a terminal with fullscreen image support; other terminals show a short notice and keep the selected attachment available for editing.
- Transcript thumbnails load in the background and use previews of at most 1024px on the longest edge. Opening the fullscreen viewer uses the stored image at its original resolution; thumbnail preparation does not change attachments or saved images.

Unused image byte and transport caches are released after about 30–45 seconds; image dimensions remain cached so layout stays stable. Kitty images outside the viewport have a short grace period before Chord requests terminal resource deletion, and returning to them reloads them in the background. Visible images remain displayed. Closing or switching images releases Kitty viewer resources; an idle iTerm2 viewer releases its redraw payload after five minutes and rebuilds it on the next redraw. Attachments and saved originals are preserved. Actual process memory falls when Go collects the released data; iTerm2 manages its own bitmap memory.

## Copying text

- Drag in the transcript or main composer to select text. Composer selections preserve actual newlines and trailing spaces when copied, without adding soft line breaks. `Ctrl+Shift+C` copies the composer selection.
- `yy` copies the focused message card; tool cards are copied as Markdown with `# Tool call`, `## Arguments`, `## Result`, and `## Diff` sections (`edit` cards use `## old_string` / `## new_string` for the replaced text instead, plus `## replace_all` only when it is enabled). Done rejection reasons are copied in a separate `## Rejection reason` section.
- `Cmd+C`: copy the current transcript or composer selection in macOS terminals that forward the key to Chord; when a confirmation dialog input is focused, copies that input instead
- `Ctrl+C`: press twice to quit; in a dialog it acts like `Esc` and is not used for transcript copy

## Headless

`chord headless` is useful for:

- bot / gateway integration
- automation scripts
- external control-plane access without a local TUI

It uses:

- stdin: one JSON command per line
- stdout: one JSON event per line

See [Headless](./headless.md) for details.

## Model editing tools

Chord picks the file-editing tool per active model: gpt-5-and-later family names (gpt-5, gpt-5-mini, gpt-5-nano, gpt-5-codex, any `gpt-5.*` name, and later majors like gpt-6-astra) and `codex-auto-review` use `apply_patch`, everything else defaults to `edit`; see [Edit tools](./edit-tools.md) for the full matrix and the rationale. On compatible Responses endpoints, patch-native models additionally receive `apply_patch` as a freeform custom tool instead of a JSON function tool.

When a model name or gateway behaves differently from the inference, override it per provider or model with `compat.apply_patch.enabled` (tool surface) and `compat.apply_patch.freeform` (wire shape). Both keys are three-state: omitted means infer from the model name and endpoint, so you only set the knob you need to change. The authoritative field reference is in [Configuration & Auth](./configuration.md).

## Related

- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Edit tools](./edit-tools.md)
- [Customization](./customization.md)
- [Troubleshooting](./troubleshooting.md)

## Search saved inputs

In Insert mode, press **Ctrl+R** or run `/history` to search inputs submitted in the TUI, including inputs waiting in the send queue. Search defaults to prompts from the current project. Use Tab to focus the project/type filters and Space or arrow keys to switch to all projects or all input types. Up/Down and PageUp/PageDown select results; Ctrl+R selects the next result. Enter replaces the draft without sending it. Esc or Ctrl+C closes the search and preserves your draft. Ctrl+U clears the query; Ctrl+PageUp/PageDown scrolls the preview. Mouse clicks select results and mouse wheels scroll the list or preview.

History keeps the original text and folded pastes, before file references are expanded. Shell inputs restore shell mode. Restoring clears current attachments, and attachments from history must be added again; one Undo restores the previous draft and its attachments. Restoring while editing a queued input preserves that queue item's identity. The up/down composer history remains limited to the current process.

Run `/history clear` to clear **all projects'** saved inputs, plus this process's temporary records accepted before confirmation. Cancel is selected by default. Other processes may still save queued inputs afterwards. Deleting a session does not delete input history. History is local to your user state directory and is not sent to the model automatically.

Prompts, slash commands, and local shell commands are saved by default; attachment-only and generated inputs are excluded. If saving fails, sending continues and a notice explains whether the input is available only in this process or saving could not be confirmed. Failed writes are not retried automatically. See [input history configuration](configuration.md#input-history) to disable recording or change retention.
