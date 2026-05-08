# 术语表

Chord 文档里反复出现的术语速查。

## MainAgent

会话内唯一的主 agent。它持有面向用户的对话，也是唯一能派出 SubAgent 的角色。当前主 role 显示在 TUI 状态栏，在 main 视图里可用 `Tab` 循环切换。

## SubAgent

由 MainAgent（在允许的层级深度下，也可以由其他 SubAgent）派出的下级 agent，专注于某个子任务。SubAgent 有自己的 context window、system prompt 和权限，完成后通过 `agent_done` 事件汇报 summary。`Shift+Tab` 可在多个 SubAgent 视图间切换 focus。

## Pool（模型池）

命名、有序的 `provider/model`（或 `model@variant`）引用列表。每个 agent 引用一个或多个 pool。运行时默认用第一个 pool 的第一个 ref，失败时按顺序 fallback。可用 `/models`、`Ctrl+P` 或 `chord headless` 的 `models` 命令切换当前 pool。

## Variant

某个模型的命名参数预设——例如 `claude-opus-4.7@high` 选高 reasoning effort。Variant 在 `config.yaml` 的 `models.<name>.variants` 下定义，在模型池里以 `provider/model@variant` 形式引用。

## Compaction（上下文压缩）

把早期对话总结成紧凑摘要的运行时过程，使长会话能继续而不超出模型 context window。当上下文接近阈值时自动触发，也可手动 `/compact`。详见 [配置 — 上下文压缩](./configuration_CN.md#上下文压缩)。

## Worktree

chord 管理的 git worktree（位于 `<state-dir>/worktrees/<repo-id>/<slug>`），有自己的 project key、sessions、cache、exports。通过 `chord --worktree <name>` 创建；通过 `chord worktree list / remove / finish` 管理。适合在同一仓库上同时跑多个 chord 任务而不互相干扰。详见 [目录与路径 — Worktree](./paths_CN.md#worktree)。

## Skill

一段可复用、按需加载的"专长"，由 markdown 正文加 YAML frontmatter 组成（`SKILL.md`）。模型在相关时通过 `Skill` 工具按需加载——Chord 不会把所有 skill 都预灌进每个 prompt。从 `.chord/skills/`、`.agents/skills/`、`~/.config/chord/skills/` 以及 `skills.paths` 配置的额外目录发现。详见 [扩展与定制 — Skills](./customization_CN.md#skills)。

## Hook

在 Chord 生命周期某个明确节点运行的外部命令（工具调用前、LLM 调用后、idle 时等）。Hook 从 stdin 拿到 JSON envelope，会被注入 `CHORD_HOOK_*` 环境变量；按触发点类别可以 block / modify / observe。详见 [Hooks](./hooks_CN.md)。

## MCP（Model Context Protocol）

一种把外部工具/数据源暴露给 AI agent 的协议。Chord 中 MCP 服务器在 `config.yaml` 的 `mcp:` 下配置；每个服务器以 `mcp_<服务器>_<工具>` 前缀注册其工具。用 `allowed_tools` 只注册有用子集，避免无用 schema 被发给模型。

## Headless

无 TUI 的 Chord。`chord headless` 提供 stdio JSONL 控制面，适合 bot / gateway / 自动化集成。配套项目 [chord-gateway](https://github.com/keakon/chord-gateway) 把它打包成聊天平台接入。详见 [Headless](./headless_CN.md)。

## Speculative execution（推测执行 / early tool execution）

Chord 在模型响应**仍在流式传输**时（一旦工具参数完整）就执行一小批安全工具，缩短"finalize gap"。推测性的文件改动是真实落盘的，但若 finalize 丢弃了那次调用，会被回滚。始终开启，不可配置。详见 [配置 — 流式工具执行](./configuration_CN.md#流式工具执行early-execution)。

## Project key

Chord 从项目 canonical 文件系统根计算出的稳定、清洗后的标识（如 `~/projects/chord` 的 key 是 `HOME-projects-chord`）。作为 sessions、运行时缓存、exports、worktree 身份的命名空间。若两个不同路径清洗后冲突，Chord 追加 8 字符指纹消歧。详见 [目录与路径 — `<project-key>`](./paths_CN.md#project-key-是什么)。

## Permission action（权限决策）

权限规则对一次工具调用的判定结果，三种之一：

- `allow` —— 自动执行
- `ask` —— 暂停，需要用户确认
- `deny` —— 直接拒绝

权限是 agent 级配置，与项目/全局配置合并。它是产品级风险控制，**不是** OS 级沙箱。详见 [权限与安全](./permissions-and-safety_CN.md)。

## Local TUI / Local mode

"Local TUI"和"local mode"都指默认的 `chord` 启动：MainAgent 在进程内运行，驱动终端 UI。没有 IPC、没有 socket、没有独立服务。与之相对的是 `chord headless`：运行时不带 TUI，专供外层控制面调用。

## Diagnostics bundle（诊断包）

`Ctrl+G` 或 `/diagnostics` 导出的快照，含最近日志、运行时状态、TUI 调试信息。报 bug 时附上。详见 [常见问题排查 — 何时检查日志](./troubleshooting_CN.md#何时检查日志)。

## Insert / Normal 模式

Vim 风的两种 TUI 模式。**Insert** 是输入态，用于打字；**Normal** 用于导航、搜索、折叠、滚动和元操作。`Esc` 离开 Insert；`i`（或任意未绑定的可见字符）回到 Insert。详见 [快捷键](./keybindings_CN.md)。

## 自定义 slash 命令

用户定义的 `/name [args]` 命令，输入框中敲入后会展开成固定文本（或带 `$ARGUMENTS` 模板）作为用户消息发给模型。在 `config.yaml` 的 `commands:` 下定义，或放到 `commands/` 目录。详见 [扩展与定制 — 自定义 slash 命令](./customization_CN.md#自定义-slash-命令)。

## 相关

- [快速开始](./quickstart_CN.md)
- [使用指南](./usage_CN.md)
- [配置与认证](./configuration_CN.md)
