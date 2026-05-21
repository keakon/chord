# 快捷键速查

Chord TUI 全部快捷键的完整参考。下列键位均可通过 `config.yaml` 的 `keymap:` 重新映射。

## 模式

TUI 有两种模式：

- **Insert（输入模式）**：输入框聚焦，正在打字
- **Normal（普通模式）**：消息区聚焦，用于浏览、搜索、折叠、滚动等

按 `Esc` 从 Insert 切到 Normal；按 `i`（或任意未绑定的可见字符）从 Normal 切回 Insert。agent 正在执行时，Normal 模式下再按一次 `Esc` 会取消当前轮次。

## 速查表

### Insert 模式

| 按键               | 动作                                                                                                |
| ------------------ | --------------------------------------------------------------------------------------------------- |
| `Esc`              | 离开 Insert 模式，进入 Normal 模式                                                                  |
| `Enter`            | 补全当前显示的 slash 命令候选；没有候选时发送消息                                                  |
| `Shift+Enter`      | 输入换行                                                                                            |
| `Ctrl+J`           | 输入换行（终端不传 `Shift+Enter` 时的备选）                                                         |
| `Up`               | 输入框为空时载入上一条用户消息；非空时历史上翻                                                       |
| `Down` / `Ctrl+N`  | 历史下翻                                                                                            |
| `Ctrl+V` / `Cmd+V` | 智能粘贴：剪贴板能提供图片数据时优先粘图；否则按文本粘贴                      |
| `Ctrl+U`           | 清空输入框和待发送附件                                                                              |

### Normal 模式 — 退出与元操作

| 按键               | 动作                                              |
| ------------------ | ------------------------------------------------- |
| `i`                | 回到 Insert 模式                                  |
| `q`                | 2 秒内连按两次退出                                |
| `Ctrl+C`           | 2 秒内连按两次退出                                |
| `?`                | 切换内置帮助/键位速查浮层                         |
| `Esc`              | （agent 运行中）取消当前轮次                      |

### Normal 模式 — 滚动

| 按键               | 动作                                |
| ------------------ | ----------------------------------- |
| `↓` / `↑`          | 行滚动                              |
| `Ctrl+F`           | 整页向下                            |
| `Ctrl+B`           | 整页向上                            |
| `G`                | 跳到最底                            |
| `gg`               | 跳到最顶（双键序列）                |

### Normal 模式 — 消息卡片

| 按键                          | 动作                                                                                |
| ----------------------------- | ----------------------------------------------------------------------------------- |
| `j` / `}`                     | 跳到下一条消息卡片                                                                  |
| `k` / `{`                     | 跳到上一条消息卡片                                                                  |
| `o` / `Enter` / `Space`       | 折叠/展开当前卡片；图片卡片下用此键打开图片                                          |
| `e`                           | 编辑/分叉当前用户消息为新一轮对话                                                   |

### Normal 模式 — 浮层

| 按键      | 动作                                                |
| --------- | --------------------------------------------------- |
| `Ctrl+T`  | 打开消息目录（跳转到指定卡片）                      |
| `$`       | 打开 Usage 统计浮层                                 |

### Normal 模式 — 搜索

| 按键     | 动作                                  |
| -------- | ------------------------------------- |
| `/`      | 开始搜索                              |
| `n`      | 跳到下一个匹配                        |
| `N`      | 跳到上一个匹配                        |

### 两种模式都有效 — Agent / 模型 / 集成

| 按键          | 动作                                                                                                            |
| ------------- | --------------------------------------------------------------------------------------------------------------- |
| `Tab`         | 循环切换主 agent 的模式（role，显示在状态栏；仅在 main 视图生效）                                             |
| `Shift+Tab`   | 循环切换当前查看的 agent 视图（主 agent 与所有活跃 SubAgent）                                                |
| `Ctrl+P`      | 在 Insert 和 Normal 两种模式下都打开模型池选择器                                                   |
| `Ctrl+R`      | 切换所有 agent 的 fast responses                                                                                |
| `Ctrl+O`      | 打开 MCP server 选择器；agent 运行中只读                                                                    |
| `Ctrl+G`      | 导出 diagnostics 包                                                                                             |

### 关于 `Ctrl+O` 与 MCP

`Ctrl+O` 在 Insert 和 Normal 模式下都会打开 MCP server 选择器。Agent 运行中也可以打开它查看 server 状态，但面板会保持只读，直到 agent 回到 idle。只有配置了 `manual: true` 的 server 才能切换启用/禁用；自动启动的 server 在选择器里始终只读。

## 自定义键位

可在 `config.yaml` 中覆盖任意键位：

```yaml
keymap:
  next_block: ["j"]            # 去掉 } 作为下一条卡片的备用键
  prev_block: ["k"]            # 去掉 { 作为上一条卡片的备用键
  scroll_down: ["down"]        # 仅用方向键做行滚动
  scroll_up: ["up"]
  quit: ["Q"]                  # 退出要求大写 Q（防误触）
  switch_model: ["ctrl+t"]     # 如果你更喜欢，也可以改成别的键
```

### 终端兼容性注意事项

自定义键位只有在终端模拟器、操作系统，以及 tmux 等中间层把该按键序列转发给 Chord 时才会生效。优先选择 Normal 模式下的普通可打印键，或没有强终端语义的简单 `ctrl+字母` 组合。

除非你已经在自己的终端环境里验证过，否则不建议把这些组合设为默认/自定义快捷键：

- macOS 上的 `alt+字母` / Option 组合：Ghostty 等终端可能把 Option 用于字符输入、菜单快捷键或应用级 keybind，例如 `alt+f` 可能根本不会传给 Chord。
- `ctrl+i`、`ctrl+m`、`ctrl+[`：传统终端会分别把它们编码成和 `Tab`、`Enter`、`Esc` 相同的输入。
- `ctrl+s` 和 `ctrl+q`：可能被软件流控截获。
- `ctrl+c`、`ctrl+z`、`ctrl+\\`：在终端里有中断/挂起等信号语义。
- 功能键或 `ctrl+shift+...` 组合：在不同终端、键盘布局、SSH 和 tmux 中支持不一致。

如果某个自定义键位不生效，先按 `?` 确认 Chord 已加载该映射，再用 `showkey`、`cat` 或终端自带的 key-event inspector 检查该按键是否真的传到了终端应用。

action 名是 [`internal/tui/keymap.go` 中 `KeyMap` 字段](https://github.com/keakon/chord/blob/main/internal/tui/keymap.go)的 lower snake_case 形式。键名沿用 Bubble Tea `tea.KeyMsg.String()` 的写法，如 `"esc"`、`"enter"`、`"shift+enter"`、`"ctrl+p"`、`"ctrl+shift+left"`、`"j"`、`"down"`、`"space"`、`" "`。

### Action 名速查

这里的 action 名是 `config.yaml` 里（`keymap:`）用的名称。

| Action                     | 默认值                            |
| -------------------------- | --------------------------------- |
| `insert_escape`            | `["esc"]`                         |
| `insert_submit`            | `["enter"]`                       |
| `insert_newline`           | `["shift+enter", "ctrl+j"]`       |
| `insert_history_up`        | `["up"]`                           |
| `insert_history_down`      | `["down", "ctrl+n"]`              |
| `insert_attach_clipboard`  | `["ctrl+v"]`（`Cmd+V` 在支持的终端里也会按同样的智能粘贴逻辑处理） |
| `insert_attach_file`       | `[]`                               |
| `insert_clear_input`       | `["ctrl+u"]`                      |
| `enter_insert`             | `["i"]`                           |
| `quit`                     | `["q"]`                           |
| `help_toggle`              | `["?"]`                           |
| `scroll_down`              | `["down"]`                        |
| `scroll_up`                | `["up"]`                          |
| `full_page_down`           | `["ctrl+f"]`                      |
| `full_page_up`             | `["ctrl+b"]`                      |
| `scroll_to_bottom`         | `["G"]`                           |
| `scroll_to_top_seq`        | `["g"]`（`gg` 序列的首键）        |
| `next_block`               | `["j", "}"]`                      |
| `prev_block`               | `["k", "{"]`                      |
| `toggle_collapse`          | `["o", "enter", " ", "space"]`    |
| `fork_session`             | `["e"]`                           |
| `directory`                | `["ctrl+t"]`                      |
| `usage_stats`              | `["$"]`                           |
| `search_start`             | `["/"]`                           |
| `search_next`              | `["n"]`                           |
| `search_prev`              | `["N"]`                           |
| `switch_agent`             | `["shift+tab"]`                   |
| `switch_role`              | `["tab"]`                         |
| `switch_model`             | `["ctrl+p"]`                      |
| `fast_mode`                | `["ctrl+r"]`                      |
| `mcp`                      | `["ctrl+o"]`                      |
| `diagnostics`              | `["ctrl+g"]`                      |

只有你列出的 action 会被覆盖，其余仍按上表默认值生效。

## 运行时查看当前键位

Normal 模式按 `?` 唤出内置 cheatsheet 浮层，里面显示的是当前实际生效的键位——修改 `keymap` 后尤其有用。

## 相关

- [使用指南](./usage_CN.md)：键位对应的工作流上下文
- [配置与认证](./configuration_CN.md)：完整 `config.yaml` schema
- [扩展与定制](./customization_CN.md)：agents、hooks、skills、MCP、LSP
