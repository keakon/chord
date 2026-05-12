# 平台支持

Chord 主要在 macOS 上开发和测试。其他平台不同程度可用：大部分功能与平台无关，少数依赖 OS 特定能力，在其他平台会降级或 no-op。本页给出每个功能的真实支持情况、降级行为及各平台的额外安装要求。

## 速览矩阵

| 功能                                                     | macOS | Linux         | Windows         | WSL              |
| -------------------------------------------------------- | ----- | ------------- | --------------- | ---------------- |
| 核心 TUI（模式切换、消息、会话）                         | ✅    | ✅             | ⚠️（尽力而为） | ✅                |
| `chord headless` JSON 控制面                             | ✅    | ✅             | ✅               | ✅                |
| Worktree（`chord --worktree`、`chord worktree …`）       | ✅    | ✅             | ✅               | ✅                |
| `prevent_sleep`（阻止系统休眠）                          | ✅    | ❌（no-op）    | ❌（no-op）      | ❌（no-op）       |
| `ime_switch_target`（模式切换时自动切输入法）            | ✅[^im]| ⚠️[^im-linux]| ✅[^im-win]    | ⚠️[^im-wsl]      |
| `desktop_notification`（终端通知）                         | ⚠️[^osc] | ⚠️[^osc] | ⚠️[^osc]      | ⚠️[^osc]        |
| 剪贴板图片粘贴（`Ctrl+V` / `Cmd+V`）                     | ✅    | ⚠️[^clip]    | ⚠️[^clip]       | ⚠️[^clip]        |
| 终端图片渲染（Kitty / iTerm2）                           | ⚠️[^img]| ⚠️[^img]  | ⚠️[^img]       | ⚠️[^img]         |
| LSP — gopls / typescript / rust-analyzer                 | ✅[^lsp]| ✅[^lsp]   | ✅[^lsp]       | ✅[^lsp]         |
| LSP — Pyright + 项目 venv 自动探测                       | ✅[^py-unix] | ✅[^py-unix] | ✅[^py-win] | ✅[^py-unix]（仅 WSL Linux venv，详见下文） |
| MCP servers（stdio / HTTP）                              | ✅    | ✅             | ✅               | ✅                |
| 电源感知 idle 处理                                       | ✅    | ❌（no-op）    | ❌（no-op）      | ❌（no-op）       |

[^im]: 需要 `PATH` 中存在 `im-select` 二进制（Windows 用 `im-select.exe`）。Chord 只做集成，不附带二进制。
[^im-linux]: `im-select` 始于 macOS；Linux 上可用兼容 build 或自己写一个同 CLI 的 wrapper 脚本。
[^im-win]: 用 `im-select.exe`（如 <https://github.com/daipeihust/im-select#-windows>）。
[^im-wsl]: WSL 内通常希望切换 Windows 端的 IM；一般通过 interop 调 `im-select.exe`，可能需要配 PATH 或 wrapper。
[^osc]: 通知能力取决于终端而非 OS。Chord 会按终端自动选择通知转义序列（OSC 9 或 OSC 777）；不支持的终端通常会忽略该序列。详见下文 [终端兼容](#终端兼容)。
[^clip]: 剪贴板图片粘贴取决于终端是转发图片字节、发空 paste 事件后要求应用自己读系统剪贴板，还是只粘贴普通文本。Chord 仅在拿到真实图片数据时附图；文本仍按文本处理。
[^img]: 图片渲染当前自动检测 Kitty graphics 与 iTerm2 inline images（Ghostty 走 Kitty，WezTerm 走 iTerm2）。三者都不支持时，图片附件仍可发给模型，但 TUI 内无预览。`tmux` / `zellij` 内默认保守禁用。
[^lsp]: 需要本地装好对应 language server（如 `gopls`、`typescript-language-server`、`rust-analyzer`）。Chord 不打包它们。
[^py-unix]: 类 Unix 下 Chord 在 LSP root 下依次探测 `.venv/bin/python`、`venv/bin/python`、`env/bin/python`。
[^py-win]: Windows 下 Chord 探测 `.venv\Scripts\python.exe`、`venv\Scripts\python.exe`、`env\Scripts\python.exe`。

图例：✅ 支持 · ⚠️ 有注意事项 · ❌ 不支持 / no-op。

## 各功能详情

### `prevent_sleep`

macOS 下用 `caffeinate(1)` 实现。Linux / Windows / WSL 上是 no-op，实现为 `internal/power/power_other.go` 的 `NoopBackend`。其他平台需常驻不休眠的话，直接用 OS 自带的电源设置。

### `ime_switch_target`

从 Insert 切到 Normal 时，Chord 可调用 `im-select`（或 `im-select.exe`）切到指定 IM（通常是英文键盘布局），切回 Insert 时恢复原来的 IM。

```yaml
# ~/.config/chord/config.yaml
ime_switch_target: com.apple.keylayout.ABC          # macOS 示例
# ime_switch_target: 1033                           # Windows 示例（locale id）
```

`im-select` 需单独安装。变量值就是字符串，Chord 原样传给 `im-select`，具体格式由该平台工具决定。

### `desktop_notification`（终端通知）

启用后，Chord 会在权限确认、问题待回答、agent 回到 idle 等事件时发出终端通知转义序列。Chord 不负责通知守护进程；具体显示依赖终端。

当前实现会按终端自动选择协议；不支持的终端通常会忽略该序列。

实践中：

- **Ghostty / WezTerm / Windows Terminal**：Chord 会尝试使用 **OSC 777**
- **iTerm2**：Chord 使用 **OSC 9**
- **其他终端**：Chord 保守回退到 **OSC 9**

`tmux` 中可能需要 `set -g allow-passthrough on`，通知序列才可能透传到宿主终端。

### 剪贴板图片粘贴

`Ctrl+V`（macOS 下 `Cmd+V`）是智能粘贴：优先尝试图片，再回退到文本。不同终端把“剪贴板里的图片”交给应用的方式并不一致：有的直接转发图片字节，有的发空 paste 事件后要求应用自己读系统剪贴板，还有的只能粘贴普通文本。

Chord 目前对这些差异采用保守处理：

- **能直接读到图片字节**：作为图片附件添加
- **其余情况**：按普通文本粘贴

常见情况：

- **macOS + iTerm2 / WezTerm / Ghostty**：通常可以直接粘图
- **Linux Wayland / X11**：取决于终端实现；有些终端对剪贴板图片只能粘普通文本
- **Windows Terminal**：核心文本粘贴没问题；图片更依赖终端/系统链路
- **tmux 内部**：图片字节通常透不过来

剪贴板图片粘贴不可用时，你仍可给 `insert_attach_file` 绑定快捷键，再通过输入框里的图片路径附加图片。

### 终端图片渲染

Chord 当前自动检测并启用：

- **Kitty graphics**（kitty、Ghostty）
- **iTerm2 inline images**（iTerm2、WezTerm）

如果终端不支持这些协议，图片附件仍会发给模型，只是 TUI 内不预览。

说明：

- **Sixel 目前未实现为 Chord 后端**
- **`tmux` / `zellij` 内默认保守禁用图片预览**，避免常见 passthrough / 占位符兼容问题
- 高级用户可用环境变量覆盖自动检测：`CHORD_IMAGE_BACKEND=kitty|iterm2|none`，以及 `CHORD_IMAGE_INLINE=0|1`、`CHORD_IMAGE_FULLSCREEN=0|1`

### Pyright venv 自动探测

未在配置中手动指定 Python 解释器时，Chord 在 LSP root 下按以下顺序找项目 venv：

- 类 Unix（macOS、Linux、WSL）：`.venv/bin/python` → `venv/bin/python` → `env/bin/python`
- Windows：`.venv\Scripts\python.exe` → `venv\Scripts\python.exe` → `env\Scripts\python.exe`

WSL 自动探测不会选 `Scripts\python.exe` 里的 Windows venv。WSL 内开发请在 WSL 里建 Linux venv，或在 `lsp.pyright.options` 中显式设置 `python.pythonPath`。

详见 [扩展与定制 — LSP](./customization_CN.md#lsp)。

## 终端兼容

很多「macOS 上能用、Linux 不行」的反馈实际是终端模拟器差异，而非 OS 差异。推荐以下终端以获得最佳体验：

- **iTerm2**（macOS）：图片预览、终端通知、剪贴板图片粘贴
- **Ghostty**（跨平台）：图片预览、终端通知（会尝试 OSC 777）
- **WezTerm**（跨平台）：图片预览、终端通知（会尝试 OSC 777）、剪贴板图片粘贴
- **kitty**（Linux/macOS）：图片预览、终端通知
- **Windows Terminal**：作为通用 TUI 没问题；图片协议和通知依赖版本/宿主链路

`tmux`、`screen` 在 Chord 与终端之间又多一层；部分功能（终端通知、某些图片流程）需要显式配置 pass-through，而且 Chord 当前默认会在 `tmux` / `zellij` 内禁用图片预览。
## Windows 用户须知

Chord 能在 Windows 上跑，但 Windows 不是主要平台。具体：

- TUI 在现代终端（Windows Terminal、WezTerm）下工作正常。
- `prevent_sleep` 是 no-op——请用 Windows 电源设置。
- `ime_switch_target` 需要 `im-select.exe`。
- 工具调用中的文件路径走 Windows 风格，反斜杠原样保留。
- `Shell` 和 `Spawn` 在 Windows 上也仍是非交互的，但超时 / 取消清理依赖直接终止进程，而不是 Unix 风格的 session / 进程组控制；因此对后代进程的清理可能不如 Unix 完整。
- 遇到 Windows 特有 bug 的话，更可能是「还没人踩到」而非「故意不支持」。请用 `Ctrl+G` 或 `/diagnostics` 导出诊断包再上报。

## WSL 用户须知

WSL 大致表现得像 Linux：

- Chord 作为 Linux 二进制在 WSL 内运行；sessions、配置用 Linux 路径（`~/.config/chord/` 等）。
- `prevent_sleep` 是 no-op；请用宿主 Windows 的电源设置。
- `ime_switch_target` 通常通过 Windows interop 调 `im-select.exe`。
- Pyright venv 自动探测在 WSL 内使用 **Linux** venv（`.venv/bin/python` 等）；Windows 风格 `Scripts\python.exe` venv 不会被选。
- 终端能力取决于宿主 Windows 上跑的终端（Windows Terminal、WezTerm、Ghostty 等）。

## 报告平台相关问题

报 bug 怀疑与平台相关时请附上：

- OS 与版本
- 终端模拟器与版本
- 是否在 `tmux` / `screen` / WSL 内
- 一份诊断包（`Ctrl+G` 或 `/diagnostics`）

日志位置和包结构见 [常见问题排查 — 何时检查日志](./troubleshooting_CN.md#何时检查日志)。

## 相关

- [配置与认证](./configuration_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
