# 变更记录

本项目采用语义化版本风格发布。1.0 之前的版本可能包含不兼容变更。

## 未发布

- 新增模型池（model pool）支持：`AgentConfig.Models` 从扁平列表改为 `map[string][]string`（池名 → 有序模型引用列表）。所有 agent（包括 primary）必须定义至少一个池；池名由用户自定义，不做保留（如 "default"、"base"、"fast"、"strong" 均为合法池名）。未显式选择池时，Chord 会回退到该 agent 的 `model_pools: [...]` 列表中的**第一个**池。运行时 `RuntimeModelPoolPolicy` 管理当前主角色池、按 agent 覆盖以及 last-picked 追踪，并按项目持久化状态。`/model` 命令替换为 `/models`，支持子命令：`status`（显示当前池和可用池）、`<pool>`（设置当前视图对象的池）和 `--agent <name> <pool>`（为指定 agent 固定池）。TUI 弹窗改为池选择器（不再支持单模型选择）。Agent 现在可通过 `model_pools` 字段引用 `config.yaml` 中全局定义的模型池，实现跨 agent 复用；`models` 与 `model_pools` 互斥。不兼容变更：agent 旧的扁平列表模型配置格式不再接受；agent 必须通过 `model_pools` 引用一个或多个池，且池必须定义在 `config.yaml` 顶层的 `model_pools` 中。
- 在 diagnostics 与启动日志中加入构建身份信息。`chord --version`、diagnostics bundle 和 TUI dump 现在会包含或展示 commit、dirty 状态、注入的 build time、VCS time、Go 版本和可执行文件 mtime 等信息；MCP client info 继续使用精简的应用版本号。
- agent 忙碌时选择模型池会自动排队，等下一次用户消息发送时应用，与排队用户输入保持一致。pending switch 失败现在通过 TUI 的 Update 消息路径回流处理（不再由后台 goroutine 直接改动视图状态），避免 data race。
- 修复 SKILLS 侧边栏状态：`Skill` 工具加载失败不再被标记为已加载（绿色），未发现/不存在的 skill 不再显示，且移除旧的 "(loaded)" 后缀。

## 0.2.0 - 2026-05-05

- 重构 TUI 渲染缓存布局：`viewCacheState` 现在只包含可安全批量清零的 draw 循环缓存，动画、ticker、本地 shell 和 startup transcript 相关运行态移入独立 runtime state，并会在 `invalidateDrawCaches` 后保留。缓存失效逻辑仍可对缓存结构体整体清零（同时保留 `cachedMainSearchBlockIndex = -1` 这一不变量），不再逐字段写约 80 行的归零语句。删除从未被读取或赋值的 `cachedDirKey`、`cachedHelpKey`、`cachedStatusActivitiesKey`、`cachedStatusChordDisplay`、`cachedStatusSessionSwitchKey` 字段；将 5 个 `renderSlashCache*` 字段合并为子结构体 `slashRenderCache`（`m.slashCache`）；并把 `OverlayList` 与 `OverlayTable` 中重复出现的 `renderVersion / renderCacheWidth / renderCacheText / renderCacheValid` 四元组抽成共享的 `widthKeyedRenderCache` 嵌入字段。
- 拆分 `agent.AgentForTUI` 接口为按职责划分的子接口（`MessageSender`、`PromptResolver`、`ModelSelector`、`SessionController`、`SubAgentInspector`、`LoopController`、`RoleController`、`UsageReporter`、`KeyHealthReporter`、`CompactionController`、`PlanExecutor`），原 `AgentForTUI` 通过组合这些子接口得到。现有实现（`MainAgent`、headless adapter、TUI 测试 stub）和消费方都继续满足组合后的接口；新增 TUI 消费方应直接依赖更小的子接口，而不是再依赖整个 `AgentForTUI`。
- 重构 `MainAgent.Shutdown`：将原本约 170 行的单函数拆为 `cancelActiveWork`、`closeSubAgentMCPServers`、`buildShutdownSnapshot` 三个独立 helper，主函数压缩到约 92 行，各阶段的顺序与 budget 处理可独立审计。
- 移除未被使用的 `tools.TruncateOutput` 包装函数（包内调用者均已使用 `TruncateOutputWithOptions`）。仓库外的调用方需要切换到 `TruncateOutputWithOptions(output, sessionDir, tools.TruncateOptions{})` 以保持原默认行为。

- 改进 Pyright LSP 配置体验：未显式配置 Python 解释器时，Chord 会按当前平台的 virtualenv 布局自动发现项目本地的 `.venv`、`venv`、`env` 解释器（类 Unix/WSL 使用 `bin/python`，Windows 使用 `Scripts\python.exe`）；相对的 `python.pythonPath`、`python.defaultInterpreterPath`、`python.venvPath` 会按 LSP root 规范化为绝对路径；`workspace/configuration` 现在也会按 section 返回配置，确保 pyright-langserver 能正确读取 `python` 配置。不兼容变更：通过 `workspace/configuration` 提供的 LSP `options` 现在对所有 LSP server 都必须按 section 组织，而不仅是 Pyright；对于 Pyright，请使用 `python`、`python.analysis` 这类嵌套键，而不是旧的扁平顶层键。
- 移除已废弃的 headless `notification` envelope 类型：删除 `protocol.TypeNotification` 与 `protocol.NotificationPayload`，并从 headless 订阅白名单中移除 `"notification"`。已无任何代码路径会发出该 envelope，gateway 应基于 `idle` envelope 自行渲染 ready/idle 状态。
- 将运行时日志从原先的 `slog` 风格适配层全面迁移为直接使用 `golog`。日志现在是 golog 原生纯文本输出，并由 golog 直接记录调用位置；此前伪结构化的 `level=... msg=... key=value` 格式以及默认 logger 的 `With(...)` 上下文字段不再自动输出。
- 修复带图用户消息通过 `ee` / fork 编辑后再次发送时，来自会话历史路径恢复的图片不会被重新读入并随消息发送的问题。
- 修复 TUI 工具卡片渲染：工具参数/结果现在按终端安全的纯文本展示，ANSI/control sequence 会被转义；看起来像 Markdown 的普通工具输出不再自动按 Markdown 渲染；超大的折叠 Bash 结果不会再 wrap 隐藏尾部；折叠状态的 hidden-line 提示也不再重复计算第一条隐藏行。
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
- 修复 TUI 转录区在长会话里可能逐步漂移的问题：会导致最后一张卡片/内边距被裁剪，且鼠标拖选命中到错误的行。
- 修复 TUI 转录区鼠标拖选复制：用 `Cmd+C` 复制时，拖选文本会保留最后一个字符；同时补充了转录区复制行为说明。
- 改进 loop 验证续跑：`verify` assessment 现在会注入专用 `LOOP VERIFY` notice，并明确提示运行相关验证；同时文档补充 `/loop on [target]` 用法。
- 修复 LSP 侧边栏诊断：编辑后 self-review 若已清零，会持久化 `0E/0W` snapshot，避免语法错误修复后仍显示旧错误。
- 修复 TUI 卡片在 emoji、variation selector 和 ZWJ 组合字符附近出现背景色异常的问题：wrap、padding、truncate 现在与 viewport 的 grapheme-width 计算保持一致。
- 改进 TUI 工具调用中的本地路径显示：`Read`、`Write`、`Edit`、`Delete`、`Grep`、`Glob`，以及 Bash 中当前已可见的路径元信息，会在可能时优先显示相对于当前活动项目根目录的路径；恢复会话、启动时恢复和 spill/hydrate 恢复后也保持同样逻辑，项目根之外的路径仍显示绝对路径。
- 改进 AGENTS.md 处理：仅在检测到仓库指令存在时，才在 stable system prompt 中加入一小段 framing；AGENTS.md 正文仍保留在 session `<system-reminder>` 上下文层。
- 修复 sticky fallback 模型的 variant 状态：已 pin 的 fallback 请求会保留自身 `@variant`，且不会把主模型的 variant 泄漏到无 variant 的 fallback 运行中。
- 修复分类后的循环阻塞消息会渲染成未命名状态卡的问题。
- 修复 Ghostty/tab 恢复焦点后的界面残影：现在会跟踪终端处于后台期间发生的转录区/布局变化，并在 focus-settle 后检测到这些后台变化时强制触发 host redraw；同时在 diagnostics 中记录 background-dirty 与输入分隔线位置，便于继续排查残留的 stale-display 场景。
- 修复 Ghostty/cmux 在快速滚动/resize/布局变化后分隔线偶发显示为两条的残影问题：在这些终端下，Chord 现在会对每一行渲染结果追加“清到行尾”，避免空行遗留旧 cell。
- 改进排队中的工具调用徽标：保持右侧留白，并在工具标题宽度不足时隐藏。
- 改进 assistant/thinking 流、压缩摘要和状态卡的 TUI Markdown 渲染缓存。
- 修复类似 Markdown 的工具输出在折叠状态下隐藏行数计算不准确的问题。
- 修复 headless idle 事件：Chord 现在只发送一个 `idle` envelope，不再额外发送重复的 ready `notification` envelope；gateway 应自行把 idle 状态渲染给用户。

- 修复一组 compaction 后续行为问题：恢复/保留终端标题时，会更可靠地忽略被 compaction 摘要污染的首条消息；自动 compaction 在 continuation barrier apply 失败时不再重复发送 idle 转换；活动标题动画在恢复 spinner 驱动状态前也会始终重新同步 terminal-title ticker。
- 调整自动（usage / 阈值触发）compaction 的续期行为：durable summary 应用完成后，agent 现在会主动在压缩后的上下文上启动新的 LLM turn 继续推进任务，而不是回到 idle 等待用户再次输入。手动 `/compact` 仍然返回 idle。loop 模式会在自动续 turn 时同步推进 loop state。
- 改进 compaction 后会话列表预览/终端标题的准确性：不再通过文本内容推断 compaction 摘要，而是在 `usage-summary.json` 和 session summary 中持久化显式元数据（`*_is_compaction_summary` 标志）并据此决策。不兼容变更：旧版本创建/压缩的会话可能仍会显示被污染的标题/预览，直到用本版本再次进行 compaction。

## 0.1.0 - 2026-04-29

- Chord 首次公开发布。
- 提供本地优先的终端编码 Agent，包含 Vim 风格导航、会话管理、模型/服务商配置、工具执行、LSP 集成、图片输入和 headless 远程控制能力。
- 增加 macOS、Linux 和 Windows 的跨平台发布构建。
