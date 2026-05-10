# 术语表

文档中反复出现的术语速查。

## MainAgent

会话内唯一的主 agent。它负责面向用户的对话，也是唯一能派出 SubAgent 的角色。当前主 role 显示在 TUI 状态栏，在 main 视图里用 `Tab` 循环切换。

## SubAgent

由 MainAgent（或允许层级深度内的其他 SubAgent）派出的下级 agent，专注于某个子任务。SubAgent 有自己的对话预算（context window，上下文窗口）、system prompt 和权限，完成后通过 `agent_done` 事件汇报摘要。`Shift+Tab` 在多个 SubAgent 视图间切换焦点。

## Pool（模型池）

命名、有序的 `provider/model`（或 `model@variant`）引用列表。每个 agent 引用一个或多个池。运行时默认用第一个池的第一个 ref，失败时按序 fallback。用 `/models`、`Ctrl+P` 或 `chord headless` 的 `models` 命令切换当前池。

## Variant

模型的命名参数预设——例如 `claude-opus-4.7@high` 选高 reasoning effort。Variant 在 `config.yaml` 的 `models.<name>.variants` 下定义，在模型池中以 `provider/model@variant` 形式引用。

## Compaction（上下文压缩）

将早期对话压缩为摘要的运行时过程，让长会话在接近模型上下文窗口上限时仍能继续。自动压缩表示 Chord 会在请求过大前自动触发；也可以手动执行 `/compact`。详见 [配置 — 上下文压缩](./configuration_CN.md#上下文压缩)。

## Context window（上下文窗口）

模型一次请求最多能处理的 token 数。对大多数模型，实用规则就是：“输入 + 请求输出”必须放进这个窗口。配置中对应 `limit.context`。

## 模型限制（`limit.*`）

每个模型允许的 token 数，告诉 Chord 各 provider 分别提供多少空间：

- `limit.context`：总请求窗口。
- `limit.input`：单独的输入上限，只有 provider 明确公布时才需要写。
- `limit.output`：模型的最大输出能力。

## 分离限制（split limits）

provider 文档里有时会用这个词表示“一个模型公布了不止一种限制”，通常是总上下文窗口外，另外还有单独的输入上限。一些 GPT 模型属于这种情况。如果 provider 文档同时列出这两个数字，就同时配置 `limit.context` 和 `limit.input`，这样 Chord 才能在输入过大前进行压缩。

## 请求输出上限（`max_output_tokens`）

Chord 每次请求时主动要求的最大输出量。它和模型的 `limit.output` 不是一回事。运行时会取适用限制中的最小值：`max_output_tokens`、模型的 `limit.output`，以及 `limit.context` 中剩余的空间。

## Oversize recovery（超限恢复）

provider 因请求过大而拒绝后，Chord 采用的恢复重试流程。Chord 会根据已配置的输入预算压缩或裁剪对话，并在可以安全重试时再次发送请求。

## Worktree

Chord 管理的 git worktree（位于 `<state-dir>/worktrees/<repo-id>/<slug>`），拥有独立的 project key、sessions、cache、exports。通过 `chord --worktree <name>` 创建；通过 `chord worktree list / remove / finish` 管理。适合在同一仓库上并行跑多个 Chord 任务而不互相干扰。详见 [目录与路径 — Worktree](./paths_CN.md#worktree)。

## Skill

一段可复用的"专长"模块，由 Markdown 正文和 YAML frontmatter 组成（`SKILL.md`），按需加载。模型在相关时调用 `Skill` 工具加载——Chord 不会把所有 skill 都预灌到每次 prompt。从 `.chord/skills/`、`.agents/skills/`、`~/.config/chord/skills/` 以及 `skills.paths` 配置的额外目录发现。详见 [扩展与定制 — Skills](./customization_CN.md#skills)。

## Hook

在 Chord 生命周期明确节点运行的外部命令（工具调用前、LLM 调用后、idle 时等）。Hook 从 stdin 拿到 JSON envelope，被注入 `CHORD_HOOK_*` 环境变量；按触发点类别可以 block / modify / observe。详见 [Hooks](./hooks_CN.md)。

## MCP（Model Context Protocol）

将外部工具/数据源暴露给 AI agent 的协议。Chord 中 MCP 服务器在 `config.yaml` 的 `mcp:` 下配置；每个服务器以 `mcp_<服务器>_<工具>` 前缀注册工具，可用 `allowed_tools` 只注册有用子集，避免无用 schema 发给模型。

## Headless

无 TUI 的 Chord。`chord headless` 提供 stdio JSONL 控制面，适合 bot / gateway / 自动化集成。[chord-gateway](https://github.com/keakon/chord-gateway) 将它包装为聊天平台接入。详见 [Headless](./headless_CN.md)。

## 推测执行（Speculative execution / early tool execution）

Chord 在模型响应仍在流式传输时（工具参数刚完整），就提前执行一小批安全工具，缩短"最终确认等待"时间。推测性的文件改动会真实落盘，但若最终确认阶段丢弃了该调用则自动回滚。始终开启，不可配置。

## Project key

Chord 从项目根路径计算出的稳定、清洗后标识（如 `~/projects/chord` 的 key 为 `HOME-projects-chord`），用作 sessions、运行时缓存、exports、worktree 身份的命名空间。两个不同路径清洗后冲突时，Chord 追加 8 字符指纹消歧。详见 [目录与路径 — `<project-key>`](./paths_CN.md#project-key-是什么)。

## Permission action（权限决策）

权限规则对工具调用的判定结果，三选一：

- `allow` —— 自动执行
- `ask` —— 暂停，需要用户确认
- `deny` —— 直接拒绝

权限是 agent 级配置，与项目/全局配置合并。属于产品层面的风险控制，**不是** OS 级安全沙箱。详见 [权限与安全](./permissions-and-safety_CN.md)。

## Local TUI / Local mode

"Local TUI"和"local mode"均指默认 `chord` 启动方式：MainAgent 在进程内运行，驱动终端 UI。没有 IPC、没有 socket、没有独立服务。与之相对的是 `chord headless`：运行时不带 TUI，专供外层控制面调用。

## Diagnostics bundle（诊断包）

`Ctrl+G` 或 `/diagnostics` 导出的快照，含最近日志、运行时状态、TUI 调试信息。报 bug 时附上。详见 [常见问题排查 — 何时检查日志](./troubleshooting_CN.md#何时检查日志)。

## Insert / Normal 模式

Vim 风格的两种 TUI 模式。**Insert** 是输入态，用于打字；**Normal** 用于导航、搜索、折叠、滚动和元操作。`Esc` 离开 Insert；`i`（或任意未绑定的可见字符）回到 Insert。详见 [快捷键](./keybindings_CN.md)。

## 自定义 slash 命令

用户定义的 `/name [args]` 命令，输入框中输入后展开为固定文本（或 `$ARGUMENTS` 模板），作为用户消息发给模型。在 `config.yaml` 的 `commands:` 下定义，或放到 `commands/` 目录。详见 [扩展与定制 — 自定义 slash 命令](./customization_CN.md#自定义-slash-命令)。

## 相关

- [快速开始](./quickstart_CN.md)
- [使用指南](./usage_CN.md)
- [配置与认证](./configuration_CN.md)
