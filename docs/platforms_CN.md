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
| `desktop_notification`（OSC 9 通知）                     | ⚠️[^osc] | ⚠️[^osc] | ⚠️[^osc]      | ⚠️[^osc]        |
| 剪贴板图片粘贴（`Ctrl+V` / `Cmd+V`）                     | ✅    | ⚠️[^clip]    | ⚠️[^clip]       | ⚠️[^clip]        |
| 终端图片渲染（iTerm2 / Kitty / Sixel）                   | ⚠️[^img]| ⚠️[^img]  | ⚠️[^img]       | ⚠️[^img]         |
| LSP — gopls / typescript / rust-analyzer                 | ✅[^lsp]| ✅[^lsp]   | ✅[^lsp]       | ✅[^lsp]         |
| LSP — Pyright + 项目 venv 自动探测                       | ✅[^py-unix] | ✅[^py-unix] | ✅[^py-win] | ✅[^py-unix]（仅 WSL Linux venv，详见下文） |
| MCP servers（stdio / HTTP）                              | ✅    | ✅             | ✅               | ✅                |
| 电源感知 idle 处理                                       | ✅    | ❌（no-op）    | ❌（no-op）      | ❌（no-op）       |

[^im]: 需要 `PATH` 中存在 `im-select` 二进制（Windows 用 `im-select.exe`）。Chord 只做集成，不附带二进制。
[^im-linux]: `im-select` 始于 macOS；Linux 上可用兼容 build 或自己写一个同 CLI 的 wrapper 脚本。
[^im-win]: 用 `im-select.exe`（如 <https://github.com/daipeihust/im-select#-windows>）。
[^im-wsl]: WSL 内通常希望切换 Windows 端的 IM；一般通过 interop 调 `im-select.exe`，可能需要配 PATH 或 wrapper。
[^osc]: OSC 9 是终端的能力而非 OS 的能力。iTerm2 等多数现代终端支持；旧 `xterm`、部分裸 tty、某些 `tmux` 配置下转义会被静默丢弃。详见下文 [终端兼容](#终端兼容)。
[^clip]: 剪贴板图片粘贴依赖终端是否转发图片字节；并非所有终端都支持。iTerm2、新版 WezTerm/Ghostty 可以；某些 tmux/Linux 设置只能拿到路径或什么都没有。
[^img]: 图片渲染依赖终端的图片协议（iTerm2 inline images、Kitty graphics、Sixel 等）。终端均不支持时，图片附件仍可发给模型，但 TUI 内无预览。
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

### `desktop_notification`（OSC 9）

启用后，Chord 在权限确认、问题待回答、agent 回到 idle 等事件时发出 OSC 9 转义序列。Chord 不负责通知守护进程；具体显示依赖终端（iTerm2、WezTerm、Ghostty、kitty 等都支持）。

`tmux` 中可能需要 `set -g allow-passthrough on`，OSC 9 才能透传到宿主终端。

### 剪贴板图片粘贴

`Ctrl+V`（macOS 下 `Cmd+V`）优先粘贴剪贴板中的图片而非文本。能否真正粘到取决于终端：

- **macOS + iTerm2 / WezTerm / Ghostty**：可以。
- **Linux Wayland**：取决于终端实现；有些只送 `text/uri-list`。
- **Linux X11**：取决于终端实现。
- **Windows Terminal**：多数情况可以。
- **tmux 内部**：图片字节通常透不过来。

剪贴板图片粘贴不可用时，仍可用 `Ctrl+F` 将输入框中已有的图片路径附加到消息。

### 终端图片渲染

Chord 自动检测 iTerm2 inline-images、Kitty graphics、Sixel。三者都不支持时，图片附件仍会发给模型，只是 TUI 内不预览。

### Pyright venv 自动探测

未在配置中手动指定 Python 解释器时，Chord 在 LSP root 下按以下顺序找项目 venv：

- 类 Unix（macOS、Linux、WSL）：`.venv/bin/python` → `venv/bin/python` → `env/bin/python`
- Windows：`.venv\Scripts\python.exe` → `venv\Scripts\python.exe` → `env\Scripts\python.exe`

WSL 自动探测不会选 `Scripts\python.exe` 里的 Windows venv。WSL 内开发请在 WSL 里建 Linux venv，或在 `lsp.pyright.options` 中显式设置 `python.pythonPath`。

详见 [扩展与定制 — LSP](./customization_CN.md#lsp)。

## 终端兼容

很多「macOS 上能用、Linux 不行」的反馈实际是终端模拟器差异，而非 OS 差异。推荐以下终端以获得最佳体验：

- **iTerm2**（macOS）：图片预览、OSC 9、剪贴板图片粘贴
- **Ghostty**（跨平台）：图片预览、OSC 9
- **WezTerm**（跨平台）：图片预览、OSC 9、剪贴板图片粘贴
- **kitty**（Linux/macOS）：Kitty graphics、OSC 9
- **Windows Terminal**：作为通用 TUI 没问题；图片协议有限

`tmux`、`screen` 在 Chord 与终端之间又多一层；部分功能（OSC 9、某些图片流程）需要显式配置 pass-through。

## Windows 用户须知

Chord 能在 Windows 上跑，但 Windows 不是主要平台。具体：

- TUI 在现代终端（Windows Terminal、WezTerm）下工作正常。
- `prevent_sleep` 是 no-op——请用 Windows 电源设置。
- `ime_switch_target` 需要 `im-select.exe`。
- 工具调用中的文件路径走 Windows 风格，反斜杠原样保留。
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
