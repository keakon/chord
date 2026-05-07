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

When Chord is running in the background, the terminal title shows a one-shot `✅` completion marker when the focused agent transitions from busy to idle. Focusing the terminal clears the marker; ordinary tab/window focus changes do not re-add it unless new background work later completes.

Common keys:

- `Esc`: switch to Normal mode; pressing `Esc` again in the running main view cancels the current turn
- `i`: return to insert/input mode
- `j` / `k`: move between message cards
- `gg` / `G`: jump to top / bottom
- `/`: search messages
- `Ctrl+J`: open the message directory
- `Ctrl+P`: switch the main role model pool
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
- `chord import <source> <file>`: import an external session (currently supports `opencode` export JSON) into Chord's session store
- `/new`: create a new session in the TUI
- `/resume`: pick a historical session in the TUI

When exiting, if the current session can be resumed, Chord prints the corresponding resume command.

### Importing external sessions

Chord can import an external agent session into a resumable Chord session.

Currently supported sources:

- `opencode`: JSON from `opencode export <sessionID>`

Example:

```bash
opencode export <sessionID> > export.json
chord import opencode export.json
chord resume <sid>
```

Notes (Phase 1):

- Tool calls/results are imported as plain text (no structured tool replay).
- Reasoning is not imported as provider thinking payload. By default (`--reasoning strict`) non-signed reasoning is dropped; use `--reasoning visible` to include it as plain text.
- The imported session contains an `import-report.json` with conversion warnings and stats.

Common flags:

- `--project <path>`: which project to write into (default: current directory)
- `--sid <id>`: specify session id (default: auto-generated)
- `--dry-run`: parse and report only, no writes
- `--json`: machine-readable output
- `--force`: overwrite an existing `--sid`

## Worktrees

For working on multiple tasks in parallel without crosstalk, Chord can create and run inside dedicated git worktrees:

- `chord --worktree`: create or enter a chord-managed worktree (auto-named when no name is given)
- `chord --worktree feat-auth`: create or enter the worktree named `feat-auth` (branch `chord/feat-auth`); combine with `--continue` or `--resume` to act on the worktree's own session history
- `chord headless -d <repo> --worktree feat-auth`: same in headless mode; the `ready` event payload includes the worktree's `name`, `branch`, `path`, and `repo_root`
- `chord worktree list`: list chord-managed worktrees of the current repository
- `chord worktree remove <name>`: delete the worktree and its sessions/cache/exports; the branch is preserved by default. Pass `--delete-branch` to delete only-if-merged or `--force` to force-remove a dirty worktree and its branch.
- `chord worktree finish <name>`: rebase the worktree branch onto the main line (default: the main worktree's current branch), fast-forward that main branch to include it, then remove the worktree and delete its branch. Use `--onto <branch>` to pick the target branch and `--force` to relax clean-tree checks. If rebase hits conflicts, the command now prints guided recovery steps (`git status`, `git rebase --show-current-patch`, then choose `--skip` / `--continue` / `--abort`) and keeps the worktree/branch so you can resolve and rerun. If a rebase is already in progress in that worktree, `finish` exits early with an explicit "complete that rebase first" hint instead of attempting another rebase.

Creating/entering a worktree is a startup-level action (it changes the project chord runs in), so it lives on the `chord` flag rather than under `chord worktree`. The `worktree` subcommand only owns pure management operations (`list`, `remove`, `finish`).

Worktrees live under `<state-dir>/worktrees/<repo-id>/<slug>` (outside the repository) and each gets its own project key, so sessions and caches are isolated automatically. Worktrees contain only tracked files; uncommitted changes in the main repository are not copied across.

## Local slash commands

These commands are handled by the local runtime and are not sent to the model as-is:

- `/new`: create a new session
- `/resume`: resume a session
- `/models`: view pool status or switch the current view's model pool (`main` view = current main role; `SubAgent` view = that agent)
- `/models --agent <name> <pool>`: directly set a named agent's pool
- `/export`: export the current session
- `/compact`: manually trigger context compaction
- `/stats`: view usage statistics
- `/diagnostics`: export a diagnostics bundle for troubleshooting
- `/loop on [target]` / `/loop off`: enable or disable continuous execution mode

## Multi-agent focus switching

Chord supports cooperation between MainAgent and SubAgents.

- `Tab`: switch the main agent role
- `Shift+Tab`: switch focus between the main agent and subagents

In a SubAgent view, you can inspect that agent's context and output. Finished SubAgent views are read-only.

## Images

Currently supported:

- Paste images from the clipboard
- Attach image files to the currently focused agent's message
- View images directly in supported terminals
- Edit/fork historical user messages that contain images; path-restored images are reloaded when the edited message is sent again

Common actions:

- `Ctrl+V` / `Cmd+V`: prefer clipboard image input; otherwise paste text
- `Ctrl+F`: attach image paths from the input box to the current message
- `Enter` / `o` / `Space`: open the image in the current message in Normal mode

## Copying text

- Drag in the transcript to select text inside the TUI
- `Cmd+C`: copy the current transcript selection in macOS terminals that forward the key to Chord
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
