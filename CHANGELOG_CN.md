# 变更记录

本项目采用语义化版本风格发布。1.0 之前的版本可能包含不兼容变更。

## 未发布

- 修复带图用户消息通过 `ee` / fork 编辑后再次发送时，来自会话历史路径恢复的图片不会被重新读入并随消息发送的问题。
- 修复 TUI 工具卡片、本地 shell 输出、问题对话框和确认摘要中的 ANSI-rich 外部文本显示：现在会按字面展示转义/控制序列，而不会把它们再次当成终端样式执行；这也包括裸 carriage return / `\r` 进度刷新文本。这样在查看诊断 dump、shell 输出或其他包含 escape/control sequence 的文本时，不会再污染卡片渲染。
- 删除一批 1.0 前不应继续保留的兼容路径与死代码，覆盖 compaction、LLM 会话处理、仅供测试的 LSP/helper、tools 与 TUI 内部实现。此次清理移除了未使用的 `ResetResponsesSession` / 旧 responses-session reset 链路，删除了旧的同步 compaction fallback 路径，将仅测试使用的 helper 迁入 `_test.go`，抽取了 fallback summary 共享渲染，并完成了工具名向 `tools.NameXxx` 常量的统一收口；同时补齐了 plan execution 新会话路径上的 session identifier 同步。
- 修复长会话中的 TUI 转录区裁剪：较早的后台状态卡在 spill/hydrate 恢复后再接收晚到更新时，现在会先恢复并重算转录高度，避免底部若干行甚至最后几张卡片无法滚动到。
- 删除 SubAgent `Complete` 工具参数及 `CompletionEnvelope` 中已废弃的 `blockers_remaining` 字段；SubAgent 应使用 `remaining_limitations` 报告非阻塞遗留事项，真正的阻塞需走 Escalate 或 `blocked` mailbox 流程，而不是直接 `Complete`。
- 统一 SubAgent artifact 表示：mailbox 消息、durable task 记录、实例 meta 文件以及内存中的运行时状态现在统一通过 `ArtifactRef` / `[]ArtifactRef` 引用 artifact；删除并行的 `artifact_ids` / `artifact_rel_paths` / `artifact_type` 字段及配套的旧适配函数。
- 将 TUI 渲染、搜索、hooks、agent 执行路径和编辑追踪中残留的 `Read` / `Write` / `Edit` / `Delete` / `Grep` / `Glob` 字面量替换为集中维护的 `tools.NameXxx` 常量。
- 删除无调用方的 `skill.Loader.Scan()` 包装方法（现有调用方已使用 `ScanMeta` 加按需 `Load`）。
- 改进 MCP initialize 握手元数据：运行时管理的 MCP client 现在会发送 build-time 注入的真实 Chord 版本，不再使用陈旧的硬编码版本；同时保留默认的 `mcp.NewClient` / `NewPendingManager` / `NewManager` 便捷入口，并新增显式 `WithClientInfo` 变体，供需要自定义握手身份的调用方使用。
- 将 TUI 展开逻辑和 compaction 用到的本地工具 trait（`Read` / `Grep` / `Glob` / `WebFetch` 与文件修改类工具）集中到 `internal/tools/tool_traits.go`，减少散落的字符串分支。
- 删除历史保留的 `ProviderConfig.UpdatePolledRateLimitSnapshot` 测试兼容包装方法，统一改为显式调用 `UpdatePolledRateLimitSnapshotForCredentialIndex`。
- 新增结构化 SubAgent 完成交接信息，支持记录实际修改文件、已运行验证、剩余限制、已知风险、推荐后续事项和 artifact 引用。
- 修复 TUI 工具卡片：排队徽标与换行内容现在会保持一致的右侧留白。
- 新增会话范围内的 `SaveArtifact` / `ReadArtifact` 工具，用于 SubAgent 交接 artifact，并支持通过 mailbox、task registry、snapshot 和会话恢复持久化。
- 改进 SubAgent 协调快照：展示近期完成信息、artifact 引用、写入范围和疑似停滞原因。
- 修复转录区选择复制在包含 tab 展开渲染行时的列宽计算问题。
- 修复 TUI 转录区鼠标拖选复制：用 `Cmd+C` 复制时，拖选文本会保留最后一个字符；同时补充了转录区复制行为说明。
- 改进 loop 验证续跑：`verify` assessment 现在会注入专用 `LOOP VERIFY` notice，并明确提示运行相关验证；同时文档补充 `/loop on [target]` 用法。
- 修复 LSP 侧边栏诊断：编辑后 self-review 若已清零，会持久化 `0E/0W` snapshot，避免语法错误修复后仍显示旧错误。
- 修复 TUI 卡片在 emoji、variation selector 和 ZWJ 组合字符附近出现背景色异常的问题：wrap、padding、truncate 现在与 viewport 的 grapheme-width 计算保持一致。
- 改进 TUI 工具调用中的本地路径显示：`Read`、`Write`、`Edit`、`Delete`、`Grep`、`Glob`，以及 Bash 中当前已可见的路径元信息，会在可能时优先显示相对于当前活动项目根目录的路径；恢复会话、启动时恢复和 spill/hydrate 恢复后也保持同样逻辑，项目根之外的路径仍显示绝对路径。
- 改进 AGENTS.md 处理：仅在检测到仓库指令存在时，才在 stable system prompt 中加入一小段 framing；AGENTS.md 正文仍保留在 session `<system-reminder>` 上下文层。
- 修复 sticky fallback 模型的 variant 状态：已 pin 的 fallback 请求会保留自身 `@variant`，且不会把主模型的 variant 泄漏到无 variant 的 fallback 运行中。
- 修复分类后的循环阻塞消息会渲染成未命名状态卡的问题。
- 修复 Ghostty/tab 恢复焦点后的界面残影：现在会跟踪终端处于后台期间发生的转录区/布局变化，并在 focus-settle 后检测到这些后台变化时强制触发 host redraw；同时在 diagnostics 中记录 background-dirty 与输入分隔线位置，便于继续排查残留的 stale-display 场景。
- 改进排队中的工具调用徽标：保持右侧留白，并在工具标题宽度不足时隐藏。
- 改进 assistant/thinking 流、压缩摘要和状态卡的 TUI Markdown 渲染缓存。
- 修复类似 Markdown 的工具输出在折叠状态下隐藏行数计算不准确的问题。
- 修复 headless idle 事件：Chord 现在只发送一个 `idle` envelope，不再额外发送重复的 ready `notification` envelope；gateway 应自行把 idle 状态渲染给用户。

## 0.1.0 - 2026-04-29

- Chord 首次公开发布。
- 提供本地优先的终端编码 Agent，包含 Vim 风格导航、会话管理、模型/服务商配置、工具执行、LSP 集成、图片输入和 headless 远程控制能力。
- 增加 macOS、Linux 和 Windows 的跨平台发布构建。
