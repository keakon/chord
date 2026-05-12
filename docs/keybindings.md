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
| `Ctrl+V` / `Cmd+V` | Smart paste: prefer an image attachment when the clipboard exposes image data; otherwise paste text |
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

| Key                | Action                                                   |
| ------------------ | -------------------------------------------------------- |
| `↓` / `↑`          | Scroll one line                                          |
| `Ctrl+F`           | Scroll one full page down                                |
| `Ctrl+B`           | Scroll one full page up                                  |
| `G`                | Jump to the bottom                                       |
| `gg`               | Jump to the top (two-key sequence)                       |

### Normal mode — message blocks

| Key                       | Action                                                                                |
| ------------------------- | ------------------------------------------------------------------------------------- |
| `j` / `}`                 | Move to the next message card                                                         |
| `k` / `{`                 | Move to the previous message card                                                     |
| `o` / `Enter` / `Space`   | Toggle collapse / expand on the current card; on an image card, open the image       |
| `e`                       | Edit / fork the focused user message into a new turn                                  |

### Normal mode — overlays

| Key       | Action                                                              |
| --------- | ------------------------------------------------------------------- |
| `Ctrl+T`  | Open the message directory (jump-to-card overlay)                   |
| `$`       | Open the usage statistics overlay                                   |

### Normal mode — search

| Key      | Action                                                |
| -------- | ----------------------------------------------------- |
| `/`      | Start a search                                        |
| `n`      | Jump to the next match                                |
| `N`      | Jump to the previous match                            |

### Both modes — agents, models, and integrations

| Key          | Action                                                                                                    |
| ------------ | --------------------------------------------------------------------------------------------------------- |
| `Tab`        | Cycle the main agent mode (role) shown in the status bar (main view only)                                    |
| `Shift+Tab`  | Cycle the focused agent view (main agent and any active SubAgents)                                          |
| `Ctrl+P`     | Open the model-pool selector in both Insert and Normal modes.                                          |
| `Ctrl+O`     | Open the MCP server selector; read-only while the agent is running                                      |
| `Ctrl+G`     | Export a diagnostics bundle                                                                               |

### Note on `Ctrl+O` and MCP

`Ctrl+O` opens the MCP server selector in both Insert and Normal mode. You can open it while the agent is running to inspect server status, but the panel is read-only until the agent returns to idle. Only servers configured with `manual: true` can be toggled; auto-start servers are always read-only in the selector.

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
| `insert_attach_clipboard`  | `["ctrl+v"]` (`Cmd+V` follows the same smart-paste behavior in terminals that forward it) |
| `insert_attach_file`       | `[]`                              |
| `insert_clear_input`       | `["ctrl+u"]`                     |
| `enter_insert`             | `["i"]`                          |
| `quit`                     | `["q"]`                          |
| `help_toggle`              | `["?"]`                          |
| `scroll_down`              | `["down"]`                       |
| `scroll_up`                | `["up"]`                         |
| `full_page_down`           | `["ctrl+f"]`                     |
| `full_page_up`             | `["ctrl+b"]`                     |
| `scroll_to_bottom`         | `["G"]`                          |
| `scroll_to_top_seq`        | `["g"]` (first key of `gg`)      |
| `next_block`               | `["j", "}"]`                     |
| `prev_block`               | `["k", "{"]`                     |
| `toggle_collapse`          | `["o", "enter", " ", "space"]`   |
| `fork_session`             | `["e"]`                          |
| `directory`                | `["ctrl+t"]`                     |
| `usage_stats`              | `["$"]`                          |
| `search_start`             | `["/"]`                          |
| `search_next`              | `["n"]`                          |
| `search_prev`              | `["N"]`                          |
| `switch_agent`             | `["shift+tab"]`                  |
| `switch_role`              | `["tab"]`                        |
| `switch_model`             | `["ctrl+p"]`                     |
| `mcp`                      | `["ctrl+o"]`                     |
| `diagnostics`              | `["ctrl+g"]`                     |

Only the actions you list are overridden; all others fall back to the defaults above.

## Discovering bindings at runtime

Press `?` in Normal mode to toggle an in-app cheatsheet that reflects your current effective bindings — useful after you have customized `keymap`.

## Related

- [Usage](./usage.md) — workflow context for the bindings above
- [Configuration & Auth](./configuration.md) — full `config.yaml` schema
- [Customization](./customization.md) — agents, hooks, skills, MCP, LSP
