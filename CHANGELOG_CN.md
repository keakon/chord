# 变更记录

本项目采用语义化版本风格发布。1.0 之前的版本可能包含不兼容变更。

## 未发布

- CLI：新增 `chord import`，支持从 Claude Code（`claude`）、Codex（`codex`）和 OpenCode（`opencode`）导入外部会话。导入会写入可 `/resume` 的 Chord session，并生成 `import-report.json`；Codex/OpenCode 默认以安全的文本模式导入 tools，Claude 默认 `auto`。
- Runtime：新增请求前 model compatibility normalization：在切换 provider/model 时对历史中 provider-specific payload（如 Anthropic signed thinking、结构化 tools）进行安全回放或降级，避免协议错误。
- Runtime：修复一种潜在卡死：当工具 batch/turn 在等待共享的流式工具执行配额（slot）期间被取消时，现在会产生一条 cancelled 的 tool result，从而保证 PendingToolCalls 能归零、UI 不会卡在 busy。
- TUI：修复运行中工具/Bash 的 spinner 动画：现在每次 visual animation tick 推进一帧，而不是按 wall-clock 时间采样，避免确定性跳帧和旋转不均匀；后台 active 会话现在保持与前台一致的视觉 spinner cadence，同时保留较慢的内容 flush。
- TUI：修复 agent 忙碌时通过 `/models` 选择器切换模型池的时序。现在选择器会立即把切换请求提交到主事件循环，因此已排队的用户消息会在下一次实际发起请求时使用新 pool，而不再需要等待再次发送 draft 或完全回到 idle。
- Worktree：改进 `chord worktree finish` 的失败可操作性。若目标 worktree 已存在进行中的 rebase，`finish` 现在会提前退出并明确提示先收尾该 rebase，避免再次启动嵌套 rebase；若 rebase 发生冲突，错误信息会给出分步恢复命令（`git status`、`git rebase --show-current-patch`，再按情况选择 `--skip` / `--continue` / `--abort`），并基于 `git cherry -v` 提供尽力而为的“可能是冗余提交”提示，帮助判断何时可安全 `--skip`。
- TUI：当 Chord 在后台运行时，如果当前聚焦的 Agent 从 busy 变为 idle，终端标题栏会显示一次性的 `✅` 完成标记；重新聚焦终端会清除该标记。
- TUI：修复 reasoning 输出很快切换到 assistant 正文时 THINKING 卡片的顺序问题。现在 Chord 会在发出首段正文前先关闭并 flush 已缓冲的 thinking；正文开始后到达的 late reasoning 不再重新打开 THINKING，因此 THINKING 不会显示在答案正文下方。
- TUI：修复 input separator 上方偶发出现的“双横线”。当 viewport 锚定到对话底部、最后一个 block 是带 `MarginBottom(1)` 的卡片（assistant / user / thinking / tool / compaction summary）时，卡片透明的 marginBottom 行与上方带 dim 背景的 padBottom 行形成颜色台阶，在 input separator 上方看起来像第二条横线。现在卡片纵向 margin 区间继承卡片背景色，横向 marginLeft / marginRight 仍透明，卡片左缩进的视觉效果不变。

## 0.4.0 - 2026-05-07

- 在默认 TUI 命令和 `chord headless` 中新增 `--worktree [name]`：在启动前创建或进入一个 chord 管理的 git worktree。worktree 路径为 `<state-dir>/worktrees/<repo-id>/<slug>`，分支名 `chord/<slug>`（若配置了 `worktree.branch_prefix`，则为 `<branch_prefix><slug>`）；每个 worktree 拥有独立的 ProjectKey，session、runtime cache 与 exports 自动隔离。`--worktree` 可与 `--continue` / `--resume` 组合，作用域为该 worktree 自身的会话。值为空时按 `task-YYYYMMDD-HHMMSS` 自动命名；分支已挂在某个 worktree 时会直接复用（fast resume）。`chord headless` 启动时若使用 `--worktree`，`ready` 事件 payload 新增 `worktree: { name, branch, path, repo_root }` 字段。
- 新增 `chord worktree` 命令组：`list`（按最近使用排序列出当前仓库的 chord 管理 worktree）、`remove <name>`（删除 worktree 目录及其 sessions/cache/exports，默认保留分支；`--delete-branch` 仅在已合并时删除分支，`--force` 强制删除脏工作区与分支）。创建/进入 worktree 属于启动级动作，由 `chord --worktree` flag 承担、不归属 `chord worktree` 子命令；如需"进入并继续"，使用 `chord --worktree <name> --continue`。
- 新增 `chord resume <session-id>`：根据 session metadata 自动定位会话所在的 worktree（或主仓库），切换目录后继续；与仅在当前项目内恢复的 `chord -r <id>` 互补。
- `config.yaml` 新增 `worktree.branch_prefix`：覆盖默认的 `chord/` 分支前缀（同时影响 `git worktree list --porcelain` 的过滤）。空值回退为 `chord/`；末尾未带 `/` 时会自动补齐；会产生非法 git ref 的取值（以 `/` 或 `-` 开头、包含 `..` / `//` / 空白字符、或含 `[a-zA-Z0-9._/-]` 之外的字符）会在启动时直接报错。
- 扩展每会话的 `session-meta.json`：新增 `repo_id`、`repo_root`、`worktree_name`、`worktree_branch`、`worktree_path`、`is_main_worktree` 字段。已有 session 保持兼容；只含 worktree 字段的元数据文件现在能被正确识别。
- 新增 Google Gemini 一等公民 provider（`type: generate-content`，`api_url` 以 `/models` 结尾）：支持流式文本/工具/思考输出、多模态内联图片、function calling 工具，以及带 `Retry-After` 解析的 Gemini 形态错误响应。
- 修复本地 slash 命令（`/export`、`/models`）：现在始终在主 agent 的事件循环中执行，而不是从 TUI 输入 goroutine 直接调用。此前在 LLM 重试中途提交这两个命令可能会清掉当前 turn，使 UI 卡在"忙碌"状态且无法取消；同时 cancel-busy 按键路径在 agent 报告无活动 turn 时也能正确恢复。
- 修复 slash 命令补全下拉列表：当命令数量超过 8 个时，使用上下键选择会自动滚动可见窗口，确保当前选中的命令始终可见。
- 修复 `/new`：执行后会清空侧边栏 EDITED FILES 区域，不再保留上一 session 的文件列表。

## 0.3.0 - 2026-05-07

- 新增运行时模型池（model pool）：
  - **不兼容变更：** agent 模型配置现在必须通过 `model_pools` 引用 `config.yaml` 顶层定义的一个或多个池；旧的 per-agent 扁平 `models` 列表不再接受。内部 `AgentConfig.Models` 现在表示为 `map[string][]string`（池名 → 有序模型引用列表）。
  - 所有 agent（包括 `primary`）必须定义至少一个池。池名由用户自定义且不做保留，例如 `default`、`base`、`fast`、`strong` 均为合法池名。未显式选择池时，Chord 会回退到该 agent 的 `model_pools: [...]` 列表中的**第一个**池。
  - Agent 可通过 `model_pools` 复用全局定义的池；配置中的内联 `models` 与 `model_pools` 互斥。运行时池策略会按项目持久化当前角色池、按 agent 覆盖以及 last-picked 状态。
  - `/model` 命令替换为 `/models`，支持 `status`、`<pool>` 和 `--agent <name> <pool>` 子命令。TUI 选择器现在选择模型池，而不是单个模型。
  - agent 忙碌时选择模型池会立即提交到主事件循环。当前已发起的请求继续使用开始时快照到的 client，而排队用户消息和其他后续请求边界会在不等待再次发送 draft 或完全回到 idle 的情况下使用新 pool。pending switch 失败现在通过 TUI 的 Update 消息路径回流处理，不再由后台 goroutine 直接改动视图状态。
- 在 diagnostics 与启动日志中加入构建身份信息。`chord --version`、diagnostics bundle 和 TUI dump 现在会包含或展示 commit、dirty 状态、注入的 build time、VCS time、Go 版本和可执行文件 mtime 等信息；MCP client info 继续使用精简的应用版本号。
- 修复 SKILLS 侧边栏状态：`Skill` 工具加载失败不再被标记为已加载（绿色），未发现/不存在的 skill 不再显示，且移除旧的 "(loaded)" 后缀。
- 修复 Codex RATE LIMIT 信息面板：倒计时到期后不再卡在 "1s"；当窗口到达 reset 时间点时会隐藏倒计时，并触发一次尽力而为的用量刷新，使新窗口尽快更新展示。
- 修复 TUI diagnostics/export 状态卡延迟显示：在 assistant 流式输出期间排队的状态卡现在会在当前 assistant 卡片结束后立即出现，而不是一直等到 agent idle。
- 修复权限确认的编辑/拒绝理由输入区无法通过 `Cmd+V` 粘贴剪贴板文本的问题。

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
