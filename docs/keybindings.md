# Keybindings

This page is the complete reference for Chord's TUI key bindings. Every binding listed here can be remapped via the `keymap:` section in `config.yaml`.

## Modes

The TUI has two modes:

- **Insert mode** — the input box is focused; you type messages
- **Normal mode** — the transcript is focused; you navigate, search, fold, scroll, etc.

Press `Esc` to leave Insert mode for Normal mode; press `i` (or any unbound printable key) to return to Insert mode. While the agent is running, pressing `Esc` a second time in Normal mode cancels the current turn.

## Quick reference

### Insert mode

| Key                | Action                                                                                         |
| ------------------ | ---------------------------------------------------------------------------------------------- |
| `Esc`              | Leave Insert mode, switch to Normal mode                                                       |
| `Enter`            | Complete the visible slash-command suggestion; otherwise send the message                         |
| `Shift+Enter`      | Insert a newline                                                                               |
| `Ctrl+J`           | Insert a newline (alternative when terminal does not deliver `Shift+Enter`)                    |
| `Up`               | Recall the previous user message into the composer (or move history up if composer non-empty)  |
| `Down` / `Ctrl+N`  | Move history down                                                                              |
| `Ctrl+V` / `Alt+V` | Attach an image or PDF from the system clipboard asynchronously; `Alt+V` works when the terminal reserves `Ctrl+V` |
| `Cmd+V` / paste    | Paste text only; terminal paste events never probe clipboard attachments                       |
| `Ctrl+U`           | Clear the input box and pending attachments                                                    |

### Normal mode — leaving and meta

| Key                | Action                                          |
| ------------------ | ----------------------------------------------- |
| `i`                | Return to Insert mode                           |
| `q`                | Press twice within ~2s to quit                  |
| `Ctrl+C`           | Press twice within ~2s to quit                  |
| `?`                | Toggle the in-app help / cheatsheet overlay     |
| `Esc`              | (when agent is running) Cancel the current turn |

### Normal mode — scrolling

| Key                  | Action                                                   |
| -------------------- | -------------------------------------------------------- |
| `↓` / `↑`            | Scroll one line                                          |
| `Ctrl+F` / `PgDown`  | Scroll one full page down                                |
| `Ctrl+B` / `PgUp`    | Scroll one full page up                                  |
| `G`                  | Jump to the bottom                                       |
| `gg`                 | Jump to the top (two-key sequence)                       |

### Normal mode — message blocks

| Key                       | Action                                                                                |
| ------------------------- | ------------------------------------------------------------------------------------- |
| `j` / `}`                 | Move to the next message card                                                         |
| `k` / `{`                 | Move to the previous message card                                                     |
| `o` / `Enter` / `Space`   | Toggle collapse / expand on the current card; on an image card, open the image       |
| `e`                       | Edit the focused user message; forks only when that message is not the transcript tail |

### Normal mode — overlays

| Key       | Action                                                              |
| --------- | ------------------------------------------------------------------- |
| `Ctrl+T`  | Open the message directory (jump-to-card overlay)                   |
| `Ctrl+E`  | Open the error panel                                                |
| `$`       | Open the usage statistics overlay                                   |

### Normal mode — search

| Key      | Action                                                |
| -------- | ----------------------------------------------------- |
| `/`      | Start a search                                        |
| `n`      | Jump to the next match                                |
| `N`      | Jump to the previous match                            |

While entering a search, `Enter` confirms it and `Esc` cancels it. `Backspace` edits a non-empty query normally; when the query is already empty, `Backspace` cancels the search like `Esc`. Therefore, deleting the final character keeps search mode active, and one more `Backspace` exits it, matching Vim behavior.

### Both modes — agents, models, and integrations

| Key          | Action                                                                                                    |
| ------------ | --------------------------------------------------------------------------------------------------------- |
| `Tab`        | Cycle the main agent mode (role) shown in the status bar (main view only)                                    |
| `Shift+Tab`  | Cycle the focused agent view (main agent and any active SubAgents)                                          |
| `Ctrl+P`     | Open the model-pool selector in both Insert and Normal modes.                                          |
| `Ctrl+R`     | Cycle service tier for subsequent model requests, limited to tiers supported by the current provider/model; `/tier` slash completion predicts the same next tier and is hidden when there is no actual switch target |
| `Ctrl+Y`     | Toggle YOLO mode; bypasses main-agent permissions except handoff, delegate, cancel, and done                 |
| `Ctrl+O`     | Open the MCP server selector; manual changes while running apply on the next model request                   |
| `Ctrl+G`     | Export a diagnostics bundle                                                                               |

### Note on `Ctrl+O` and MCP

`Ctrl+O` opens the MCP server selector in both Insert and Normal mode. You can open it while the agent is running to inspect server status and toggle manual servers; changes made during a running turn apply to the next model request. Only servers configured with `manual: true` can be toggled; auto-start servers are always read-only in the selector.

### Mouse text selection

Transcript cards, the composer input, and Done/Handoff Markdown viewers share the same mouse selection gestures: drag to select a range, double-click to select the current word, and triple-click to select the current visible line.

### Content viewer — Done reports and Handoff plans

Done confirmation dialogs and Handoff plan selectors can open a read-only Markdown viewer with `V`. The viewer keeps the right sidebar visible, supports mouse-wheel scrolling, and shows `esc ⇢ close view` in the status bar.

| Key / Mouse              | Action                                                                    |
| ------------------------ | ------------------------------------------------------------------------- |
| `Esc` / `q`              | Close the viewer and return to the previous Done or Handoff dialog        |
| `j` / `k`, `↓` / `↑`     | Scroll one line                                                           |
| `Ctrl+F` / `Ctrl+B`      | Scroll one page down / up                                                 |
| `g` / `G`                | Jump to top / bottom                                                      |
| Mouse drag               | Select and highlight text in the viewer                                   |
| `Cmd+C` / `Super+C`      | Copy the highlighted range only                                           |
| `y`                      | Copy the highlighted range and clear the highlight                        |
| `yy`                     | Copy the full raw Markdown content                                        |

Handoff plan views include the plan path at the top so it can be selected and copied with the same controls.

## Customizing key bindings

You can override any binding in `config.yaml`:

```yaml
keymap:
  next_block: ["j"]            # disable the } alias for next-card
  prev_block: ["k"]            # disable the { alias for prev-card
  scroll_down: ["down"]        # arrow keys for line scrolling only
  scroll_up: ["up"]
  quit: ["Q"]                  # require shift for quit
  switch_model: ["ctrl+t"]     # choose a different key if you prefer
```

### Terminal compatibility notes

Custom key bindings only work when your terminal emulator, OS, and any multiplexer such as tmux forward that key sequence to Chord. Prefer plain printable keys in Normal mode or simple `ctrl+letter` combinations that do not already have strong terminal meanings.

`Cmd+V` on macOS is often handled by the terminal or wrapper before Chord can see it. Chord treats forwarded `super+v` and terminal paste events as text-only. Use `Ctrl+V` or `Alt+V` (the `insert_attach_clipboard` action) to read an image or PDF from the system clipboard. Windows Terminal reserves `Ctrl+V` for text paste by default, so use `Alt+V` there and in WSL sessions hosted by it. In cmux, image-only `Cmd+V` may still be converted by the cmux/Ghostty paste layer into a temporary file path; Chord treats that path as ordinary pasted text.

Avoid these as default/custom bindings unless you have tested them in your exact terminal setup:

- Other `alt+letter` / Option combinations on macOS: terminals such as Ghostty may use Option for text input, menu shortcuts, or app-level bindings, so verify custom combinations in your terminal. `Alt+V` is provided primarily as the Windows Terminal/WSL attachment fallback.
- `ctrl+i`, `ctrl+m`, and `ctrl+[`: traditional terminals encode these the same as `Tab`, `Enter`, and `Esc`.
- `ctrl+s` and `ctrl+q`: these can be intercepted by software flow control.
- `ctrl+c`, `ctrl+z`, and `ctrl+\\`: these have signal/cancel/suspend meanings in terminals.
- Function keys or `ctrl+shift+...` combinations: support varies across terminals, keyboard layouts, SSH, and tmux.

If a custom binding does not work, press `?` to confirm Chord loaded the mapping, then check whether the terminal receives the key with tools such as `showkey`, `cat`, or your terminal's key-event inspector.

Action names are lower snake_case mirrors of the [`KeyMap` fields](https://github.com/keakon/chord/blob/main/internal/tui/keymap.go) in `internal/tui/keymap.go`. Keys are the strings produced by Bubble Tea's `tea.KeyMsg.String()`, e.g. `"esc"`, `"enter"`, `"shift+enter"`, `"ctrl+p"`, `"ctrl+shift+left"`, `"j"`, `"down"`, `"space"`, `" "`.

### Action name reference

Action names here are the names used in `config.yaml` (for `keymap:`).

| Action                     | Default                          |
| -------------------------- | -------------------------------- |
| `insert_escape`            | `["esc"]`                        |
| `insert_submit`            | `["enter"]`                      |
| `insert_newline`           | `["shift+enter", "ctrl+j"]`      |
| `insert_history_up`        | `["up"]`                          |
| `insert_history_down`      | `["down", "ctrl+n"]`             |
| `insert_attach_clipboard`  | `["ctrl+v", "alt+v"]` (attach a clipboard image or PDF) |
| `insert_attach_file`       | `[]`                              |
| `insert_clear_input`       | `["ctrl+u"]`                     |
| `enter_insert`             | `["i"]`                          |
| `quit`                     | `["q"]`                          |
| `help_toggle`              | `["?"]`                          |
| `scroll_down`              | `["down"]`                       |
| `scroll_up`                | `["up"]`                         |
| `full_page_down`           | `["ctrl+f", "pgdown"]`          |
| `full_page_up`             | `["ctrl+b", "pgup"]`            |
| `scroll_to_bottom`         | `["G"]`                          |
| `scroll_to_top_seq`        | `["g"]` (first key of `gg`)      |
| `next_block`               | `["j", "}"]`                     |
| `prev_block`               | `["k", "{"]`                     |
| `toggle_collapse`          | `["o", "enter", " ", "space"]`   |
| `fork_session`             | `["e"]`                          |
| `directory`                | `["ctrl+t"]`                     |
| `usage_stats`              | `["$"]`                          |
| `error_panel`              | `["ctrl+e"]`                     |
| `search_start`             | `["/"]`                          |
| `search_next`              | `["n"]`                          |
| `search_prev`              | `["N"]`                          |
| `switch_agent`             | `["shift+tab"]`                  |
| `switch_role`              | `["tab"]`                        |
| `switch_model`             | `["ctrl+p"]`                     |
| `service_tier`             | `["ctrl+r"]`                     |
| `yolo`                     | `["ctrl+y"]`                     |
| `mcp`                      | `["ctrl+o"]`                     |
| `diagnostics`              | `["ctrl+g"]`                     |

Only the actions you list are overridden; all others fall back to the defaults above.

## Discovering bindings at runtime

Press `?` in Normal mode to toggle an in-app cheatsheet that reflects your current effective bindings — useful after you have customized `keymap`.

## Related

- [Usage](./usage.md) — workflow context for the bindings above
- [Configuration & Auth](./configuration.md) — full `config.yaml` schema
- [Customization](./customization.md) — agents, hooks, skills, MCP, LSP
