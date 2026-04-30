# 使用指南

本文档介绍 Chord 的主要运行模式、核心交互方式，以及你在日常使用中最常用到的功能。

## 运行模式

Chord 有两条主要使用路径：

- **本地 TUI**：默认模式，直接在当前进程内运行 MainAgent
- **Headless 控制面**：通过 `chord headless` 使用 stdio JSONL 与外部 gateway / bot 集成

大多数个人开发场景推荐直接使用本地 TUI。

## TUI 基本交互

启动后输入框默认聚焦，可以直接输入消息并按 `Enter` 发送。

工具调用卡片会尽量保持路径简洁：对于 `Read`、`Write`、`Edit` 这类文件工具，如果路径位于当前工作目录内，TUI 会优先显示相对路径；如果路径位于当前目录之外，则继续显示绝对路径。

常用操作：

- `Esc`：切换到 Normal 模式；在 main 视图运行中再次按 `Esc` 可取消当前 turn
- `i`：回到输入模式
- `j` / `k`：在消息卡片之间移动
- `gg` / `G`：跳到开头 / 结尾
- `/`：搜索消息
- `Ctrl+J`：打开消息目录
- `Ctrl+P`：切换模型
- `Ctrl+G`：导出诊断包
- `q`：双击退出
- `Ctrl+C`：双击退出

## 会话

Chord 会为当前项目维护持久化会话。

常见方式：

- `chord`：新建会话
- `chord --continue`：恢复当前项目最近的非空会话
- `chord --resume <session-id>`：恢复指定会话
- `/new`：在 TUI 内创建新会话
- `/resume`：在 TUI 内选择历史会话

退出时，如果当前会话可恢复，Chord 会打印对应的恢复命令。

## 常用本地控制命令

以下命令由本地运行时处理，不会原样发送给模型：

- `/new`：新建会话
- `/resume`：恢复会话
- `/model`：切换当前运行模型
- `/export`：导出当前会话
- `/compact`：手动触发上下文压缩
- `/stats`：查看用量统计
- `/diagnostics`：导出用于排障的诊断包
- `/loop on [target]` / `/loop off`：开启或关闭持续执行模式

## 多 Agent 与焦点切换

Chord 支持 MainAgent 与 SubAgent 协作。

- `Tab`：切换 main agent 角色
- `Shift+Tab`：在 main agent 与各 sub agent 之间切换焦点

在 SubAgent 视图中，你可以查看该 agent 的上下文与输出；已结束的 SubAgent 视图是只读的。

## 图片输入与查看

当前支持：

- 从剪贴板粘贴图片
- 把图片文件作为附件发送给当前聚焦的 Agent
- 在支持的终端里直接查看图片

常用操作：

- `Ctrl+V` / `Cmd+V`：优先读取剪贴板图片，否则粘贴文本
- `Ctrl+F`：把输入框中的图片路径加入当前消息附件
- `Enter` / `o` / `Space`：在 Normal 模式下打开当前消息中的图片

## 复制文本

- 可在转录区内用鼠标拖选 TUI 里的文本
- `Cmd+C`：在会把这个按键转发给 Chord 的 macOS 终端中，复制当前转录区选中的文本
- `Ctrl+C`：仍用于取消 / 退出，不用于复制转录区文本

## Headless 模式

`chord headless` 适合以下场景：

- bot / gateway 集成
- 自动化脚本驱动
- 无需本地 TUI 的外部控制面接入

它使用：

- stdin：一行一个 JSON 命令
- stdout：一行一个 JSON 事件

更详细的说明见 [Headless 集成](./headless_CN.md)。

## 日常使用建议

- 首次接入时，先用最小 provider 配置确认请求能跑通
- 需要更强代码感知时，再配置 LSP
- 需要外部工具接入时，再添加 MCP 或 Hooks
- 对高风险工具保持 `ask`，不要直接全局 `allow`

## 相关文档

- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
