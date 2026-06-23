# Platform Support

Chord is developed and tested primarily on macOS. Other platforms work to varying degrees: most features are platform-agnostic, but a few depend on OS-specific machinery and degrade or no-op elsewhere. This page lists where features really work, where they fall back, and what to install on each OS.

## Quick matrix

| Feature                                             | macOS | Linux        | Windows        | WSL             |
| --------------------------------------------------- | ----- | ------------ | -------------- | --------------- |
| Core TUI (modes, transcript, sessions)              | ✅     | ✅            | ⚠️ (best-effort) | ✅               |
| `chord headless` JSON control plane                 | ✅     | ✅            | ✅              | ✅               |
| Worktrees (`chord --worktree`, `chord worktree …`)  | ✅     | ✅            | ✅              | ✅               |
| `prevent_sleep` (idle-sleep prevention)             | ✅     | ❌ (no-op)   | ❌ (no-op)      | ❌ (no-op)       |
| `ime_switch_target` (auto IM switch on mode change) | ✅[^im]| ⚠️[^im-linux]| ✅[^im-win]    | ⚠️[^im-wsl]     |
| `desktop_notification` (terminal notifications)       | ⚠️[^osc] | ⚠️[^osc] | ⚠️[^osc]    | ⚠️[^osc]      |
| Clipboard image paste (`Ctrl+V` / `Cmd+V`)          | ✅     | ⚠️[^clip]   | ⚠️[^clip]      | ⚠️[^clip]       |
| Terminal-rendered images (Kitty / iTerm2)           | ⚠️[^img]| ⚠️[^img]  | ⚠️[^img]      | ⚠️[^img]        |
| LSP — gopls / typescript / rust-analyzer            | ✅[^lsp]| ✅[^lsp]   | ✅[^lsp]      | ✅[^lsp]        |
| LSP — Pyright with project venv auto-discovery      | ✅[^py-unix] | ✅[^py-unix] | ✅[^py-win] | ✅[^py-unix] (WSL Linux venvs only — see below) |
| MCP servers (stdio / HTTP)                          | ✅     | ✅            | ✅              | ✅               |
| Power-aware idle handling                           | ✅     | ❌ (no-op)   | ❌ (no-op)      | ❌ (no-op)       |

[^im]: Requires the `im-select` binary in `PATH` (`im-select.exe` on Windows). Install separately — Chord ships only the integration, not the binary itself.
[^im-linux]: `im-select` is a macOS-first tool; on Linux you need a compatible build or a wrapper script with the same CLI.
[^im-win]: Use `im-select.exe` (e.g. from <https://github.com/daipeihust/im-select#-windows>).
[^im-wsl]: Inside WSL, IM switching usually targets the host (Windows) IM. You typically run `im-select.exe` over interop and may need PATH or wrapper setup.
[^osc]: Notification support is a terminal capability, not an OS capability. Chord auto-selects a notification escape sequence by terminal (OSC 9 or OSC 777). Some terminals may ignore unsupported sequences; see [Terminal compatibility](#terminal-compatibility) below.
[^clip]: Clipboard image paste depends on whether the terminal forwards raw image bytes, emits an empty paste event and expects the app to read the system clipboard, or pastes only text. Chord attaches real clipboard image data when available; text remains text.
[^img]: Image rendering currently auto-detects Kitty graphics and iTerm2 inline images (Ghostty uses Kitty; WezTerm uses iTerm2). When the terminal does not support those, image attachments are still sent to the model but are not previewed in the TUI. `tmux` / `zellij` are disabled by default for safety.
[^lsp]: Requires the relevant language server installed locally (e.g. `gopls`, `typescript-language-server`, `rust-analyzer`). Chord does not bundle them.
[^py-unix]: On Unix-like systems Chord probes `.venv/bin/python`, `venv/bin/python`, `env/bin/python` under the LSP root.
[^py-win]: On Windows Chord probes `.venv\Scripts\python.exe`, `venv\Scripts\python.exe`, `env\Scripts\python.exe`.

Legend: ✅ supported · ⚠️ supported with caveats · ❌ not supported / no-op.

## Per-feature details

### `prevent_sleep`

macOS uses `caffeinate(1)` under the hood. On Linux / Windows / WSL this setting is a no-op. If you depend on always-on behavior elsewhere, configure your OS power settings directly.

The first-run setup wizard asks about `prevent_sleep` only on macOS, and only as an explicit opt-in confirmation. It is intended for longer-running agent sessions where idle sleep would be disruptive.

### `ime_switch_target`

When you switch from Insert mode to Normal mode, Chord can call `im-select` (or `im-select.exe`) to switch to a configured input method (typically the system English layout) and restore the previous one when you switch back to Insert.

On supported platforms, the first-run setup wizard can also ask for this value. Skip it unless you actively use a non-Latin IME and want more reliable Normal-mode shortcuts.

```yaml
# ~/.config/chord/config.yaml
ime_switch_target: com.apple.keylayout.ABC          # macOS example
# ime_switch_target: 1033                           # Windows example (locale id)
```

Install `im-select` separately. The variable name is just a string — Chord passes it verbatim to `im-select`, so the format depends on the platform-specific tool.

### `desktop_notification` (terminal notifications)

When enabled, Chord emits terminal notification escape sequences for events such as permission confirmations, questions waiting for input, and agents returning to idle. There is no Chord-side notifier daemon; the terminal is responsible for surfacing the notification.

Chord auto-selects the protocol by terminal. Unsupported terminals usually ignore the sequence.

In practice:

- **Ghostty / WezTerm / Windows Terminal**: Chord attempts **OSC 777**
- **iTerm2**: Chord uses **OSC 9**
- **Other terminals**: Chord conservatively falls back to **OSC 9**

Inside `tmux` you may need `set -g allow-passthrough on` for notifications to reach the host terminal.

### Clipboard image paste

`Ctrl+V` (`Cmd+V` on macOS) is a smart paste: it prefers an image attachment, then falls back to text. Different terminals hand clipboard images to applications differently: some forward raw image bytes, some emit an empty paste event and expect the app to read the system clipboard, and some can only paste plain text.

Chord currently handles these cases conservatively:

- **Raw image bytes available**: attach as an image
- **Anything else**: paste as ordinary text

Inline image attachments are capped at 5 per composer message. Literal placeholder text like `[image1]` is not special by itself; only Chord-inserted inline placeholders are backed by real attachments.

Common cases:

- **macOS + iTerm2 / WezTerm / Ghostty**: usually paste images directly
- **macOS + cmux**: use `Ctrl+V` for clipboard images. `Cmd+V` may be intercepted by cmux and converted into a pasted temporary file path, which can appear after Chord's image placeholder.
- **Linux Wayland / X11**: terminal-dependent; some terminals only paste plain text for clipboard images
- **Windows Terminal**: text paste is fine; image behavior depends more on the terminal / host chain
- **Inside tmux**: raw image bytes often do not pass through

When clipboard image paste is unavailable, you can still bind `insert_attach_file` and attach images by path from the composer.

### Terminal-rendered images

Chord currently auto-detects and enables:

- **Kitty graphics** (kitty, Ghostty)
- **iTerm2 inline images** (iTerm2, WezTerm)

If neither protocol is available, image attachments are still sent to the model — they just are not previewed in the TUI.

Notes:

- **Sixel is not currently implemented as a Chord backend**
- **Inside `tmux` / `zellij`, image preview is conservatively disabled by default** to avoid common passthrough / placeholder issues
- Advanced users can override auto-detection with `CHORD_IMAGE_BACKEND=kitty|iterm2|none`, plus `CHORD_IMAGE_INLINE=0|1` and `CHORD_IMAGE_FULLSCREEN=0|1`

### Pyright venv auto-discovery

When no Python interpreter is configured for Pyright, Chord probes a project-local venv under the LSP root, in this order:

- Unix-like (macOS, Linux, WSL): `.venv/bin/python` → `venv/bin/python` → `env/bin/python`
- Windows: `.venv\Scripts\python.exe` → `venv\Scripts\python.exe` → `env\Scripts\python.exe`

WSL auto-discovery intentionally **does not** pick up Windows venvs under `Scripts\python.exe`. If you work inside WSL, create a Linux venv inside WSL, or set `python.pythonPath` explicitly under `lsp.pyright.options`.

For more, see [Customization — LSP](./customization.md#lsp).

## Terminal compatibility

Most "this works on macOS but not on my Linux box" reports really come down to the terminal emulator, not the OS. Recommended terminals where Chord behaves best:

- **iTerm2** (macOS) — image preview, terminal notifications, clipboard image paste
- **Ghostty** (cross-platform) — image preview, terminal notifications (tries OSC 777)
- **WezTerm** (cross-platform) — image preview, terminal notifications (tries OSC 777), clipboard image paste
- **kitty** (Linux/macOS) — image preview, terminal notifications
- **macOS Terminal.app** works for basic TUI use, but it does **not** reliably deliver modified `Enter` keys (for example `Shift+Enter`). Use `Ctrl+J` for newline in the composer, or switch to iTerm2 / Ghostty / WezTerm for full key behavior.

Key disambiguation note: in terminal multiplexers like `tmux` / `zellij`, modified keys such as `Shift+Enter` can be lost or rewritten unless the host chain is configured for extended keys. When in doubt, use `Ctrl+J` for newline (it works everywhere).

`tmux` and `screen` add a layer between Chord and your terminal; some features (terminal notifications, certain image flows) require explicit pass-through configuration, and Chord currently disables image preview by default inside `tmux` / `zellij`.

## What Windows users should expect

Chord runs on Windows but is not the primary platform. Concretely:

- TUI works in modern terminals (Windows Terminal, WezTerm).
- `prevent_sleep` is a no-op — use Windows power settings.
- `ime_switch_target` works with `im-select.exe`.
- File paths in tool calls follow Windows conventions; backslashes are preserved verbatim.
- `shell` and `spawn` remain non-interactive on Windows too, but timeout/cancellation cleanup uses direct process termination instead of Unix-style session/process-group control; descendant process cleanup may therefore be less complete than on Unix.
- If you hit a Windows-specific bug, it is more likely to be undiscovered than a deliberate limitation. Capture a diagnostics bundle with `Ctrl+G` and report it.

## What WSL users should expect

WSL behaves like Linux for the most part:

- Chord runs as a Linux binary inside WSL; sessions and config use Linux paths (`~/.config/chord/`, etc.).
- `prevent_sleep` is a no-op; use Windows power settings on the host.
- `ime_switch_target` typically goes through Windows interop (`im-select.exe`).
- Pyright venv auto-discovery uses **Linux** venvs (`.venv/bin/python` etc.) inside WSL — Windows-style `Scripts\python.exe` venvs are intentionally not selected.
- Terminal capabilities depend on the Windows terminal hosting WSL (Windows Terminal, WezTerm, Ghostty).

## Reporting platform-specific issues

When reporting a bug that you suspect is platform-related, include:

- OS and version
- Terminal emulator and version
- Whether you are inside `tmux` / `screen` / WSL
- A diagnostics bundle (`Ctrl+G`)

See [Troubleshooting — When to check logs](./troubleshooting.md#when-to-check-logs) for log location and bundle layout.

## Related

- [Configuration & Auth](./configuration.md)
- [Customization](./customization.md)
- [Troubleshooting](./troubleshooting.md)
