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

Common keys:

- `Esc`: switch to Normal mode; pressing `Esc` again in the running main view cancels the current turn
- `i`: return to insert/input mode
- `j` / `k`: move between message cards
- `gg` / `G`: jump to top / bottom
- `/`: search messages
- `Ctrl+J`: open the message directory
- `Ctrl+P`: switch model
- `Ctrl+G`: export a diagnostics bundle
- `q`: press twice to quit
- `Ctrl+C`: press twice to quit

## Sessions

Chord keeps persistent sessions for the current project.

Common workflows:

- `chord`: create a new session
- `chord --continue`: resume the most recent non-empty session for this project
- `chord --resume <session-id>`: resume a specific session
- `/new`: create a new session in the TUI
- `/resume`: pick a historical session in the TUI

When exiting, if the current session can be resumed, Chord prints the corresponding resume command.

## Local slash commands

These commands are handled by the local runtime and are not sent to the model as-is:

- `/new`: create a new session
- `/resume`: resume a session
- `/model`: switch the current running model
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

Common actions:

- `Ctrl+V` / `Cmd+V`: prefer clipboard image input; otherwise paste text
- `Ctrl+F`: attach image paths from the input box to the current message
- `Enter` / `o` / `Space`: open the image in the current message in Normal mode

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
