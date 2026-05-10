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
| `desktop_notification` (OSC 9)                      | ⚠️[^osc] | ⚠️[^osc] | ⚠️[^osc]    | ⚠️[^osc]      |
| Clipboard image paste (`Ctrl+V` / `Cmd+V`)          | ✅     | ⚠️[^clip]   | ⚠️[^clip]      | ⚠️[^clip]       |
| Terminal-rendered images (iTerm2/Kitty/Sixel)       | ⚠️[^img]| ⚠️[^img]  | ⚠️[^img]      | ⚠️[^img]        |
| LSP — gopls / typescript / rust-analyzer            | ✅[^lsp]| ✅[^lsp]   | ✅[^lsp]      | ✅[^lsp]        |
| LSP — Pyright with project venv auto-discovery      | ✅[^py-unix] | ✅[^py-unix] | ✅[^py-win] | ✅[^py-unix] (WSL Linux venvs only — see below) |
| MCP servers (stdio / HTTP)                          | ✅     | ✅            | ✅              | ✅               |
| Power-aware idle handling                           | ✅     | ❌ (no-op)   | ❌ (no-op)      | ❌ (no-op)       |

[^im]: Requires the `im-select` binary in `PATH` (`im-select.exe` on Windows). Install separately — Chord ships only the integration, not the binary itself.
[^im-linux]: `im-select` is a macOS-first tool; on Linux you need a compatible build or a wrapper script with the same CLI.
[^im-win]: Use `im-select.exe` (e.g. from <https://github.com/daipeihust/im-select#-windows>).
[^im-wsl]: Inside WSL, IM switching usually targets the host (Windows) IM. You typically run `im-select.exe` over interop and may need PATH or wrapper setup.
[^osc]: OSC 9 is a terminal-side feature, not an OS feature. It works in iTerm2 and many modern terminals; in others (older `xterm`, some bare ttys, certain `tmux` setups), the escape sequence is silently ignored. See [Terminal compatibility](#terminal-compatibility) below.
[^clip]: Clipboard image paste depends on the terminal forwarding the image bytes; not all terminal emulators do. iTerm2 and modern WezTerm/Ghostty work; some tmux/Linux setups deliver only a path or nothing.
[^img]: Image rendering depends on the terminal's image protocol (iTerm2 inline-images, Kitty graphics, Sixel, …). When the terminal does not support any, image attachments are still sent to the model but are not previewed in the TUI.
[^lsp]: Requires the relevant language server installed locally (e.g. `gopls`, `typescript-language-server`, `rust-analyzer`). Chord does not bundle them.
[^py-unix]: On Unix-like systems Chord probes `.venv/bin/python`, `venv/bin/python`, `env/bin/python` under the LSP root.
[^py-win]: On Windows Chord probes `.venv\Scripts\python.exe`, `venv\Scripts\python.exe`, `env\Scripts\python.exe`.

Legend: ✅ supported · ⚠️ supported with caveats · ❌ not supported / no-op.

## Per-feature details

### `prevent_sleep`

macOS uses `caffeinate(1)` under the hood. On Linux / Windows / WSL this setting is a no-op — the implementation is `internal/power/power_other.go` (a `NoopBackend`). If you depend on always-on behavior elsewhere, configure your OS power settings directly.

### `ime_switch_target`

When you switch from Insert mode to Normal mode, Chord can call `im-select` (or `im-select.exe`) to switch to a configured input method (typically the system English layout) and restore the previous one when you switch back to Insert.

```yaml
# ~/.config/chord/config.yaml
ime_switch_target: com.apple.keylayout.ABC          # macOS example
# ime_switch_target: 1033                           # Windows example (locale id)
```

Install `im-select` separately. The variable name is just a string — Chord passes it verbatim to `im-select`, so the format depends on the platform-specific tool.

### `desktop_notification` (OSC 9)

When enabled, Chord emits OSC 9 escape sequences for events such as permission confirmations, questions waiting for input, and agents returning to idle. There is no Chord-side notifier daemon; the terminal is responsible for surfacing the notification (iTerm2, WezTerm, Ghostty, kitty, and others do).

Inside `tmux` you may need `set -g allow-passthrough on` for OSC 9 to reach the host terminal.

### Clipboard image paste

`Ctrl+V` (`Cmd+V` on macOS) prefers clipboard images over text. The actual delivery depends on the terminal:

- **macOS + iTerm2 / WezTerm / Ghostty**: works.
- **Linux Wayland**: depends on terminal support; some only deliver `text/uri-list`.
- **Linux X11**: terminal-dependent.
- **Windows Terminal**: works for many cases.
- **Inside tmux**: image bytes may not pass through.

When clipboard image paste is unavailable, you can still bind `insert_attach_file` yourself and attach images by path from the composer.

### Terminal-rendered images

Chord auto-detects iTerm2 inline-images, Kitty graphics protocol, and Sixel. If none is available, image attachments are still sent to the model — they just are not previewed in the TUI.

### Pyright venv auto-discovery

When no Python interpreter is configured for Pyright, Chord probes a project-local venv under the LSP root, in this order:

- Unix-like (macOS, Linux, WSL): `.venv/bin/python` → `venv/bin/python` → `env/bin/python`
- Windows: `.venv\Scripts\python.exe` → `venv\Scripts\python.exe` → `env\Scripts\python.exe`

WSL auto-discovery intentionally **does not** pick up Windows venvs under `Scripts\python.exe`. If you work inside WSL, create a Linux venv inside WSL, or set `python.pythonPath` explicitly under `lsp.pyright.options`.

For more, see [Customization — LSP](./customization.md#lsp).

## Terminal compatibility

Most "this works on macOS but not on my Linux box" reports really come down to the terminal emulator, not the OS. Recommended terminals where Chord behaves best:

- **iTerm2** (macOS) — image preview, OSC 9, clipboard image paste
- **Ghostty** (cross-platform) — image preview, OSC 9
- **WezTerm** (cross-platform) — image preview, OSC 9, clipboard image paste
- **kitty** (Linux/macOS) — Kitty graphics protocol, OSC 9
- **Windows Terminal** — works as a general TUI; image protocol limited

`tmux` and `screen` add a layer between Chord and your terminal; some features (OSC 9, certain image flows) require explicit pass-through configuration.

## What Windows users should expect

Chord runs on Windows but is not the primary platform. Concretely:

- TUI works in modern terminals (Windows Terminal, WezTerm).
- `prevent_sleep` is a no-op — use Windows power settings.
- `ime_switch_target` works with `im-select.exe`.
- File paths in tool calls follow Windows conventions; backslashes are preserved verbatim.
- `Shell` and `Spawn` remain non-interactive on Windows too, but timeout/cancellation cleanup uses direct process termination instead of Unix-style session/process-group control; descendant process cleanup may therefore be less complete than on Unix.
- If you hit a Windows-specific bug, it is more likely to be undiscovered than a deliberate limitation. Capture a diagnostics bundle (`Ctrl+G` or `/diagnostics`) and report it.

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
- A diagnostics bundle (`Ctrl+G` or `/diagnostics`)

See [Troubleshooting — When to check logs](./troubleshooting.md#when-to-check-logs) for log location and bundle layout.

## Related

- [Configuration & Auth](./configuration.md)
- [Customization](./customization.md)
- [Troubleshooting](./troubleshooting.md)
