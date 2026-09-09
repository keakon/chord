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
| `PgUp` / `PgDown`  | Page the transcript up / down without leaving Insert mode                                      |
| `Shift+Tab`        | Cycle the main agent role shown in the status bar. A switch is announced with a toast (`role: planner → builder`) because it rebuilds permissions, invalidates the cached prompt prefix, and may select the role's own model. On a SubAgent view, where a role switch does not apply, it cycles the focused view instead |
| `Tab`              | Complete the visible slash-command or `@`-mention suggestion; otherwise does nothing            |

### Normal mode — leaving and meta

| Key                | Action                                          |
| ------------------ | ----------------------------------------------- |
| `i`                | Return to Insert mode                           |
| `q`                | Press twice within ~2s to quit                  |
| `Ctrl+C`           | Press twice within ~2s to quit; inside any overlay or dialog it closes the overlay instead (like `Esc`) |
| `?`                | Toggle the in-app help / cheatsheet overlay     |
| `Esc`              | (when agent is running) Cancel the current turn |
| `Shift+Tab`        | Cycle the focused agent view. Main is always available; stopped-but-incomplete SubAgents remain switchable |

`Shift+Tab` is deliberately the same key in both modes, and the mode decides
what it does: a role change is normally followed by typing a message, so it
belongs in Insert mode, while a view change is normally followed by scrolling
and reading, so it belongs in Normal mode. Because only the action belonging to
the current mode is consulted, `switch_role` and `switch_agent` sharing a
default binding is not a conflict. Press `?` to see each mode's effective
bindings separately.

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
| `j`                       | Move to the next message card                                                         |
| `k`                       | Move to the previous message card                                                     |
| `}` / `{`                 | Jump to the next / previous user message card (turn boundary)                         |
| `)` / `(`                 | Jump to the next / previous assistant message card                                    |
| `]` / `[`                 | Jump to the next / previous message card of the same type as the current card          |
| `o` / `Enter` / `Space`   | Toggle collapse / expand on the current card (cards that are always expanded ignore it); on an image card, open the image       |
| `e`                       | Edit the focused user message; forks only when that message is not the transcript tail |

The structural jumps (`}`, `)`, `]` and their counterparts) accept a count prefix, so `3}` moves three user cards forward and `2(` moves two assistant cards backward. Each jump skips all other card types and never lands on error cards; when no matching card exists in that direction the view stays put. `]` / `[` use the focused card's type as the template, or the card at the top of the viewport when nothing is focused.

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

Search covers visible textual content across conversation cards, including user and assistant text, original and translated thinking, tool arguments and output, file diffs, completion reports, compaction summaries, local shell output, and visible attachment or file-reference labels. A matching collapsed card is expanded before Chord scrolls to and highlights the text. Search remains card-based: each matching card counts once, and only the current card's first visible occurrence is highlighted. Image pixels are not OCR-searched, and Markdown syntax or hidden link targets that are not rendered as visible text cannot receive a visible highlight.

Search also covers older regions of lazily loaded large sessions. Chord loads a cold card only long enough to verify that the match is actually visible, then keeps the off-screen region cold until navigation needs it.

### Both modes — agents, models, and integrations

| Key          | Action                                                                                                    |
| ------------ | --------------------------------------------------------------------------------------------------------- |
| `Ctrl+P`     | Open the model-pool selector in both Insert and Normal modes.                                          |
| `Ctrl+R`     | Cycle service tier for subsequent model requests, limited to tiers supported by the current provider/model; `/tier` slash completion predicts the same next tier and is hidden when there is no actual switch target |
| `Ctrl+Y`     | Toggle YOLO mode; ordinary tools skip their permission checks and confirmations; handoff, delegate, cancel, done, and compact_context keep following their configured rules                 |
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
  next_block: ["j"]            # next-card is j only
  prev_block: ["k"]            # prev-card is k only
  next_user_block: ["}"]       # } jumps to the next user card (turn boundary)
  prev_user_block: ["{"]       # { jumps to the previous user card
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
| `insert_page_up`           | `["pgup"]` (page the transcript) |
| `insert_page_down`         | `["pgdown"]` (page the transcript) |
| `enter_insert`             | `["i"]`                          |
| `quit`                     | `["q"]`                          |
| `help_toggle`              | `["?"]`                          |
| `scroll_down`              | `["down"]`                       |
| `scroll_up`                | `["up"]`                         |
| `full_page_down`           | `["ctrl+f", "pgdown"]`          |
| `full_page_up`             | `["ctrl+b", "pgup"]`            |
| `scroll_to_bottom`         | `["G"]`                          |
| `scroll_to_top_seq`        | `["g"]` (first key of `gg`)      |
| `next_block`               | `["j"]`                          |
| `prev_block`               | `["k"]`                          |
| `next_user_block`          | `["}"]`                          |
| `prev_user_block`          | `["{"]`                          |
| `next_assistant_block`     | `[")"]`                          |
| `prev_assistant_block`     | `["("]`                          |
| `next_same_type_block`     | `["]"]`                          |
| `prev_same_type_block`     | `["["]`                          |
| `toggle_collapse`          | `["o", "enter", " ", "space"]`   |
| `fork_session`             | `["e"]`                          |
| `directory`                | `["ctrl+t"]`                     |
| `usage_stats`              | `["$"]`                          |
| `error_panel`              | `["ctrl+e"]`                     |
| `search_start`             | `["/"]`                          |
| `search_next`              | `["n"]`                          |
| `search_prev`              | `["N"]`                          |
| `switch_agent`             | `["shift+tab"]` (Normal mode only) |
| `switch_role`              | `["shift+tab"]` (Insert mode only) |
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
