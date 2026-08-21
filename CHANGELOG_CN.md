# 变更记录

本项目采用语义化版本风格发布。1.0 之前的版本可能包含不兼容变更。

## 未发布

### 不兼容变更

- 会话 ID（`SID`）改用本地墙钟时间生成，是 17 位纯数字（`YYYYMMDDHHmmSSfff`），嵌入的日期和时间即本地时间。已有会话 ID 不会被改写，因此同一个 sessions 目录可能混有本地时间派生和更早的 UTC 派生两种 ID；在非 UTC 时区下，旧 ID 显示的时间与其实际创建时间不同。会话列表和 `--continue` 现在按最近活动排序——取 `main.jsonl` 与 `usage-summary.json` 两个修改时间中较新的那个——不再按 SID 排序，SID 不再充当排序键。Chord 不会扫描完整会话，也不会额外维护项目级索引；复制或恢复文件，以及没有更新这两份 metadata 的活动，仍可能让排序只是近似值。`--continue` 现在还会跳过已被另一个 Chord 进程打开的会话、改用下一个候选（所有候选都被占用时新建会话），不再因此启动失败，并在启动时给出一条一次性提示（TUI 里是 toast，headless 的 ready 信封里有 `skipped_locked_sessions` 字段，日志里也会记录）；`--resume <id>` 指定的会话已被占用时仍然报错。
- Provider 配置键 `official_api` 拆分为 `trust_http_400`（把 HTTP 400 视为终止性请求错误）与 `retry_after_max_s`（采纳 `Retry-After` 的最长等待秒数，1–86400）。严格解析会在启动时拒绝仍包含 `official_api` 的配置。请把 `official_api: true` 迁移为 `trust_http_400: true` 加 `retry_after_max_s: 86400`；`official_api: false` 改成 `trust_http_400: false`，也可以直接省略。第三方 Provider 现在默认只采纳最长 60 秒的 `Retry-After`；旧配置若依赖更长等待，请显式设置 `retry_after_max_s: 86400`。`preset: codex` 会自动启用可信 400 语义与一天上限。
- `prompt_cache.ttl` 现在会在启动时校验：接受 `"5m"` 与 `"1h"`（`"5m"` 是 API 默认值，会归一化为省略该字段），其余取值报配置错误，不再被静默忽略。TTL 现在在 `explicit` 断点模式下同样生效，而不仅是 `auto` 模式。
- `preset: azure` provider preset 已移除。Azure OpenAI Responses 现在按普通 `type: responses` provider 配置：设置 `auth_scheme: api-key`、`store: true`、`trust_http_400: true` 与 `retry_after_max_s: 86400`，并用 `compat.request_overrides.headers` 将 `OpenAI-Beta` 与 `originator` 置 `null` 移除 Codex 身份 header（这是旧 preset 唯一无法用普通配置表达的行为）。配置中仍含 `preset: azure` 的会在启动时被拒绝；迁移为等价普通 provider 后，线上请求行为与原来完全一致。

### 新功能

- 通知现在会附带一声终端铃声（BEL）。终端聚焦时多数终端（Ghostty、iTerm2 等）会隐藏通知横幅、只静默进通知中心，铃声是此时可靠的提示音渠道；`desktop_notification_foreground: false` 可在聚焦时同时静音通知序列和铃声。是否出声取决于终端配置，文档列出了各终端的开启方式（如 Ghostty 的 `bell-features = system,audio`）。
- Manual MCP 的启用状态现在按会话持久化：`/mcp enable|disable` 会保存期望启用的 server 集合，resume 会话时会在第一次模型请求前恢复；连接失败也会保留意图，显示为已启用但暂不可用，方便重试。模型可通过 `compat.chat_completions.mcp_system_tools_message` 或 `compat.responses.mcp_additional_tools` 显式开启缓存友好挂载；首次模型请求之前启用的 server 直接进入顶层 tools 数组（此时还没有需要保护的缓存），之后启用的才以声明形式追加在固定对话位置，Chord 会原样回放这些声明，禁用时只拦截执行、不改写前缀；模型切换、恢复或 fork 出的历史、持久压缩之后，本次会话运行的余下部分退回顶层工具，全新的空会话则重新可用动态挂载。
- 中断后的会话现在能更准确地恢复未完成的工具结果。结果没来得及保存的工具会标记为**未启动**（工具还没有开始执行，重新核对前置条件后可重试）或**结果未知**（工具已经开始执行，副作用可能部分或全部发生，先核对当前状态再重试）。Chord 会在运行工具前先把工具调用消息写盘，并在执行会改变状态的工具前同步落盘「已开始」标记，因此中断后不会留下「不知道当时想执行这个工具」的副作用；会话目录写不进去时，Chord 会暂停工具执行、避免无法恢复的重复副作用，写入恢复后自动继续。
- Codex 压缩改用原生 remote compaction v2 协议：走普通 `/responses` 流式通道，附加一个 compaction-trigger 输入项和 `remote_compaction_v2` beta 特性协商头，替代旧的一次性 compact 端点。compact 请求还会按 provider 遍历模型池，主目标失败时回落到其他已配置 provider，而不是直接放弃本次压缩。
- 后台压缩现在有实时进度。两条压缩后端都会把传输层字节数和流事件数即时上报，状态栏的压缩胶囊以 `■ 12s ↓ 8.0 KB · 3 events` 这样的后缀展示，并在换 key、换模型、摘要修复重试之间持续累加，不再只显示图标加耗时；整个请求一条传输进度都收不到时，压缩期间也会持续刷新活动状态。
- 新增项目记忆（Memory）：项目根目录的 `MEMORY.md` 加 `.chord/memory/records/` 用来沉淀可复用的偏好、项目事实、工作流与教训；所有文件引用都按项目根相对路径解释，项目整体移动后依然有效。新会话会自动注入有界的活跃索引（大小固定，不随记录数量增长），只在相关时按需打开详细记录，并把记忆当作不可信、可能过时的历史背景——它不授予权限，也不覆盖当前请求。自动抽取默认关闭，通过配置中的 `memory.enabled: true` 按本机+项目开启（项目级配置可覆盖用户级值）；开启后只在主 agent 空闲时后台运行，会用有界的项目规则和当前活跃记忆排除重复内容及临时 Git/任务状态，只在你明确表达偏好应当长期生效时才记住它，而不是从单次说法推断，用修订结论替换过时索引但保留不可变来源记录，在模型调用前与写盘前各做一次敏感信息清洗（best-effort），每次提交后自动刷新当前会话的摘要，并在状态栏显示 `MEMORY` 标识。系统提示词只在有记忆加载时才加入 Memory 使用纪律，只在抽取开启时才追加抽取说明。Memory 文件是普通项目文件：Chord 不会代你暂存、提交或修改 `.gitignore`。

### 改进

- THINKING 卡片现在与 USER / ASSISTANT 卡片对齐：THINKING 卡片及其角色标签与 USER、ASSISTANT 处于同一左列，不再作为单独的嵌套层级缩进到右侧。
- 信息面板新增 `TIME` 板块，展示当前聚焦 agent 的墙钟时间去向：模型流式输出、工具执行、上下文压缩、key/模型冷却，以及等待用户确认或回答的时间。不足 1 秒的桶不显示，短调用不会刷屏；会话恢复后该板块会从 usage ledger 重建。
- 信息面板的 `MODEL` 板块改为两行显示：provider 单独一行，模型 ID 带 `@variant` 后缀放在下一行，不再把 `provider/model@variant` 挤在一行里截断。
- `apply_patch` 现在会在同一补丁里保留无关文件组的成功修改，并把完整的未应用依赖链作为可编辑的补丁参考返回。结果会明确区分已提交修改与未应用操作，提示模型修订陈旧操作而不是原样重试，同时不再在末尾用 `Error:` 重复同一项失败。移动依赖按操作顺序传播：移动失败时，后续触及源路径或目标路径的操作不会单独提交；源文件组在移动后才失败时，依赖移动目标的操作也会一起回滚。文件变更统计与会话恢复只记录真正提交成功的文件组。
- `delete` 遵循同样的部分结果契约：失败前已删除的文件会计入已提交变更，而不是随错误一起被丢弃；会话恢复后，删除操作（包括完全成功的）现在也会重新出现在变更文件侧栏中。
- 工具结果类 hook 载荷（`on_tool_result`、`on_before_tool_result_append` 以及批量 `tool_calls` 的每一项）的 `path`/`paths` 现在优先取自已提交的 FileState：路径为解析后的绝对路径，部分失败的调用只列出真正变更的文件，SubAgent 与主 agent 的载荷形态一致。`src/**/*.go` 这类相对 `path_filter` glob 现在也能匹配项目根内的绝对载荷路径。
- 请求级上下文剪裁现在会在 JSON 对象摘要中保留标量值和精确的大整数，JSON 数组改为抽取首项、中间项与末项，带行号的源码输出则会标明原始行号范围。摘要长度有明确上限；只有 JSON 摘要比原工具结果更小时，才会替换原内容。
- Provider 重试节奏现在可通过 `retry_backoff: exponential | fixed | none` 与 `retry_delay_ms`（0–60000ms）配置。它控制完整模型池重试轮之间由 Chord 生成的等待，以及未携带 `Retry-After` 提示的普通 HTTP 429 key 冷却；合法的 `Retry-After`（受 `retry_after_max_s` 限制）始终优先于已配置的节奏、按原值生效。已确认的配额重置窗口与凭据硬状态仍然优先。作为该变更的一部分，所有 HTTP 429 现在都走普通限流路径：既没有重试提示、也没有已耗尽额度快照的 Codex usage-limit 429 从共享的 1 秒指数默认值起步，而不再是 preset 的 1 分钟冷却（preset 分支仍处理非 429 的 usage-limit 错误）；打断可见流式输出的 429 也会应用同样的节奏并轮换到下一个 key，而不是在同一 key 上无冷却重试。
- 系统提示词与工具描述不再重复相同的引导规则，每个 agent 只会看到与其角色和可见工具匹配的指导：Done 工具仅在可见时才被提及，其必需信号来源有了具体所指（对话中的显式工作流指令，例如 loop 锚点）；loop 完成要求改为引用 Done 报告结构而不再内联第二份副本；SubAgent 的开放产品决策和必要提问会被路由到 owner agent，而不是让它去"询问用户"；agent 被明确告知执行授权由权限系统自动处理，无需自行评估风险。
- 委派策略——用 Notify 延续既有任务还是新建 delegate、仅在写作用域明确独立时并行——现在对所有能委派的 agent 可见，包括启用嵌套委派的 SubAgent。
- 若干小型清理：压缩后的两条继续执行提示合并为一条 system reminder；Read 工具的截断语义按状态逐条说明（`budget` / `stale` / `superseded`）；planner 提示词明确了计划文件的命名规则；中文 bug 排查的结论比较类问题（"哪个结论更对 / 哪个更正确"及变体）按结构匹配、无需持续追加短语列表，普通代码审查请求仍不会触发故障调查工作流；新文件"发挥创意且全面"改为具体指令——覆盖请求隐含的边界情况、不虚构额外范围。
- 默认请求输出 token 上限（`max_output_tokens`）从 `32000` 提升到 `64000`，工具密集和推理密集的回合更不容易在响应中途被截断。实际请求上限仍取全局上限、模型 `limit.output` 与剩余上下文空间三者中的最小值。未显式配置 `limit.input` 的模型会为输出预留更多窗口，因此推导出的输入预算变小、自动压缩会更早触发。未配置 `limit.output` 的模型会直接按新上限发起请求，若后端实际输出能力更低且在服务端校验 `max_tokens`，请求可能开始被拒绝——请为这类模型声明 `limit.output`，或设置 `max_output_tokens: 32000` 保持旧上限。
- 只读 shell 输出与 JSON 文档在上下文剪裁前保留更久：无副作用的检查类命令使用专门的 `context.reduction.shell_read_only_age_turns: 3` 阈值，非行式日志的 JSON 文档保留到过期阈值，不再提前骨架化。
- 上下文剪裁生成的搜索摘要现在在字节预算内保留完整的 `path:line` 位置清单，不再收缩成笼统的省略标记，模型仍能定位到每一处报告的匹配。
- 写后 LSP 诊断现在会对当前工具工作目录内的文件显示相对路径，目录外文件仍保留绝对路径。这样能缩短 `write`、`edit`、`apply_patch` 的结果和模型上下文，同时不改变 LSP 运行时内部的文件身份。
- 模型在剪裁后重新获取过的工具输入不会被再次剪裁（`recalled_input_protect`）：重读被裁的文件或重跑被裁的查询会在本轮循环内持久保护该证据。
- `yy` 复制 `edit` 工具卡片时，现在用 `## old_string` / `## new_string` 两段展示替换文本，取代统一的 diff；只有启用时才增加 `## replace_all`，默认复制结果更精简。
- `write` 不再因为目标文件在读取后被修改而拒绝写入。它会把之前的非空内容备份到当前会话目录并继续写入。工具结果会提示文件已变化，并且只有确实创建了备份时才给出备份路径——模型和用户看到的是同一段文本：备份只是尽力而为，不能对一方声称存在安全网却对另一方隐瞒。备份失败不会阻止写入，只记录在本地日志里。模型从未读取过的文件在覆盖前仍要求先完整读取。

### 修复

- `edit` 失败时，工具卡现在把 `old_string → new_string` 显示成带语法高亮和增删行背景的替换预览，不再把多行代码压成带转义符的 JSON。`replace_all` 与可操作错误会分段显示；复制卡片时仍能拿到未截断的精确字符串。
- 修复 Edit 工具卡在删改行数不一致的 diff 块里丢行的问题。此前一个 hunk 把两行替换成一行（或一行替换成两行）时，多出来的删除/新增行会丢掉整行背景，甚至整个从 diff 视图里消失。
- `apply_patch` 工具卡片现在明确标注每一段的含义：计划涉及的文件列表是 `Targets:`，已提交的 diff 是 `Applied changes:`，提交的输入是 `Requested patch:`。部分应用的补丁会把已应用摘要、`Error:` 文本和 `Diagnostics:` 拆成有序的独立段落，不再糊在一起，也不再重复上方 diff 已经展示过的请求补丁。目标行、操作行和补丁行改为用 `…` 截断而不是折行，这样一个文件或一行补丁始终占一行，窄终端下卡片结构不会散架；复制卡片（`yy`）拿到的仍是未截断的完整文本。中间被省略的超长 diff 行现在会在 `…` 标记处保留 diff 行背景色，不会在色带上留下空洞。
- 状态栏的 "Since HH:MM" 锚点不再随后台压缩漂移：压缩期间的周期性心跳会重复发射同一活动，旧逻辑每次都会重置计时器，显示的开始时间可能每个心跳间隔就前跳一次。同类型重复保持原始起点，真正的活动切换仍会刷新锚点。
- 后台压缩不再覆盖前台的实时状态。压缩的启动、等待草稿就绪与保活心跳都不会再顶掉状态栏上正在运行的主模型或工具状态；前台工作结束时活动栏立即交还，不必等到下一次心跳。压缩的生命周期与取消改为通过原子显示状态投影，异步生产者不再直接读取事件循环状态；超长上下文触发的压缩也改走主事件循环，使请求收尾、重试上限与槽位交接与该轮其余状态保持有序。前台与后台的压缩指示器共用同一个平稳的 1 秒动画节拍，传输进度在拿到字节数或流事件任一信号时就开始显示。
- 主模型正常以 `stop` 结束时，Chord 不再因为 Agent 回到 idle 就立刻新建一轮自动压缩。阈值信号会保留到下一次主模型请求准备阶段再启动压缩；如果 stop 之前已经有压缩在后台运行，Chord 不会取消它，压缩仍会在下一个安全的 continuation 或 idle barrier 应用。
- 「模型不支持 image/PDF 输入」的提示改为每个模型只显示一次，不再每次请求都弹。切到不支持图片/PDF 的模型时，即使会话里已带图片或 PDF 内容，后续每一轮或每一次请求都不会再重复提示；再切到另一个同样不支持的模型会出现一次新的提示，用户输入被丢弃与工具结果被丢弃两类情况各自独立提示。
- MainAgent 的排队输入不再等整条模型 fallback 链结束后才处理。Chord 向下一个 fallback 模型发请求前，会先把此时已经排队的用户消息写入对话，并随该请求一并发送；请求发出后才到达的消息仍等待下一个安全边界，已经在途的请求不会被改写。
- 文件路径拼写建议不再扫描会话工作目录或工具配置根目录之外的目录，也会阻断通过符号链接祖先越界的情况。其他位置的绝对路径不会再把无关文件名带入工具结果和模型上下文；home 目录内的路径仍可获得精确、受限的建议。
- OpenAI 兼容 chat 流现在除了 `reasoning_content`，也能识别通过 `reasoning` 或 `reasoning_text` 返回的可见思考过程。网关把同一段增量重复写进多个别名字段时，Chord 会按该顺序取第一个非空字段，不会重复显示或回放思考内容。
- 长上下文定价档现在按完整提示词大小选择，并计入缓存读取与缓存写入 token；请求跨过配置阈值后，会使用正确的输入、输出与缓存费率。
- 文件变更统计现在会合并同一路径的工作区相对形式与绝对形式；恢复会话更新项目根目录后也能正确合并，并按最后一次操作判断文件最终是已删除还是已恢复。
- 请求级上下文剪裁不再摘要模型从未看到过的新鲜工具输出。结果在首个能对它作出反应的请求上就已是 age 1，此前 `shell_success_age_turns`/`read_like_age_turns` 默认值 1 会让大小类规则在模型完整看到大体积成功输出之前就将其摘要；两项默认值升至 2（硬编码的 diagnostics 规则同步跟进），保证每份新鲜的成功输出恰好被完整看到一次。已失效或已被覆盖的读取仍立即渲染有效性标记：它们跳过年龄门控和 high-risk/diff 保护分支，而不是继续回放陈旧的文件内容。
- 自动压缩在 prompt cache 命中时不再"失明"。阈值此前直接比较上报的 `input_tokens`，但 Anthropic 风格 wire 把缓存前缀报在 `input_tokens` 之外，部分 OpenAI 兼容中转只报未缓存的余量，于是缓存命中的请求看起来像近乎零的 prompt，并污染字节校准。现在会把用量归一化为完整 prompt，且阈值比较响应后的上下文基线（完整 prompt 加输出）——下一个请求会同时回放两者，只比较 prompt 会让压缩晚一个请求才触发。
- 服务端自行开启 thinking 的端点（网关后的 DeepSeek 系列）在请求侧 reasoning 被关闭后，不再沿 "reasoning_content must be passed back" 400 逐级降级：当前轮的空 reasoning 填充现在即使 thinking 控制字段已被剥离也会执行，空字段即可满足存在性校验。
- 已完成轮次的明文 `reasoning_content` 和无签名 thinking 块默认不再回放给后端。多数 thinking 模式后端只校验最后一条 user 消息之后的 reasoning，更早轮次的 reasoning 会在服务端被丢弃，因此回放的历史每次请求都计费却从未被使用——在受影响端点上占大型请求的一半以上。当前轮 reasoning 以及带签名或 redacted 的 Anthropic thinking 块不受影响。文档明确要求回传完整 assistant 历史的 preserved-thinking 模型（Kimi K3 / `keep: all`、Qwen `preserve_thinking`、GLM `clear_thinking: false`）通过新增的 `compat.reasoning_continuity.preserve_history: true` 保留历史；模型配置指南中的相应模板已同步设置。
- 当 OpenAI 兼容端点在 reasoning 启用时拒绝 forced `tool_choice: required`，现在可以按模型配置 `compat.forced_tool_choice.suppress_in_thinking: true` 关闭强制工具选择。DeepSeek V4 Chat 模板已启用该项；Responses 接口支持 `required`，对应模板会保留强制工具选择。是否降级按 request override 最终生效的请求判断，因此 override 关闭 reasoning 后不会再误降级。
- DeepSeek V4 Responses 续跑不再报 `reasoning_text must be passed back to the API`。Chord 现在会保留并回放原生明文 reasoning item；从其他模型切换导致原生 reasoning 无法回放时，会把对应的历史工具调用轨迹改为文本记录而不是结构化 `function_call`，续跑不再依赖缺失的 reasoning。DeepSeek V4 Responses 配置示例通过 `compat.reasoning_continuity.mode: openai_visible` 启用跨模型兜底。
- 同一份 read 输出既重复、又已失效或被取代时，上下文剪裁现在始终使用稳定的有效性标记，不再反复让 prompt cache 失效。
- 上下文压缩遇到卡住或完成事件不匹配时会正常恢复，不再一直停在运行中并阻止后续自动压缩。草稿生成现在有明确截止时间，迟到结果不能跨会话生效（压缩收尾时切换会话会提示稍后重试），压缩模型调用也会计入用量统计。
- 环境信息与 AGENTS.md 会话指引现在会出现在 MainAgent 和 SubAgent 的每次请求中；压缩或恢复会话后仍然保留，同时不会反复让 prompt cache 失效。
- 普通代码审查请求不再触发面向故障排查的 bug 分析工作流。
- planner 不再把每个请求都变成一轮规划。只读审查与直接提问会被直接回答，不再强制产出 plan 文件与 Handoff 调用；被拒绝的 Handoff 改为修订既有的 plan 文件，而不是新建一个 plan 编号。
- SubAgent 的自动恢复请求不再被排队的用户输入静默取消。当模型只返回纯文本、流被瞬时传输错误截断、或完成被验证拒绝时，Chord 会发出一次有上限的恢复请求；但此前该请求发出时"请求进行中"的闸门已被清空，事件循环因此把该 SubAgent 视为空闲：它可能取走排队中的用户消息并开启新回合（新回合会取消这次恢复请求），或者直接把该 SubAgent 挂起。现在这些恢复路径会在发出请求前重新置位该闸门，与既有的上下文超长恢复路径保持一致；同时也避免了模型池切换因漏判而在请求中途替换 LLM 客户端。
- 当输出上限在模型产出任何内容之前就被触及时，回合不再静默结束。推理可能耗尽整个输出预算，使响应既没有工具调用也没有可见文本；而既有的截断恢复机制只在存在畸形工具调用时才会介入，因此这种形态会落到普通的"无工具调用，转入空闲"路径——没有警告、没有错误、界面上也没有任何内容，整段记录在一张 thinking 卡片之后就停住了。现在 Chord 会用一条引导模型立即行动（而非继续推理）的恢复提示重试这种响应，重试次数受既有的两次上限约束；若反复触及上限则报告明确的错误。这种形态不会尝试自动压缩，因为原因是输出预算而非输入过大。若被截断的响应确实带有文本，则保留这段部分回复，并提示它并不完整。
- 空闲终端通知不再播报用户已经读过的内容。此前通知文案会向前查找最近的 assistant 或 error 卡片，因此当某个回合的回复始终没有出现时（例如上述输出上限截断），播报的是同一回合中更早的过程叙述——往往是一句以冒号结尾的话，读起来像"仍在进行"，而不是"已停下，等你"。现在查找会在工具卡片和用户卡片处停止，当回合没有产生属于自己的回复时退回到中性的就绪文案。
- 当 Anthropic 兼容网关把一个完整的 `tool_use` 块插入某个 thinking 块中间时，该 thinking 块不再被拆成两张 TUI 卡片。这类网关会交错发送 content block——先是 thinking delta，然后是一整个工具调用，再是*同一个* thinking 块的剩余部分——而此前开始工具卡片会顺带定型并摘除正在流式输出的 thinking 卡片，导致剩余 delta 另起一张卡片，且通常断在词中间。现在 thinking 卡片会保持挂载直到它自己的 `thinking_end` 到达，因此显示的思考耗时也覆盖整个块，而不只是续写的尾段。真正的终结点（回合结束、取消、错误、idle）仍会定型未收到 `thinking_end` 的 thinking 卡片并计算兜底耗时，行为不变。
- 上下文剪裁生成的搜索摘要不再静默丢失省略元数据。此前当所有文件分组都放进了渲染列表、但末尾的"其余行已省略"标记超出字节预算时，该标记会直接消失，摘要看起来像是完整的；现在会回收已渲染的分组为标记腾出空间，摘要始终报告省略了多少文件、匹配和其余行。
- 上下文剪裁的重复调用匹配现在只信任明确成功的结果：失败的尝试不再掩盖同一调用此前的成功运行，大整数参数在归一化过程中保持精确身份。
- 每一种任务终态转移现在都经由同一 journal 路径结算：取消、过期与停止的任务都会留下一致的持久化 settlement，而不是仅翻转记录状态。
- 验证声明不再因 epoch 间隙被拒绝：最新声明的命令必须覆盖当前 workspace epoch，较早声明的命令则可以来自更早的 epoch。这样既继续阻止过期验证，又不会误拒绝 lint 加 test 这类真实的多命令声明。
- 流事件的回放拒绝现在以事件实际携带的 status 为准：没有显式 HTTP status 的 SSE 事件保持可重试而不被猜测为终态，携带真实 status 的 WebSocket 帧按其分类。
- 不带工具标记的请求不再发送只对带工具请求有意义的字段。Chat-completions 请求在未声明工具时省略 `parallel_tool_calls` 与 `tool_choice`，Anthropic messages、Gemini generateContent 与普通 Responses 生成请求也相应省略 `tool_choice` / `toolConfig` / `parallel_tool_calls`；原生 Codex 压缩请求继续遵循专用 wire contract。OpenAI 兼容端点在未提供 `tools` 时收到 `parallel_tool_calls` 会返回 HTTP 400，导致 `chord doctor models --model ...` 这类无工具的探测请求失败。
- 协调用 JSON（agent 请求、任务组、settlement）在 rename 前会 fsync；恢复时遇到损坏文件会隔离并重建，而不是中止恢复或向坏文件续写。
- 用量账本扫描现在容忍其他版本写入的 `usage.jsonl` 行中的未知字段与空 event id，不再丢弃整个账本。
- 从渲染输出重新解析的 LSP 诊断会与服务器推送的诊断去重；服务器为已 review 的文件推送更新时，review 计数也会刷新，侧栏不再显示过期或翻倍的数字。
- 多文件编辑的 LSP 诊断反馈更紧凑：每个文件的新增/已解决计数并入该文件块的标题，不再把每个路径重复两遍、也不再把一层变更摘要嵌进另一层。
- 工具卡片头部现在按模型的原始写法保留小数与大整数参数值，不再经 float 格式化往返。
- 相邻的 markdown 加粗片段（`**a****b**`）在 TUI 中正确渲染，不再泄漏星号字面量。
- 复制助手卡片里的鼠标选区不再丢掉行首字符。把屏幕列换算回文本所依赖的每行缩进宽度，此前整体下移了卡片底部 padding 与 margin 的行数，于是每一行都按邻行的缩进来量：在代码块里选中文本时，若附近某行发生了软换行（软换行会加一段悬挂缩进），复制出来的内容就少了开头两个字符；这些行上的双击选词也会选错边界。
- WaitingMain 过期清扫不再与任务复活竞争：结算过期的 parked 任务时，会在 settlement journal 锁内复核该任务仍处 parked、仍在等待且仍已过期，因此在此窗口内被直接回复复活的任务不会被从存活 attempt 下取消。停止 parked worker 时若 attempt 被并发变更，现在会如实报告冲突并建议重试，而不是声称任务消失；cascade 取消在 settlement 冲突落败时会将运行时钉到已结算的终态，使其仍可被 park。
- 协调用 JSON 的原子写入现在在 rename 后对父目录 fsync：写入刚完成即崩溃时，agent 请求、任务组与 settlement 不会再回退到上一版本，重新打开标识符复用的口子。
- 会话元数据（`session-meta.json`）的写入获得同等待遇——rename 前 fsync、rename 后同步目录：改标题、`/mcp enable` 或 fork 刚完成即崩溃时，会话元数据不会再静默回退到旧版本。
- 退出流程现在会等待后台的 OAuth 元数据回填写完，而不是丢下可能写到一半的 auth 文件；等待配置文件锁的过程可被取消，退出不再因别的进程持锁而挂住。
- Gemini 的 thinking token 现在计入 output 用量与成本。Google 上报的 `candidatesTokenCount` 不含 thoughts、计费却按 output 价，此前重思考的 Gemini 调用会同时低估 output token 数与估算成本。
- 零用量的诊断事件（压缩生命周期与溯源、失败分类、超限恢复）不再抬高 `/stats`、状态面板与项目报表里的 LLM 调用数。它们仍完整保留在 `usage.jsonl` 中可供查看；已有的 summary 会在下次重建时自动修正。
- 持久化降级标志现在随会话切换复位：它描述的是诊断时所在的会话目录，`/new`、`/resume` 或 fork 后以健康状态开始，磁盘真有问题时首次写入会立刻重新降级。关停或切换期间与主动关闭竞争的写入不再给下一个会话捏造降级告警。
- 上下文剪裁不再把不可召回的输入注册进 discarded-input 集合：变更类 shell 输出与 edit/apply_patch 诊断的剪裁——其 key 内嵌完整原始参数（apply_patch 是整个 patch）——此前会被记住整轮循环却永远无法转化为召回保护，导致该集合及其每请求克隆在长会话中无界增长。
- `lsp` 工具在向语言服务器发起查询前会先按当前文件内容校验光标位置：非正数坐标、超出文件末尾的行号、超出行内长度的字符偏移都会被直接拒绝，错误信息给出有效范围并建议重读文件，而不是表现为语言服务器侧的费解报错。查询失败的错误现在也会带上操作、路径与位置。
- apply_patch 工具卡片头部现在与结果里的路径摘要一致，用 `D` 前缀标注被删除的文件；删除操作的内容 diff 是有意隐藏的，此前仅含删除的 patch 卡片看起来与普通更新没有区别。
- 任务复活不再从复活侧与 WaitingMain 过期清扫竞争：attempt 决策现在读取当前任务记录而非调用方快照，最终提交时若任务已在复活期间被结算则退避。此前清扫在该窗口内胜出会被复活静默覆盖——注册表回退到存活 attempt，而该 attempt 的 cancel 结算已经存在，任务随后的真实完成从此永远无法记录。竞争落败的复活现在如实报告冲突，重试会开启全新 attempt。
- 从损坏 journal 的有效前缀恢复出的结算现在会在隔离后重新灌入新 journal。此前它们只存在于内存与任务注册表中，restore 却将其标记为已持久化，因此永远不会再被补写——注册表随后一旦损坏，这批结算的 existing-wins 保护会静默丢失，重放的 completion mailbox 便可能改写已定的结局。
- 终端标题的旋转动画不再在流式输出或工具执行期间突然加速。此前它除了按固定节拍推进，还会在每次活动/进度更新时额外推进一帧，繁忙的回合会让标题转得远比固定节拍快；现在只按标题 ticker 的节奏推进，前台、后台忙碌与 tmux 三档速度保持稳定。
- Handoff 弹窗的拒绝原因输入框不再被二次换行：输入框原本按确认对话框的宽度排版，而 Handoff 弹窗以自己的（更窄）宽度渲染，textarea 已经折行的内容会被弹窗再折一次，硬切点可能落在单词或中文字符中间。现在输入框在弹窗展示、终端缩放与粘贴时都使用 Handoff 弹窗的实际内容宽度，Shift+Enter/Ctrl+J 插入的换行与自动折行都保持输入时的样子。

## 0.7.3 - 2026-08-08

### 不兼容变更

- 上下文剪裁不再接受 `context.reduction.high_pressure_usage` 或 `force_prune_usage`；工具输出何时被剪裁现在由 request-batch age 阈值决定。请从已有配置中删除这些键。Chord 会给出迁移错误，不再静默忽略。
- `patch` 工具被 `apply_patch` 取代：采用 Codex `*** Begin Patch` 信封格式，将多文件 Add/Update/Delete/Move 操作作为一个事务应用——每个文件基于同一份文件系统快照规划，提交前重新校验内容与文件 mode，任一提交步骤失败时整体回滚（包括自身写入失败的那个文件）。同一规范化路径可重复出现不带 Move 的 `*** Update File:` 段，并遵循 Codex 的顺序语义：后一段修改前一段的内存结果，最后把原始内容到最终内容作为一次文件 mutation 提交；因此后段匹配失败时工作区仍保持不变，不会暴露 Codex 的部分写入行为。兼容性保持不变：旧的 `patch` 工具名与单文件 `{path, patch}` 参数继续可用，`patch` 权限规则键在解析时归一化为 `apply_patch`。权限模式现在匹配 patch 触及的每一个路径，并做词法规整，`./secret/x` 无法绕过 `secret/*` 的 deny；patch 内的 `*** Delete File:` 操作仍受工具级 `delete` 规则约束。删除文件不再要求先完整读取——其安全性由路径解析、权限规则、跟踪锁与删除前备份承担。
- 配置加载现在在所有层级拒绝未知 YAML 字段——顶层、provider、model、`context`、`compat` 及各嵌套块——不再静默忽略。旧版本 Chord 曾容忍的键（拼写错误，或降级后残留的新版本字段）现在会以指明具体字段的解析错误阻止启动；升级前请删除或修正这类键。空文件与整体注释掉的配置文件行为不变，顶层 `model_templates` 键保留为纯 YAML anchor 命名空间。

### 改进

- 模型初始化现在为 GPT-5.6 使用保守的 Codex 专用限制（`400000` context / `272000` input / `128000` output），并将 GPT-5.4 input limit 保持为 `950000`。模型配置速查对基于 Codex 的中转采用相同默认值，同时说明如何手动启用完整的 1.05M OpenAI API 窗口。Provider 配置支持 provider 级 `parallel_tool_calls`，并为 Responses 与 Chat Completions 网关提供可选的 wire compatibility 开关。
- 工具结果现在会携带一次性效率提示：当某一轮工作陷入逐次单个只读查询，或对单个一次即可读完的文件反复小窗读取时，在恰当时机重新提示并行批量调用与整文件读取。在构建或准备阶段即失败的测试命令现在会先引导执行仅构建检查；shell 超时错误会说明如何缩小范围或转入后台重试。
- shell 工具结果在命令超过慢命令阈值后会附加简洁的墙钟耗时提示，让模型据实权衡下一步要跑什么；亚秒级命令保持不标注。
- reasoning 回放改为乐观尝试并按 target 自适应降级：chat 原生 reasoning 与协议原生 item（Responses 加密 reasoning、Anthropic thinking block、Gemini thought signature）首次会回放给任何使用相同 wire 协议的目标，包括跨 provider fallback。拒绝该载荷的目标会在本会话内被降级——先严格 provenance 匹配，再文本化历史——不兼容的后端每会话最多浪费两次失败请求，而不是每次 fallback 都丢失 reasoning 连续性。
- 新增可选的 `compat.reasoning_continuity.mode: anthropic_unsigned`：用于返回可见无签名 `thinking` 的 Messages 兼容 endpoint（如 DeepSeek/GLM）。无签名 thinking 只向同 provider/model 原生回放。目标提供该结构化 carrier 时，可见的 OpenAI Chat `reasoning_content` 也能转换为无签名 Anthropic thinking，从而无需伪造签名即可保持 reasoning 可见。跨 provider 或被目标拒绝时仍保留已完成的工具轮次；目标无法结构化承载 reasoning 时会将其丢弃，而不会泄漏进 assistant 正文。
- MCP 工具调用现在会在 TUI 卡片头部显示模型生成参数的紧凑摘要：标量值保持可见，JSON 值摘要为 `{N fields}` 或 `[N items]`，长值截断显示。MCP server 的启动参数与环境变量不属于模型生成的工具调用参数，不会被加入卡片。
- 用量统计现在跨协议归一化：顶层 input 计数视为完整 prompt（含单独报告的 cache read），响应后的上下文基线计入 input + cache write + output，因此无论 provider 如何拆分缓存桶，compaction 阈值、输入预算显示与用量统计都保持一致。新增 `compat.usage.input_includes_cache_read` 与 `compat.usage.input_includes_cache_write` 覆盖项，用于修正 usage 字段与声明协议不一致的网关；缓存感知的 fallback 定价现在使用本会话内按 model ref 观测到的精确 token 加权缓存命中率。用量统计采用当前的 `usage.jsonl` ledger 格式；无效或过期的 `usage-summary.json` 会从 ledger 重建。
- 待办列表现在允许同时存在多个 `in_progress` 项，前提是 agent 确实在多个不同的活动工作流之间切换，且每项使用唯一的 `active_form`；仅处于计划中、被前置条件阻塞或等待开始的工作必须保持 `pending`。
- 上下文剪裁现在用专门的 `diff_protect_age_turns: 12` 阈值保护 diff/patch 证据；剪裁旧的 diff/patch 输出时会保留文件、hunk、变更计数与有限的代表性行，而不是通用的省略标记。
- `apply_patch` 的 hunk 匹配新增最后的标点归一化兜底，适用于任何可解码为文本的文件：中文与 ASCII 标点视为等价，仅在完整 hunk 恰好只有一个唯一匹配时应用，未改动部分保留文件自身的标点，并在结果中报告使用情况；歧义匹配会被拒绝。"Hunk not found" 错误现在报告 `(N/M)` 进度、附简短期望行预览，并在文本仅出现在更长行内部时说明原因。
- 未找到路径的建议现在会针对来自已标记项目目录的相对请求搜索工作树根目录，且评分要求合理的拼写距离，不再建议无关文件。
- `apply_patch` 结果现在与 `write`、`edit` 一样运行写后 LSP 诊断流程，并以统一格式报告诊断。
- 原生支持 `apply_patch` 的模型不再看到 `write` 与 `delete` 工具：`apply_patch` 是它们唯一的写入/删除入口，依赖回退编辑工具的模型则保留两者。显式的 `delete` 权限规则（包括通配与更窄 glob 的分层）仍然约束 `*** Delete File:` 操作。
- 工具卡片完成后会显示该次调用的墙钟执行耗时。
- 终端通知现在在 TUI 聚焦时也会触发：新增 `desktop_notification_foreground: true`（默认）在聚焦状态下也发送通知，设为 `false` 则恢复仅在失焦时通知。Chord 会按终端自动选择 OSC 9 / OSC 777 转义序列，是否附带提示音由终端决定。
- SubAgent 完成验证记录（例如 `go test ./... [failed]: exit 1`）现在会随任务恢复与 compaction 持久化，并在 TUI 中一致渲染；provenance 未知时恢复的续跑会被拦截。
- 委派任务协调现在具备持久性：不可变结算、任务组、邮箱载荷与父子请求/响应状态都能跨 compaction 与重启存活。`notify` 新增 `response` message type，配合 `target_task_id` 与 `correlation_id` 发送结构化回复；`notify_peer` 的 peer 路由仅限存活的兄弟任务，且跨任务水合保持幂等投递。
- Compaction 现在为 provenance 与生命周期各阶段（source-ref 计数、耗时、失败类别）输出结构化 analytics 事件，供诊断使用。

### 修复

- 仍然有效的 `read` 输出不再被请求级上下文剪裁裁掉。此前，超过若干轮次且大于 read-like 尺寸阈值的读取即使仍是模型对该文件的唯一现行视图也会被摘要化，在上下文远未达到上限时就迫使模型分页重读、甚至只凭摘要作答。现在剪裁只处理确定过期（文件变更后的 `truncated=stale`）或在上下文后部有更新副本（`truncated=superseded`）的读取；仍然有效的读取无论年龄、大小或同批并行读取数量都一律保留，容量压力仍由持久 Compaction 负责。重读即是自然的恢复方式——不会输出任何机器可读的恢复元数据。`context.reduction` 另外接受布尔简写：`false` 完全关闭请求级剪裁（此前是解析错误），`true` 保持默认参数；未知的 reduction 键现在会报错，不再被静默丢弃。
- 整文件 `write` 授权现在会把完整 `read` 或 `<file>` 引用绑定到模型实际看到的同一份字节。局部/过期的文件引用、读取完成前被外部修改的文件，以及 SubAgent 工作目录下的相对路径都不能再授权覆盖模型未见过的版本。`delete` 则有意采用路径授权而非读取门控（见上文 `apply_patch` 条目），删除文件不再强制先做一次浪费的完整读取。
- 跨 provider、跨协议 fallback 在原生 reasoning 无法回放时不再抹掉已完成的工具历史。Chord 现在跨 Chat Completions、Responses、Messages 与 Gemini 保留结构化的 call/result 对；只有目标提供结构化 reasoning carrier 时才转换绑定动作的可见 reasoning 或公开摘要；目标拒绝结构化形态后才把已完成的工具轮次文本化。不受支持的 reasoning 会被丢弃，不会注入 assistant 正文。Fallback provenance 现在记录实际运行目标的 wire family，normalize 日志也会输出前后消息数及降级/丢弃计数。
- 修复模型切换事件可能被丢弃、以及事件循环阻塞在已满输出通道时 shutdown 死锁的问题：running-model 变更现在可靠投递；Shutdown 会立即释放被阻塞的输出发送，并等待事件循环完全停止后再 checkpoint SubAgent、保存最终 recovery 快照；persist/compaction 排干超时现在返回错误，不再带着可能不一致的状态继续。
- `edit` 工具新增引号容错兜底：当精确匹配与尾换行匹配都失败时，按引号标点归一化后重新匹配 `old_string`，并采用保留意图的替换——对未改动的上下文保留文件原始字节。这修复了模型无法逐字复现弯引号导致的主要编辑失败。同时接受已废弃的 `filePath` 参数作为 `path` 的别名（与 Glob/Grep 一致）。
- 后台 `spawn` 完成后现在必定在主会话留下结果卡片。此前主 agent 自己的后台任务卡片带着会被主视图过滤掉的归属，只能看到 toast 通知；空闲时完成的任务在后续回复之前完全不显示卡片；卡片事件在 UI 事件通道饱和时还可能被丢弃。所属 SubAgent 已不存在的结果现在显示在主会话中，而不是一个再也打不开的视图里。完成的后台工作现在渲染为专用的 JOB RESULT 卡片，并作为结构化后台结果消息持久化、可跨会话恢复保留；完成 toast 的级别也反映结果状态（取消、失败或成功）。
- 没有显式 HTTP status 的 SSE 与 WebSocket 流事件不再被猜测为 4xx/5xx：每个 `APIError` 都会记录来源（`http_response`、`sse_event` 或 `websocket_event`），未知的 Codex WebSocket status 保持原样而不再强制为 500，流事件在缺少显式请求、鉴权、上下文或回放信号时保持可重试。瞬态 provider 容量信号（overloaded、限流、暂时不可用）不再进入 reasoning 回放降级阶梯；模型重复内部回放证据而不继续任务时会被识别并当作回放失败处理。
- LSP 的 `workspace/applyEdit` 请求现在被直接拒绝：语言服务器不能再要求 Chord 应用任意的 workspace 编辑，该路径上不会发生任何文件写入。
- LSP review snapshot 现在能正确为 `apply_patch` 调用重建：持久化的文件状态元数据会标识每个写入、移动或删除的路径，每条 review snapshot 也记录自身路径，使逐文件诊断在恢复与 compaction 后仍保持准确。
- 失败的 SubAgent 工具结果现在会在终结关闭路径可观察到该失败实例之前持久化落盘，restore 与 export 始终能看到已持久化的错误结果。
- 畸形调用、compaction 与终结恢复的重试现在都有上限，回放前会丢弃失败的部分输出。SubAgent 的 shell 证据在恢复后会得到验证，同时保留 model-pool 与配额 fallback 行为。
- TUI 的 apply_patch 路径摘要现在为删除操作显示 `D` 标记。
- 无净变更的 `apply_patch`（没有任何实际文件变化）现在返回 `No net file changes`，而不是空 diff。
- MCP 工具卡片现在保留模型生成的原始参数：头部摘要始终反映模型实际生成的调用，不再被钩子或确认改写后的实际执行参数覆盖；执行状态更新也不会再覆盖恢复卡片上已记录的调用参数。
- 即使关闭超时，SubAgent 的 MCP 服务器现在也会被关闭：清理操作会延迟到 agent 运行结束，关闭预算耗尽时不再泄漏 MCP 传输连接。

## 0.7.2 - 2026-07-20

### 不兼容变更

- MCP 配置作用域现在具有明确语义：`.chord/config.yaml` 中的同名 server 会原子替换全局定义，不再逐字段递归继承；Agent 级 MCP 仅允许增量添加并自动启动。Agent server 与顶层重名或设置 `manual: true` 现在都会导致启动失败；要继承请删除 Agent 中的重复项，要使用私有连接请改名，手动启停的 server 则应配置在顶层。
- 相比 v0.7.1 及更早版本，`compat.reasoning_continuity.mode: openai_visible` 现在只负责回放 assistant `reasoning_content`，不再隐式注入 GLM 专属的 `thinking.type` 或 `clear_thinking` 字段。Provider 请求差异统一通过新的协议无关配置 `compat.request_overrides` 表达：`body` 递归 patch JSON（`null` 删除字段），`rename_body_fields` 将动态计算值保留到另一个字段名，`headers` 设置或删除请求 header。已有 GLM Preserved Thinking 配置需要在 `request_overrides.body` 中加入 `thinking: {type: enabled, clear_thinking: false}`；DeepSeek thinking 配置需要加入 `thinking: {type: enabled}`，并在需要时把 `max_completion_tokens` 重命名为 `max_tokens`。
- Agent 定义文件在声明的 `name` 与不带扩展名的文件名不一致，或同一目录内存在跨 `.md`、`.yaml`、`.yml` 的重名 agent 时，现在会直接加载失败。此前不一致的 `name` 会静默替换另一个无关的 agent 定义；请把文件名或 `name` 字段改为一致。项目级 agent 覆盖同名全局 agent 的既有设计不变。

### 改进

- `PgUp`/`PgDown` 现在可以在不离开 Insert 模式的情况下翻页浏览会话记录（新增 `insert_page_up` / `insert_page_down` keymap 动作），复用 Normal 模式的翻页逻辑，包括启动时延迟加载的记录窗口。这会有意遮蔽输入框自身的光标翻页——输入框被钳制在几行高度，光标翻页几乎无用。
- 在任意覆盖层或对话框内按 `Ctrl+C` 现在与 `Esc` 行为完全一致，不再进入退出序列；退出只会从顶层 Insert/Normal 模式发起。对确认对话框而言这改变了两个子状态：`done` 报告确认现在会打开拒绝理由输入框而非立即拒绝，参数编辑中会退回对话框而非拒绝整个确认；普通确认仍然立即拒绝。
- 错误卡片末尾现在附带指向错误面板的暗色提示（跟随配置的 `error_panel` 快捷键），让结构化的 provider、模型、掩码 key、状态码与重试详情可被发现。
- 欢迎页提示现在与当前模式匹配且不再猜测客户端操作系统：Insert 模式（启动默认）显示发送/进入 Normal 模式/附件提示，而不是会直接输入字符的 Normal 模式按键；文本粘贴提示改为交由终端自身的粘贴快捷键。
- 主 agent 的 usage 事件现在携带 prompt-cache 归因诊断：每条 `chat` 记录包含相对上一次发往同一 provider/model 请求的预期可缓存 tokens、最早前缀分歧位置，以及该变化是追加还是原位改写。将 `cache_expected_tokens` 与 provider 上报的 cache read 对比即可区分客户端前缀改写和 provider 侧缓存丢失。
- fallback 路由现在具备缓存感知：服务同一模型的可互换 provider 按有效输入单价排序——名义单价按滚动的、输入量加权的实测缓存命中率折算，最近十分钟内用过的 provider 享受热缓存保底。不同模型之间的 fallback 顺序仍严格保持配置原样。
- 上下文剪裁不再以牺牲 provider prompt cache 为代价：稳定剪裁面跨 turn 存活（以 shape 兼容性而非 turn 标识校验），会改写已发送前缀字节的剪裁被延迟并批量应用，只在缓存本来就冷（模型切换、上下文高压）或累计节省能在短期内摊销一次性重计费时统一放行。新增尾部内容仍然立即剪裁，被延迟的项在剪裁统计中以 `deferred_for_cache` 上报。
- `grep` 现在用并行 worker（按 CPU 数量封顶）扫描候选文件，同时保持遍历顺序、预算与输出和串行扫描逐字节一致；中等规模仓库的全树搜索提速 4–6 倍，不匹配的行不再逐行分配内存，取消工具调用也会立即停止遍历。
- 为 LLM 请求准备长对话时不再每次都对整个稳定 reduction surface 重新哈希与深拷贝：未变化的前缀通过字段相等性检测直接复用既有 shape，在 1000 个工具结果规模下稳态准备成本从约 8.3ms/14.8MB 降至 1.4ms/3.5MB。
- `web_fetch` 权限规则现在可以按网络语义匹配请求 URL：pattern 可以指定 host（域名 glob、字面 IP 或 CIDR）及可选的端口或端口区间，例如用 `169.254.0.0/16: deny` 拦截云元数据端点。匹配仍在请求发出前基于模型提供的 URL 进行，默认全部允许的行为不变。
- 新增顶层 `orchestration` 配置，治理进程内多 agent 资源：live 与 borrowed SubAgent runtime 槽位，全局、按 provider、按 model 的并发 LLM 请求上限，以及按执行粒度的 workspace lease——同资源的工具执行（同一文件，或 shell 对 shell）跨 agent 串行，无关工具保持并行。SubAgent 输入/上下文队列与协调 mailbox 现在有消息数和字节预算；mailbox 溢出会 spool 到持久日志并按 FIFO 顺序回灌，SubAgent 也会在 `subagent_compact_usage`（默认与 MainAgent 压缩阈值对齐）附近主动压缩上下文。
- 委派任务的 `expected_write_scope` 现在会在工具执行阶段实际生效，不再只是提示元数据：只读任务拒绝工作区修改，任何 scoped task 都拒绝无法验证副作用的 Shell，文件/路径 scope 会基于规范化后的真实路径约束原生编辑工具，nested delegation 不能扩大 parent scope；child limit、重复任务、scope 冲突、runtime 注册与 parked task 重激活也由同一 admission 生命周期门控。
- 进入静止状态的 SubAgent 现在会释放事件循环 goroutine、LLM client、context manager 及其他热运行时资源，同时保留 durable task descriptor 与 transcript。聚焦、历史查看和跟进能力不变；用户显式输入、获授权的定向通知或相关 descendant mailbox 事件会按需 rehydrate 新的 runtime 实例。failed / cancelled 任务必须由用户显式重启，模型通知不能自行复活它们。
- TUI 聚焦已回收的 SubAgent 时仍会显示其任务专属 skills，与聚焦 live worker 的行为一致。
- Headless 现在会为委派工作流输出可靠的结构化 `agent_started`、`agent_notify` 以及补充元数据后的 `agent_done` 事件；SubAgent 的 `assistant_message` payload 也会携带 task、agent 类型与 parent agent 标识，方便下游标注和路由。
- 流式 `write`、`edit` 与 `patch` 卡片现在会在 `path` 字段完整到达后立即显示路径，参数继续接收时保留已接收字符数，参数流结束后再切换到完整内容或 diff 预览。
- 现在可通过 `/rename <标题>` 为会话设置自定义显示标题，执行 `/rename` 可清空标题。标题会显示在会话选择器和终端标题中，不会改变不可变的 session ID 或磁盘目录。
- `delegate` 权限 pattern 现在会在可用目标、prompt、实际执行、嵌套委派及 hook 修改后的参数中一致匹配 `agent_type`。
- 文档站构建栈升级到 Astro 7 与 Starlight 0.41，并更新 Vite、devalue、js-yaml、yaml 等传递依赖；旧 Astro content loader 产生的重复文档 ID 告警不再出现，生产依赖审计也不再报告已知漏洞。
- OpenAI Responses 与 Chat Completions 请求现在默认启用 `parallel_tool_calls`，允许模型在一次响应中返回相互独立的工具调用；如果后端或工作流要求串行调用，可在 model 或 variant 配置中显式关闭。
- 上下文剪裁默认值基于近期会话统计重新调参：较旧的 read-like 和成功 shell 输出会在 1 个 effective turn 后摘要化，read-like 与 shell-success byte gate 默认 3000，generic stale 清理 3 个 effective turns 后启动，`min_tool_results_prune` 默认 6，`min_incremental_saved_tokens` 默认 2048。较旧的成功 shell 输出现在会保留输出大小、行数、有代表性的成功信号行和尾部 fallback，而不是压成固定省略标记。
- `preset: codex` 的 Responses 请求不再维护 Chord 本地的 reasoning-effort 白名单。Chord 现在会规范化后直接透传 `reasoning.effort`，让 `max` 等模型特定取值由上游后端校验，而不会在客户端被静默丢弃。
- 初始化向导现在会配置 GPT-5.6 Sol/Terra/Luna，并以 `gpt-5.6-sol` 作为 Codex OAuth 初始模型；模型配置速查也补充了 GPT-5.6 和其他常见 provider。

### 修复

- OpenAI Responses 传输现在始终请求加密 reasoning 状态，并持久化完整、有序的原生 output 序列——包括 reasoning、message（含 phase）和 function call——stateless 回放时不再按类型重新分组。存储型请求保留 item ID，默认 `store: false` 路径则为兼容 relay 而省略 ID。Reasoning 模型因此能跨工具调用轮次延续状态；一个 Agent 基准任务的输出从约 330K 降至与 Codex 相当的约 36K token。
- Anthropic `thinking` 与 `redacted_thinking` block 现在都会在 MainAgent 和 SubAgent 工具循环中原样保存及回放。此前 SubAgent 连普通 signed thinking 都会遗漏，而流解析也会丢弃 safety 加密 block，任一种情况都可能使下一次工具结果请求失效。
- Gemini `thoughtSignature` 现在保存在原始 thought、text 或 function-call part 上，不再压到合并文本。Gemini 3 的活跃工具轨迹若缺失签名或签名因跨模型而被剥离，会在每个 model step 的第一个 function call 上加入 Google 文档规定的 validator-skip 哨兵，从而避免 fallback 期间的 400，同时不回放不兼容的加密签名。
- DeepSeek、GLM、受支持的 Qwen 与 Kimi Chat Completions 目标现在只有在 provider provenance 匹配时才回放原生可见 reasoning。Kimi K2.6/K2.7→K3 这类官方支持的同 provider 升级会保留连续性；跨 provider fallback 则丢弃不兼容的 reasoning / 工具轨迹并重新规划，而不是把另一 provider 的思考链或不完整的 thinking-mode 工具轮次发给目标。模型配置文档也补充了 Qwen `preserve_thinking` 和 Kimi 各版本差异。
- Toast 通知现在有界且去重：同类 OAuth 账号错误反复出现时更新同一条 toast 而不是不断堆叠，队列最多保留 10 条并优先丢弃严重级别最低的一条，error/warn 的展示时长缩短为 5s/4s。
- 修复搜索根目录的 `.gitignore` 用类似 `.*` 的模式隐藏点文件时，`grep`（以及其他基于 gitignore 的遍历）静默跳过整棵树的问题：该模式会匹配根目录自身对应的字面 `.`。忽略规则现在只作用于树内条目，与 git 语义一致。
- 修复恢复会话时引用已被删除的 model pool（表现为 `(missing)` 状态）的问题：恢复阶段会把过期的当前池与各 agent 覆盖项改写为该 agent 的第一个已配置池，使实际生效、界面显示与持久化状态保持一致。
- OAuth token 刷新与 compaction 响应体现在通过大小上限读取，作为 OOM 兜底，与其他 provider 读取一致。
- TUI diff 卡片的未变更上下文行现在也做语法高亮，不再渲染为纯暗色文本。
- 修复 SubAgent transcript 持久化失败仅记录一次临时日志、后续仍可能被回收的问题。持久化健康状态现在随 durable task 保存，degraded worker 会在 park 或 shutdown 前 checkpoint 完整 transcript；废弃的 LLM client 也会取消其拥有的后台任务。并发 admission 会基于最新 task registry 合并，并在最终 durable commit point 前持续遵守 caller cancellation。
- 修复 provider 返回过量并行工具调用时可能耗尽主循环 follow-up reserve，或 escalation 被已满的外部事件队列反向阻塞的问题。Loop-owned 因果事件现在保持非阻塞和有序，超大响应会在明确的单响应调用上限处正常失败；multipart 文本与附件 payload 也会计入上下文恢复的 token 预算。
- 修复 SubAgent 上下文超限在模型 fallback 后直接永久失败的问题。严格分类的 context-length error 现在会触发一次有界的本地 transcript 恢复并自动重试，同时保留 task identity 与近期高价值上下文；若第二次仍超限则正常终止，不会进入恢复循环。
- 修复 SubAgent mailbox 消息或 ack 在 append-only 日志真正落盘前就已显示、路由或标记 consumed 的问题。非 progress 消息现在先持久化再更新 task/UI，写入失败时以内存 pending 形式保留重试；consumed/reply 状态也只有在 ack 日志写入成功后才推进。
- 修复并发 Delegate 跨越 `/new`、`/resume`、fork、plan execution、shutdown、caller 取消或 parent 完成后，仍可能把 worker 注册到错误生命周期轨道的问题。Session transition 现在会暂停 admission 并使旧 epoch 启动的初始化失效；所有切换返回路径都会可靠恢复 admission。
- 修复长时间委派 session 中 event overflow、terminal task registry 与已消费 mailbox 历史可能无界增长的问题。事件突发现在使用有界背压并安全合并 progress；durable terminal task 与 mailbox 日志也会压缩，同时保留可恢复或尚未消费的状态。
- 修复 failed / cancelled parent task 仍留下 joined descendants 运行，或恢复后挂在 terminal owner 下的问题。Joined descendants 现在会递归取消，独立 child 会重新归属 main，恢复前也会修复不一致的 task tree。
- 修复 `grep` 等紧凑 TUI 工具卡按逻辑换行数控制折叠高度的问题。长匹配行软换行后的屏幕行现在会计入 10 行预览上限，展开卡片后仍可查看完整结果。
- 修复 MainAgent 与 SubAgent 并发流式输出时，一条 assistant 回复可能被拆成多张可见卡片的问题。正文、thinking 状态、rollback 和工具调用边界现在按 Agent 隔离，后台不可见的工具事件不会再提前结束前台回复。
- 修复委托后的全局空闲状态判断：MainAgent 停止而 SubAgent 仍处于连接、重试、流式或执行状态时，终端标题不会提前显示完成标记；只有所有 Agent 都空闲后才显示完成状态。
- 修复切换到大型 SubAgent transcript 时 TUI 可能因一次性重建并输出完整历史而失去响应的问题。焦点切换现在复用有界 transcript 窗口，首屏只加载尾部内容，小型 transcript 仍完整显示。
- 修复恢复会话后聚焦 parked SubAgent 时右侧 MODEL 信息为空的问题。模型信息现在持久化到任务、meta 和 recovery snapshot；旧会话优先从 usage ledger 恢复，缺失时按最新 Agent 配置和当前模型回退解析。
- 修复在 parked SubAgent 视图按 Enter 继续时 TUI 卡住的问题。上下文读取、SubAgent rehydrate 和继续请求不再同步执行在 TUI 更新路径中。
- 修复 rehydrate 后 info panel 查询已调用 skills 时的无限递归：MainAgent 的 focused skill 路由不再被 SubAgent 反向调用。Skill discovery 现在作为工作区级 catalog 共享，但每个 Agent 按自己的最新权限动态过滤可见项，并独立记录、恢复 invoked 状态。
- 进程 stderr 现在直接绑定到当前 rotating log 文件，不再经 Go pipe 回灌 golog；runtime panic/stack overflow 可以完整落盘并正常退出，不会因 fatal 阶段无人消费 pipe 而把崩溃表现成永久卡死。
- 恢复 SubAgent 时现在以 durable task 为 canonical 单位；同一 task 的历史 runtime instance 只参与 transcript 合并，不再作为多个独立 sidebar Agent 重复显示或参与焦点路由。
- 忙碌 SubAgent 的排队输入现在会等事件循环实际取走已完成的 LLM 响应后才放行，避免新 turn 抢先创建、把有效响应误判为 stale 并丢弃。
- SubAgent mailbox ack 状态现在每个 session 只加载一次，并随 ack 写入同步更新，不再为每个实时 mailbox 事件重读完整 JSONL 日志。
- `response_header_timeout` 现在会约束收到响应前的完整阶段，包括连接建立和请求体上传；收到响应头后计时器仍会停止，因此不会变成健康流的总请求超时。
- 恢复会话时现在会把每个 SubAgent 还原为轻量 parked task，并只加载 mailbox，不会投递 mailbox，也不会创建请求、LLM client 或事件循环 goroutine；恢复的 mailbox 只有在用户明确继续或向所属 MainAgent/SubAgent 提交输入时才会投递，正常运行期间产生的实时 mailbox 仍会正常投递并唤醒所属任务。completed、failed、cancelled 任务会保留原 transcript 与稳定 task ID，仍可聚焦并手动继续；rehydrate 会创建新的 runtime agent ID，并报告 previous ID。composer 仍保持可用，请求进度和运行模型状态则按精确 runtime 身份记录，因此包括名称以 `main-` 开头的后台 agent 在内，都不会再让当前 agent 错误显示为忙碌。
- `Shift+Tab` 现在始终保留可切换到 main agent 视图的路径。停止但未完成的 SubAgent 会继续留在视图切换序列中；即使当前焦点指向已失效的 SubAgent，也会回到 main，而不会卡在无法操作的视图。
- 使用 `Esc` 取消 turn 时，如果保留的中断回复高于 viewport，现在会确保该 turn 的用户消息仍然可见；恢复以同类中断 turn 结尾的会话时也会从用户消息处打开，而不是定位到部分回复末尾，正常完成的回复仍保持原有的尾部跟随行为。
- 当前查看的 SubAgent 在流式接收工具参数时，状态栏现在会正常显示已接收的响应字节数和事件数，不再一直停在 `0 B`。
- 恢复后的 idle SubAgent 现在可在聚焦后沿现有上下文继续执行，用户行为与 idle MainAgent 一致。Chord 会重新获取 SubAgent 并发槽、恢复 running 状态并创建新 turn，不会追加虚构的用户消息。
- TUI 卡片编号现在按当前查看的 Agent 独立计算：main transcript 和每个 SubAgent transcript 都分别从 `#1` 开始。切换 Agent 视图时会重建当前可用的完整历史，包括被 rehydrate 的委派任务较早实例，因此可通过向上翻页、跳到顶部、搜索和消息目录访问旧卡片，不会再只剩实时尾部。
- SubAgent 现在会把已提升的推测性工具结果直接加入自身事件循环队列，不再同步填满固定容量的结果通道；超过 8 个并行调用的批次不会锁死调度器，结果保持批次顺序，也不会为每个结果创建一个阻塞 goroutine。
- 当前查看的子代理完成后，TUI 现在会自动回到主代理会话，并把完成摘要合并到对应的 Delegate 卡；后台完成的子代理不会抢走用户正在查看的视角。
- 恢复会话时，交互式 `!` 命令现在会重新显示为 `TERMINAL` 卡片，而不是普通用户消息，并兼容可识别的旧版会话记录。发送给模型的上下文只记录一次实际执行的 `command` 及其 `output`，不再重复原始 `!command` 输入或大段粘贴占位符。
- TUI `USAGE` 区的 `Bytes` 剪裁百分比现在始终比较当前请求的未剪裁上下文与实际发送上下文；增量剪裁中冻结复用的旧摘要也会计入当前请求节省量，不再只显示本轮新增剪裁所占比例。
- 重试诊断现在会让持久日志避免记录 API key 前缀，仅保存后缀和稳定的单向指纹；内存中的 TUI 错误面板则独立生成便于人工识别的打码标识。
- 会话搜索现在会在各类卡片中校验真实可见的渲染文本，自动展开折叠匹配，支持 Markdown 格式与 HTML entity，并避免匹配隐藏或已截断的文本。
- `done` 工具提示现在明确把 assistant 正文作为普通任务完成后的强制默认出口。只有当前 runtime 或工作流明确要求工具化完成信号（例如申请 loop 退出）时，模型才能调用 `done`；其 report schema 也不再暗示每个已完成任务都必须使用该工具。
- 新建会话目录及内部持久化产物现在默认仅允许当前用户访问（目录 `0700`、文件 `0600`），避免 transcript、snapshot、SubAgent mailbox、artifact、task 状态和压缩历史默认暴露给同机其他用户。
- TUI 文本粘贴不再在 Bubble Tea update 热路径中探测剪贴板图片：普通终端 paste 与 `Cmd+V` 只粘贴文本，`Ctrl+V` / `Alt+V` 则通过原生剪贴板后端异步附加图片或 PDF，且不再依赖 `osascript`，从而避免输入框偶发卡住数秒。剪贴板 PNG/JPEG 可直接使用，BMP/WebP 会归一化处理，以兼容 Windows、WSLg 与 Linux。
- OpenAI Responses 与 Chat Completions 的 usage 现在会把 GPT-5.6 cache-write token 记录到独立桶中，而不会再把它重复计入普通输入，从而保证上下文阈值和费用统计准确。
- MCP/LSP 集成状态、控制、system prompt 和模型工具定义现在会一致遵守当前角色及实时 `/rules` 权限，包括同一 MCP server 内的部分授权、lazy/manual server 的精确 allow 规则，以及名称相互重叠的 MCP server。

## 0.7.1 - 2026-07-08

### 重大变更

- **模型输入 modality 默认值：** `modalities.input` 未设置时现在默认仅支持文本，而不再是之前的 `[text, image]`。支持图像或 PDF 的模型现在必须显式声明 `modalities.input: [text, image]`（支持 PDF 再加 `pdf`）；依赖隐式 image 默认值的配置会在发送前丢弃图像附件。请更新此前省略了 `modalities.input` 的 vision 模型条目。
- **Reasoning 连续性默认行为：** OpenAI-compatible 的 `type: chat-completions` 模型不再隐式回放 assistant `reasoning_content`。只有当 endpoint 明确要求可见 reasoning 回放时（例如 GLM Preserved Thinking），才配置 `compat.reasoning_continuity.mode: openai_visible`。`type: responses` 目标也不再把历史 reasoning 转成可见 `output_text`；这类目标继续只依赖 Responses 原生的连续性机制。

### 新功能

- 新增 Azure OpenAI Responses provider 的 `preset: azure`，包括 Azure `api-key` 鉴权、兼容 Azure Responses 的请求头 / 默认 `store: true`、official API 400 处理、初始化模板支持，以及 `/openai/v1/responses` 配置示例文档。Provider 类型自动检测现在也会按 URL path 后缀判断并忽略 query / fragment，因此带 `?api-version=...` 的 endpoint 能被正确识别。
- 新增 provider 级 `auth_scheme`，让兼容 endpoint 可以把凭据 header 与请求传输类型分开覆盖；支持 `anthropic-api-key`、`bearer` 和 `api-key`。

### 改进

- 改进长 agent 循环中的 prompt cache 稳定性：动态环境信息现在放入 session-context reminder 而不是 system prompt；请求级增量剪裁会冻结已剪裁前缀；Anthropic 显式 cache breakpoint 可以落在冻结的已剪裁前缀边界上。
- Thinking 翻译现在会更严格校验模型输出，拒绝纯符号或过度压缩的译文；并改为在 assistant thinking 持久化后再翻译，而不是流式过程中翻译，避免 rollback / retry 路径留下过期翻译。
- TUI 流式渲染现在减少 assistant / thinking 增量的逐 token 缓存失效，降低长流式响应期间的重绘开销。
- 恢复会话选择器在会话很多的项目中打开更快：首屏先渲染轻量列表并复用已缓存的会话摘要，精确消息数和缺失预览会在后台补齐。
- 文件工具现在提供更完整的 not-found 路径建议，包括对常见模型生成路径的空白修复提示，并把同一套建议流程覆盖到 `read`、`view_image`、`edit` 和 `patch`；`patch` 仍会保留使用 `write` 创建文件的提示。
- 原生文件工具现在会在同步 text document 前向匹配的 LSP 服务通知 workspace 文件 Created/Changed/Deleted 事件，让 Pyright、TypeScript、gopls、rust-analyzer 等服务更及时刷新项目 / 模块图，降低工具后诊断对新建文件产生暂态 unresolved import 的概率。
- 工具路径默认值现在显式锚定到 session working directory，而不是隐式依赖进程 cwd：相对文件路径、省略的 `shell` / `spawn` workdir、以及省略的 `grep` / `glob` 搜索根都会使用与首条用户消息前和上下文压缩后注入给模型的相同路径基准。工具卡片可以显示相对该基准的路径以提高可读性，但原始 tool-call 参数仍保持不变，便于审计和导出。
- TUI 文件引用现在支持 `@path:42` 和 `@path:10-20` 这类 1-based 行号后缀，只注入文本文件中请求的行；接受补全时也会保留已输入的行号后缀。

### 修复

- 工具结果截断现在会保留可恢复路径：`question` 回答不再在发送给模型前被截断；单行超长截断会把完整工具输出保存到当前会话的 `tool-outputs/` 目录；`read` 保持行分页作为聚焦的代码阅读路径；LSP 工具的位置输入和返回位置现在使用同一套 Unicode 字符计数；`read`、`glob` 和 `web_fetch` 等内部按预算裁剪且能够生成完整结果的工具，现在会在结果中提示完整输出保存位置。
- 自动压缩现在有 usage 缺失兜底：在收到可信的非零 provider usage 后，如果后续响应缺少 usage 或返回 0，Chord 会按当前会进入上下文的消息 bytes 相对校准样本的比例估算输入 token，使长会话仍能在撞到 provider 上限前压缩。如果实际尝试过的所有候选模型都返回 `context_length_exceeded` 且自动压缩已关闭，Chord 现在会停止并给出可操作错误，而不是退回到泛化的 fallback exhausted。
- 压缩摘要现在会剥离开头孤立的 `</think>` 闭合标签（provider 把 reasoning 走独立通道、但闭合标签泄漏到可见内容时产生），而不是触发摘要 repair 重试；开头未闭合的 `<think>` 或正文中间的内联标签等语义不明的情况仍会被拒绝并 repair。
- 恢复会话或跨 provider 回放历史时，现在会跳过空的或不可回放的 reasoning-only assistant 消息，避免旧 reasoning / thinking 内容导致 provider API 拒绝请求。
- TUI 流式输出现在会在工具调用卡片出现前先 flush 已缓冲的 thinking 增量，避免 provider 交错发送 thinking 与 tool-use 事件时生成多余的 thinking 卡片。
- Patch 工具现在兼容模型常见的 `@@` 纯锚点写法：当一个 hunk 只含未修改的上下文行、而整个 patch 至少有一处 `+`/`-` 修改时，该 hunk 被接受为 no-op 锚点——它匹配当前文件并推进后续 hunk 的搜索位置，但不修改文件。兼容提示只会追加到模型可见的工具结果上下文（不展示到 TUI），让模型以更低的失败重试成本学到推荐的单 hunk 写法。工具描述与错误信息现在明确区分上下文 marker 空格与源码缩进，并给出纯插入示例。整 patch 只含上下文行时仍会被拒绝，并给出可操作信息。
- Patch 工具现在会在严格解析失败后恢复模型生成的畸形尾部 `*** End Patch...` footer，同时保留合法的 hunk 上下文，并继续拒绝其他顶层 `*** ...` 操作。面向模型的格式提示也更短，强调单文件 direct `@@` hunks，而不是 Codex `apply_patch` envelope。
- 压缩、hook、subagent prompt 和 skill 列表使用的文本截断路径现在会保留 UTF-8 rune 边界，避免中文、日文或 emoji 内容被截断时破坏历史文件或模型可见文本。
- fallback 或回放目标无法接收 image/PDF 时，Chord 现在会在发送前丢弃不支持的二进制 part，而不是让整个请求失败或把目标模型无法处理的附件继续转发出去。

## 0.7.0 - 2026-06-28

### 重大变更

- **编辑工具名称与格式：** 原先以 `edit` 暴露的 patch hunk 编辑器现在改名为 `patch`；`edit` 现在使用 `old_string`/`new_string` 替换格式。请更新引用旧 `edit` patch hunk 格式的权限规则、hook 过滤器、skill `allowed_tools` 和外部集成。
- **编辑工具权限回退：** `edit` 与 `patch` 现在共享同一个编辑工具族。当另一个格式没有同名显式规则时，一个格式的规则会作用到另一个格式，包括 `deny`。如果需要禁用某个面向模型的格式但保留另一个，请同时配置两个名字。
- **Anthropic transport 配置：** `providers.<name>.compat.anthropic_transport` 不再读取。请从现有配置中移除该设置；Anthropic Messages 请求会始终使用下文说明的 Claude Code 风格传输提示。
- **导入 CLI：** `chord import --tool-mode` 已移除，因为可识别的外部工具调用现在总会转换为结构化 Chord 工具卡。
- **Codex OAuth 运行时状态：** Codex OAuth 运行时缓存现在使用 `auth.state.json`，不再使用已发布版本中的 `auth.state.yaml`。已有 quota/reset/账号状态缓存可通过 warm-up/轮询重新生成，但 YAML 缓存不会自动迁移。

### 亮点

- 按模型族暴露更贴近训练背景的编辑工具：GPT/o 系列使用 `patch` 的 `@@` hunk，Claude/Qwen/DeepSeek 风格模型使用 `edit` 的 old/new 替换。
- Responses、Anthropic Messages、Codex OAuth、流式超时、错误分类和 fallback 状态展示等 provider 传输与重试行为整体对齐。
- 请求级上下文剪裁更安全、更有用：更强保护近期高风险输出，并为较旧工具结果生成更好的类型化摘要。
- TUI 渲染、状态展示、错误诊断、变更文件统计、fallback 模型显示和宽终端卡片布局获得多项可靠性与性能打磨。
- 工具链更稳健：`read` 返回原始内容，`grep`/`glob` 支持多根搜索，`patch` 更快且失败诊断更清晰，`question` 容忍单对象输入，图片工具结果会附着在工具卡上。

### 改进

- Responses API 请求现在对每个 `type: responses` provider 都使用同一套 Responses 请求形态，显式发送 `tool_choice`、`parallel_tool_calls`、`store`、`stream`、`include` 数组，并在有 session id 时发送 Codex 兼容的 `client_metadata`。`store` 仍默认 `false`，但显式 provider / model 配置现在会生效。只有请求里带 reasoning 块时才会请求 encrypted reasoning content。会校验这套请求形态的中转站不再返回 `invalid codex request` 拒绝请求。
- Anthropic Messages 请求现在始终发送 Claude Code 风格的客户端提示，包括 `x-app: cli`、默认 Claude Code beta feature 列表，以及用于缓存 / 路由亲和的 JSON 格式 `metadata.user_id`。这些传输细节像 Responses / Codex 请求形态一样隐式启用；旧的 provider 配置项 `compat.anthropic_transport` 不再读取，也不再需要。升级提示：该配置项在已发布版本中（包括 `v0.6.3`）已经存在，因此升级时应从现有配置中移除 `providers.<name>.compat.anthropic_transport`。provider 级 `user_agent` 仍可配置，便于需要特定客户端 / 版本字符串的网关使用。其中 `context-1m-2025-08-07` 是例外：由于官方 API 会真实执行它（受权限门槛限制、超过 200K 切换长上下文计价、对不支持的模型直接报错），Chord 只在模型声明窗口达到 1M token 时才注入（优先 `limit.input`，否则 `limit.context` >= 1000000），与 Claude Code 仅对 1M 能力模型发送的做法一致。
- 推理块现在在 `effort` 或 `summary` 任一配置时即会发送（此前仅 `effort` 触发），修复了仅配 summary 的推理配置被静默丢弃的 bug。
- 非官方 Codex 的 Responses 兼容 provider 现在会在规范化后透传 `reasoning.effort`，允许 GLM 等 provider 使用 `max`、`minimal`、`none` 等自定义取值；官方 Codex 后端仍保留受限取值集合。
- Responses HTTP 请求现在都会在授权头之外发送同一套 SSE 请求头（`originator`、`Accept`、`OpenAI-Beta: responses=experimental`），不再取决于是否配置了 `preset: codex`；User-Agent 继续默认使用 `chord/<version>`，并尊重 provider 级 `user_agent` 覆盖。
- WebSocket Responses 传输现在从请求体传播 `include` 数组，而非硬编码空数组。
- Provider 级超时配置现在支持通过 `response_header_timeout`、`stream_idle_timeout` 和 `websocket_handshake_timeout` 分别覆盖单个 provider 的初始 HTTP 响应头超时、流式空闲超时和 Responses WebSocket 握手超时。
- JSON 热路径处理更快，包括 LLM 流解析、MCP JSON-RPC 编解码、会话导入 JSONL 解析和 `auth.state.json` 加载。
- 本地文件工具读取已有文本文件时现在优先使用 UTF-8 或带 BOM 的 Unicode，并保留对 GB18030、Big5、Shift-JIS 等常见地区性编码的受限支持。无法明确识别或不受支持的编码仍会快速失败；`web_fetch` 仍会按 HTTP 响应声明的 charset 解码。
- `read` 现在返回不带行号 gutter、也不额外缩进的原始文件文本，复制片段用于 patch hunk 或缩进敏感格式时更安全。
- `read` 现在使用更精简的 `READ_RESULT lines=a-b total=N` header；只有实际丢弃请求行时才报告 `truncated=budget`，不再为 UTF-8 文件显示 encoding 噪音，并会在 `offset` 严格超过 EOF 时直接报错而不是静默夹到文件末尾。
- `grep` 现在接受 `paths` 与 `includes` 数组，用于多根目录搜索和路径 glob 过滤；`glob` 现在接受 `patterns` 数组；当 `grep` 正则表达式无效时，会自动降级为字面量文本搜索并在结果中明确提示。`glob` 权限检查也会评估每一个请求的 pattern，避免后续 deny/ask 规则被前面已允许的 pattern 绕过。
- `grep` 在部分搜索路径失败时，现在会把每个失败路径作为结果备注返回部分结果，而不是整个调用失败；只有所有请求路径都失败时才报错。
- `chord import` 现在会在参数能标准化时始终把可识别的外部工具调用转换为结构化 Chord 工具卡。此前发布过的 `--tool-mode` flag 已移除，因为它不再改变导入行为。
- `edit` 和 `patch` 现在不再要求先通过 `read` 或系统解析的 `@file` mention 观察文件再修改文件；工具提示仍会在精确文本或 hunk 上下文尚未验证时建议先检查目标区域。已观察过的文件仍会作为 snapshot 跟踪，因此外部变更仍会触发风险提示并在必要时创建备份。
- 当 patch hunk 应用失败但与文件中的某一长行只存在很小差异时，现在会指出最接近的文件行号和首个差异列，便于恢复过期的单行 prompt、URL 或文档字符串。
- 当 patch hunk 的旧行无法在文件中构成连续块时，错误信息现在会解释原因：文件中还存在多少 hunk 行，或最长相邻匹配段及其起始行号；如果文件在上次读取后在磁盘上发生过变化，错误中还会提示 hunk 可能基于过期内容。
- TUI 内容查看器现在会在全量复制快捷键下复制原始查看内容；失败的 `patch` 工具卡片复制会在可见卡片内容被裁剪时使用完整 raw patch；恢复 inline 图片/PDF 附件时，输入框会使用文件名标签显示附件，但不会向模型消息额外添加重复文本 part。
- TUI 助手卡片现在在宽视口下会以文本内容宽度为背景终点，不再沿卡片宽度拉伸背景填充。
- 自然语言文本（用户/助手消息、thinking、状态卡片）在宽终端上的换行上限现在放宽到 160 列；代码块、diff 和工具卡片仍保持适合等宽对齐内容的 120 列上限。
- TUI 调色板对比度提升，卡片表面灰阶步长加宽、次要前景色调整，工具卡与助手消息区分更清晰。
- 流式渲染在长模型响应中效率更高，文本出现更流畅。
- 恢复会话选择器现在会显示对齐的 `Msgs` 消息数列，便于在打开前区分大小不同的会话。
- TUI 现在提供 `Ctrl+E` 错误面板，记录当前对话的中间重试错误和最终错误，并在 `/new` 开始全新对话时清空；可查看 provider、model、打码后的 `key=...` 标识、HTTP 状态码，以及可用时的结构化 API code/type 字段。
- 权限确认的规则建议现在会为复合 Shell 命令列出命中的 ask 规则，规则选择器也会预先勾选每一条命中的 ask 规则，让一次批准即可保存所有阻塞规则。
- `question` 现在容忍用单个问题对象代替文档中要求的 `questions` 数组，与 `grep`、`glob` 的标量转单元素列表容错保持一致。
- 请求级上下文剪裁现在会保护近期高风险工具输出，例如 diff、失败断言、stack trace、权限/安全错误，避免它们仅因很长的单轮工具链推进了 effective age 就被剪裁。默认 `context.reduction.read_like_age_turns` 也基于近期会话统计从 1 上调到 2，让刚读取的文件上下文以较低的观测 token 成本多保留一个 effective turn。
- 上下文剪裁现在不会再把未剪裁的 prompt-cache surface 当作保护路径复用：每次 main-model 请求前仍会执行正常的请求级剪裁，低压力下的稳定前缀复用也只会复用已经产生剪裁收益的前缀。
- 当所有 TODO 完成后，上下文剪裁现在会给下一次 main-model 请求一个单请求收尾宽限窗口。默认 `context.reduction.wrap_up_grace_requests: 1` 只在同一模型仍然活跃、没有排队的用户输入、上下文未处于高压、且新估算收益低于 `min_incremental_saved_tokens` 时避免低价值的最终 prompt surface 抖动；如果已有已剪裁前缀，收尾请求会复用该已剪裁前缀，而不是恢复原始工具输出。
- 上下文剪裁现在默认只保留更短的成功 shell 输出片段，减少上下文压力下保留的低信号终端输出。
- 请求级上下文剪裁现在会为较旧工具输出生成更安全、更有用的摘要：过期错误会保留关键失败 / 断言 / 鉴权行而不是只留下省略标记，泛化 stale 输出会保留头尾片段或在检测到 search/source/path-list 形态时路由到对应摘要，shell 输出会先按内容路由再决定是否走成功输出省略，搜索摘要会按文件分组，debug 剪裁统计也会包含聚合的跳过原因和可能过度剪裁信号，便于后续离线调参。
- Codex OAuth 运行时状态现在用用户在工作区内的组合键（`account_user_id`）识别账号，不再只按 account/workspace ID 区分，避免多个用户共享同一工作区时 quota/status 更新互相覆盖。只含 refresh 的凭据在首次成功刷新后会从临时 `refresh_sha256:<digest>` state key 迁移；OAuth access token 现在需要能解析出 account 与 user/account-user claim。
- Codex OAuth 运行时状态现在改用 `auth.state.json`，不再使用已发布版本中的 `auth.state.yaml`；已有运行时缓存可由 warm-up/轮询重新生成，但 YAML 文件中的 quota/reset/账号状态缓存不会自动迁移。
- `chord auth refresh <provider>` 现在可以刷新某个 provider 下所有带 refresh token 的 Codex OAuth 凭据，逐账号报告成功 / 失败 / 跳过状态，并保留 rate-limit reset 提示。
- `view_image` 等工具返回的图片现在会保留在对应的工具结果上，在 TUI 的工具结果卡片中显示为可打开的缩略图，并会对支持该能力的 API 通过 provider 原生的多模态 tool/function result 格式发送。`view_image` 只会在权限允许、有效 model pool 的第一个模型支持 image 输入且该模型不是 OpenAI Chat Completions 时可见；后续回放或 fallback 目标不支持 image/PDF 输入时，Chord 会在发送前丢弃不支持的二进制 part，而不是把目标模型无法处理的附件继续转发出去。

### 修复

- TUI 状态栏和信息面板现在会在 fallback / retry 尝试切换 provider 或模型时立即更新显示的模型，展示当前正在尝试的模型，而不是等到首个成功响应的 provider 后才更新。
- 流式响应中断恢复现在覆盖 OpenAI 兼容 Chat Completions，以及 Anthropic、Gemini 与 Responses provider：当流在已有可见助手正文后结束时，Chord 会将正文作为 interrupted 上下文保留；未完成的工具调用、thinking 和 reasoning 仍会丢弃，使下一次请求能继续正文而不会重放不安全的半截结构。
- 卸载空闲 language server 进程时，LSP 资源关闭不再把正常的 stderr 管道关闭记录成错误。
- 上下文压缩成功或跳过后，现在会在保存恢复状态前清理压缩前遗留的最近请求 token 样本，避免压缩后的 usage 缺失或请求失败时立即再次触发一次很小的自动压缩。
- 工具调用解析现在会在 Responses 兼容网关发送重复的部分 function-call 事件时保留已有的有效工具元数据；当网关延迟补充 `call_id` 时，流式工具调用回调会保持稳定 ID；从 Responses 完成输出中恢复的工具调用会发出成对回调；Anthropic/Gemini/OpenAI 兼容/Responses 中缺少 ID 或名称的异常工具调用会被丢弃，且不会发出孤立的流式开始、增量或完成回调；缺失或未知工具也会按无效调用报告，而不再误报为权限策略拒绝。
- 请求级上下文剪裁现在会在旧的 stable prefix 复用会破坏当前 tool_call/tool_result 链时跳过复用，避免产生孤儿 tool result 和严格 provider 的 400 错误。
- 重试日志和 LSP service-note 日志现在会区分可操作失败与中间 fallback / 已抑制的非操作性提示，减少正常成功流程中的误导性运行时噪声。
- 工具调用卡片 header 现在会优先展示主参数，单行摘要可利用更宽视口；括号内的次要参数会优先缩短。`grep` 的搜索路径等于当前工作目录时会隐藏，子目录会以相对工作区的路径显示。
- 流式请求重试或回滚时，现在会清理部分生成的 thinking 内容和待处理的 thinking 翻译，避免失败流之后在 TUI 或恢复的会话状态中残留过期 thinking 文本。
- 思考翻译语言检测改进：
  - 比较前规范化语言代码（如 `zh` vs `zh-Hans`、`en` vs `en-US`），避免因格式差异导致的误判
  - 从基于字母计数改为基于语义单元计数（拉丁单词 vs 汉字），使权重分配更公平
  - 当目标语言占主导地位（≥ 50%）时跳过翻译，避免因误检测而翻译用户语言实际为主要语言的混合内容
- 对于会上报 `thinking_tokens` 用量字段的 Anthropic 兼容 provider，现在会将其解析为 reasoning token 用量，并在 TUI 信息面板中以单独的 `Think` 行展示，与现有的输入/输出和缓存用量并列显示。（官方 Anthropic API 不返回该字段，thinking 计入 `output_tokens`。）
- 侧边栏文件变更追踪现在会在比较前规范化文件路径，避免同一文件以不同路径表示（如 `file.go` vs `./file.go`）时出现重复条目
- 侧边栏文件统计现在会优先完整显示 `+N -N` 行数统计，防止改动数量信息被长文件名截断
- `write` 工具操作现在会在文件变更摘要中正确追踪，包含新文件和覆盖写入的准确行数统计
- edit/patch 工具选择的模型匹配现在使用严格模式匹配，防止误判（例如 `o10` 或 `gptx` 模型错误地使用 patch 工具）
- edit 和 patch 之间的权限回退现在正确处理通配规则和显式的单格式覆盖，因此禁用 `patch` 但显式允许 `edit` 时，GPT/o 系列模型可以回退到 `edit` 而不是失去所有编辑能力
- 推测性文件变更追踪现在能同时从 patch 和 edit 工具参数中正确提取路径，修复了 ReplaceEditTool 的文件追踪问题
- 交互式命令检测现在能正确允许管道命令（如 `man git | grep`），并提供针对具体命令的非交互式替代方案，而不是给出通用的终端建议
- Patch 工具现在会对缺少具体标识符的 `@@` header 使用软锚点回退，减少 header 格式不精确导致的误失败
- Patch 工具现在会检测并拒绝只包含上下文行、没有 `+`/`-` 变更的 patch，并给出可操作的错误信息
- System prompt 与工具 schema 描述现在会根据可见工具动态适配，避免引用不可用工具
- 现在会在执行阶段强制执行 `edit` / `patch` 工具可见性，因此只看到 `edit` 的模型不能执行从早先对话历史中学到的隐藏 `patch` 调用；LSP 诊断提示也会使用当前模型实时适用的编辑工具名。
- LSP 工具可见性现在会正确要求已配置 LSP manager 实例
- `write` 现在会在写入文件内容时报告执行进度，使较长写入过程与其他本地文件变更工具的反馈更一致。
- Patch 工具性能：大文件处理性能从约 30 秒优化到毫秒级，通过延迟应用规范化回退、在 hunk 匹配已能判断唯一/歧义时立即停止、先限制昂贵诊断扫描窗口并仅在必要时回退到全量扫描，以及添加快速诊断路径实现。在 3000+ 行文件的失败场景下提供约 600 倍性能提升。
- 工具卡片（Done 报告、确认提示等）中的代码块现在会对长行进行换行并带续行缩进，而非溢出卡片边界，修复了 CSV 数据和长 shell 命令的显示问题
- Provider 错误分类现在优先使用结构化的 `code`/`type` 信号（包括错误体内嵌套的 JSON），而非纯文本匹配；对于不提供结构化字段的网关仍保留消息文本回退。未识别的 HTTP 400 现在按终态请求/参数错误处理，不再跨 key 和模型重试；而配额、上下文超限、并发限制、Codex WebSocket 链路状态不匹配以及带 `Retry-After` 的 400 仍保留各自的重试/冷却处理。
- 兼容网关返回临时性 HTTP 400 且没有 `Retry-After` 时，现在使用 1 秒的短探测冷却；纯 all-keys-cooling 等待会显示 `cooling` 而不是 `retrying`。配置了 `stream_retry_rounds` 时，all-keys-cooling 重试轮也会受该上限约束，与已记录的重试上限语义一致。
- 压缩续跑和长度恢复使用的请求级 tuning override 现在会与模型/variant 默认值合并，而不是整体替换，避免这些恢复路径之后的 OpenAI Responses 请求丢掉已配置的 `reasoning`/`text` 字段，同时保留 Anthropic/Gemini 的 thinking 与缓存默认值。
- 环境变量 `CHORD_API_BASE` 现在会作为 `--api-base` 的回退真正生效；两者同时设置时仍以 CLI flag 为准。
- Provider 的流式 HTTP client 现在不再用总请求计时器截断健康流：`response_header_timeout` 控制初始响应头等待，`stream_idle_timeout` 控制流式 chunk 之间的空闲等待。辅助非流式调用不再把响应头设置复用为总请求超时。
- 流式 assistant 卡片在内容仅为占位符（点号或省略号）时不再加入会话；真实内容到达后会替换占位符，仅含占位符的块会被丢弃而不是渲染成空卡片。
- 工具失败结果文本与错误文本相同时，现在只返回一次而不再追加重复的 `Error:` 块；证据收集与请求级上下文缩减现在也会根据结构化的工具结果状态识别工具错误，因此没有 `Error:` 前缀的此类结果仍会按错误处理。
- AGENTS.md 工作区指令现在会在 main agent 与 sub-agent 中带明确范围和可见性说明注入：Chord 会从项目根目录到当前工作目录加载适用的完整 AGENTS.md 内容，作为内部 user-role meta message 放在第一条真实用户消息之前，并以 `# AGENTS.md instructions` 为头部、用 `<INSTRUCTIONS> ... </INSTRUCTIONS>` 块包裹，使其被视为持久工作区指导，而不是可选上下文。
- Fork 编辑后的 TUI 消息现在在会话恢复与 fork 事件后仍会保留 inline 图片/PDF 附件，不会被延后执行的 transcript 重建清除。
- Gemini 工具 schema 现在会在发送 function declaration 前剥离 Chord 内部使用的 coercion 标记。
- Shell 权限回退检查现在在展示命中规则建议时仍保留复合命令复审语义，避免窄 allow 规则自动放行未解析的复合命令。
- 请求进行中挂起的 model-pool 切换现在会保留原始 pool，取消或应用切换时能恢复预期状态。
- TUI 中 cache-read 百分比现在使用输入侧 prompt tokens 加上 provider 单独上报的 cache-write tokens 作为分母。
- Anthropic 兼容网关在 `message_delta` 事件中上报 usage 时，不会再用 0 覆盖已有的非零 input token 计数。
- TUI 信息面板的 changed-files 区域现在会优先完整显示 `+N -N` 行数统计，而不是让长文件名挤掉改动数量，与较窄侧边栏的行为一致。
- TUI 信息面板现在会在鼠标指针位于其上方时响应鼠标滚轮或触摸板独立滚动，较长的 changed files 或状态区块不再被输入区截断。
- 被拒绝的折叠 Shell 工具卡片现在会把展开提示显示在拒绝原因之前，提示不再被挤到结果文本下方。
- 成功的 `edit`/`patch` 工具卡片现在会在文件编辑后仍存在 LSP 诊断时显示 `↳ Diagnostics:`，同时隐藏常规成功样板文本。
- TUI 侧边栏较窄时现在会优先保留 changed-file 的 `+N -N` 统计，而不是让长文件名挤掉改动数量。
- 编辑 forked TUI 消息后重新提交时，现在会保留 inline 图片附件，即使可见 prompt 文本已被修改。
- 自动压缩现在以 provider 返回的 usage 为权威依据：请求级本地 token 估算不再清除已经由 usage 触发的压缩请求。
- 切换 Codex OAuth key 时现在会清除过期的 inline rate-limit 快照，避免上一个已耗尽 key 让下一个 key 继续冻结请求级上下文剪裁。
- Done 确认对话框现在会用 dialog 专属 surface 渲染 Markdown 和 fenced code block，避免确认弹窗里混入助手卡片背景色。
- TUI 工具错误卡片现在不会在错误正文里重复显示开头的 `Error:` 前缀。
- TUI 中的助手 Markdown 表格现在可在大终端上使用更宽的卡片宽度，减少较宽 review 表格的纵向换行。
- TUI 消息渲染现在会在绘制卡片前转义原始控制字符，避免粘贴内容或模型输出包含 `\x01` 等字节时出现背景色异常。
- 请求进行中切换 model pool 现在会在下一次请求边界生效，不再打断正在进行的请求；状态栏与信息面板会显示下一次请求将使用的模型。
- 失败的 `patch` 工具卡片现在会先显示本次尝试的 patch，再显示错误文本，便于在阅读诊断前先检查失败 hunk。
- 向聚焦 agent 提交消息时，现在会附带 `@file` 引用的文件内容 parts，不再只发送纯文本。
- 当 provider 报告账号或 workspace 已停用时，OAuth key 现在会从选择中永久移除，包括以 HTTP 402 返回的停用错误。
- `view_image` 后恢复会话时，工具返回的图片不再显示成用户手动发送的消息。
- TUI 渲染现在会禁用终端硬滚动优化，避免 Chord 的 sticky transcript 布局中出现重复分隔线或旧边框残留。
- 在 TUI 输入框粘贴剪贴板图片时，现在不会再重复添加图片附件；删除 inline 图片占位符时也会同步移除对应附件 chip。

## 0.6.3 - 2026-06-05

### 亮点

- 多模态：跨 provider 的 PDF 输入支持，以及用于本地图片的内置 `view_image` 工具
- 上下文剪裁现在对大型工具结果使用类型化摘要，保留关键信息
- 卸载空闲的 LSP/MCP 资源以降低内存占用
- 改进 `edit` patch 容错性，TUI 工具结果显示更清晰
- `grep` 与 `glob` 现在会在调用方已知精确文件路径时跳过整棵搜索根的遍历：`grep` 的 `includes` 或 `glob` 的 `patterns` 里写普通相对文件名，会直接在搜索路径下解析并读取或 stat，不再递归遍历整个根。当某次搜索仍要扫描非常大的根（如系统临时目录、家目录、`/`）却只匹配到极少候选文件时，会提前中止并返回一个可恢复的错误，提示把完整文件路径作为搜索路径或缩小搜索范围，而不是卡住几分钟。工具描述也补充说明：路径/文件名过滤器是在遍历过程中生效的，并不能避免遍历搜索根本身。

### 新功能

- 新增跨 Gemini、Anthropic、OpenAI Responses 和 OpenAI Chat provider 的 PDF 多模态输入支持，包括 TUI 附件 chip 与会话恢复。
- 新增内置 `view_image` 工具：支持图片输入的模型可以用它把本地 PNG/JPEG 载入上下文，并复用与 `read` 相同的本地路径权限处理。

### 改进与修复

- 上下文剪裁现在会按类型摘要旧的大块工具输出：搜索结果保留查询 / 数量 / 代表命中，JSON 大块保留顶层结构和数量，构建 / 测试日志保留关键失败项，读取摘要包含行范围等元数据，而不是直接退化为通用省略。
- 面向 LLM 的工具定义现在使用 registry 按名称排序的稳定顺序，减少语义未变但工具顺序漂移导致的 prompt cache miss，同时保留现有 OpenAI `prompt_cache_key` 与 Anthropic `cache_control` 语义。
- 改进 `edit` patch 对 hunk 内空白 context 行的容忍度，减少模型生成补丁的应用失败。
- Chord 现在会在空闲数分钟后卸载 LSP 与 MCP 资源，并在下一次请求时恢复 MCP server；空闲的 LSP/MCP 行会以灰色显示，而不是显示为失败。
- `edit` 在 TUI 中更少噪音：普通成功 patch 摘要不再展开显示，但 diagnostics 仍会显示；失败的 edit 会显示本次尝试的 patch 预览，复制工具卡时仍保留完整结果。
- `edit` 成功结果现在使用项目相对路径和简洁的增删行数；失败结果会附带尝试应用的 patch 上下文，便于恢复。
- 文件工具卡现在会对常规 edit/write/delete 成功结果使用紧凑摘要，并且工具错误结果可以显示和复制，不再从卡片中丢失。
- 修复当前 auth 状态使用负数 credential-index 哨兵值时，OAuth credential 刷新可能崩溃的问题。
- `@` 文件补全现在会把当前模型支持的图片 / PDF 文件作为附件处理，隐藏当前模型不支持的媒体类型，并在输入框 / 转录区标记不支持或已加密的附件。
- 切换到不支持图片 / PDF 输入的模型时，会在 provider 请求前过滤历史中不支持的二进制 part，同时保留历史工具调用结构。
- 修复信息面板的上下文 `Bytes` 显示：新会话从 0 用户上下文开始，恢复会话后立即显示下一次请求会使用的剪裁后大小和节省比例估算。
- 取消当前 turn 时会保留已经成功完成的工具调用，因此在较慢的后续工具上按 Esc，不会再把前面已成功的工具卡改写成 `context canceled`。
- TUI 现在会在 turn 忙碌期间明确显示待生效的模型或 pool 切换，让状态栏和信息面板能区分当前正在运行的模型与已排队的切换。
- `edit` 现在会给模型更清晰的补丁写法指引，并接受更多常见的补丁上下文，减少可避免的编辑失败。
- deferred 的工具参数流式更新在节流渲染状态变化时会强制触发最终 TUI 刷新，避免隐藏或部分渲染的工具参数停留在旧内容。
- 对最后一条用户消息按 `ee` 编辑时，现在会从当前会话移除该消息并载入输入框，而不是 fork 一个新会话。

## 0.6.2 - 2026-06-02

### 亮点

- 内置工具名统一为 snake_case（`ApplyPatch` → `edit`、`WebFetch` → `web_fetch`、`TodoWrite` → `todo_write` 等）——请更新引用旧工具名的权限规则。
- 上下文剪裁更智能：在长工具链上更早裁掉陈旧输出，同时对 prompt cache 更友好。

### 重大变更

- 所有模型可见的内置工具名都改为 snake_case（例如 `ApplyPatch` → `edit`、`WebFetch` → `web_fetch`、`TodoWrite` → `todo_write`）。不提供兼容别名——请更新权限规则、hook 工具过滤器、skills 的 `allowed_tools`，以及所有使用旧 PascalCase 名的集成。会话导入仍会识别 Codex `apply_patch` 等来源工具名并映射为 `edit`。

### 改进与修复

- 上下文剪裁现在会在很长的单轮工具链上更早裁掉陈旧工具输出（年龄按整体进展计算，而不只看后续用户消息），同时 warmup 保护会避免反复裁剪低压力的 prompt 前缀，让 prompt cache 保持有效。`context.reduction` 接受 `true` 或 `{}` 表示默认调优，并暴露更细的调优项（`cache_aware_min_usage`、`warmup_message_limit`、`min_incremental_saved_tokens`、`high_pressure_usage`、`force_prune_usage`）；`context.reduction: false` 现在会报错，而不是静默当默认处理。
- 侧边栏的 reduction 节省量现在显示当前请求实际省下的 messages / bytes / tokens，并在回到 idle 后继续可见。
- `edit` 在补丁应用失败时给出更明确的提示——指出复制了行号 gutter、缩进漂移、文件内容已过期，或 function/class anchor 不匹配等常见问题。

## 0.6.1 - 2026-06-01

### 亮点

- 新的 `edit` 工具以 patch hunk 方式修改已有文件，大幅减少旧 `Edit` 的精确字符串匹配失败。
- YOLO 模式：通过 `--yolo`、`/yolo on|off` 或 `Ctrl+Y` 临时绕过权限确认。
- Git 状态侧边栏，显示分支、改动/暂存/stash 数量与 ahead/behind。
- Codex 认证与配额处理更稳健，并能从 WebSocket 状态错误中自动恢复。

### 重大变更

- **权限：** 记住的权限规则现在直接写入 agent 配置文件——project 规则写入 `<project>/.chord/agents/<role>.yaml`，global 规则写入 `<config-home>/agents/<role>.yaml`——不再使用单独的 permissions overlay。先前写在 `.chord/permissions/<role>.yaml` 的规则不再被加载，如仍需要请手动迁移。内置 planner 现在默认只允许在 `.chord/plans/*` 下执行 `write`/`edit`。
- **配置：** HTTP `User-Agent` 覆盖移到 provider 级 `user_agent`。请求默认使用 `User-Agent: chord/<version>`，除非显式覆盖。
- **配置：** 移除未使用的 `context.reduction.model_pool` 和 `maintenance.size_check_interval_hours`。上下文剪裁保持确定性、不调用模型；需要 LLM 参与的压缩请用 `context.compaction.model_pool`。
- **配置：** 移除模型字段 `supports_fast`——请迁移为 `supported_service_tiers: [fast]`（或省略以使用 preset/provider 默认值）。
- **兼容性：** 移除剩余的 pre-1.0 兼容路径——Codex 导入只接受当前 rollout schema，`--config` 不再是 `--config-home` 的别名，headless 模型切换只接受 `set_current_model_pool`。

### 新功能

- `edit` 以原生单文件 patch hunk 替代旧 `Edit` 来做局部修改（`write` 仍负责整文件写入，`delete` 负责删除整文件）。它强制先读后改，容忍 Codex `apply_patch` 的 envelope 标记（`*** Begin Patch` / `*** Update File:` / `*** End Patch`），并在一个 hunk 匹配到多处时报告候选行。若文件在你上次读取后发生变化，编辑会基于当前内容校验，并把有风险的覆盖写入备份到会话目录（按文件和会话设上限）。
- YOLO 模式（`--yolo`、`/yolo on|off`、`Ctrl+Y`）临时绕过 MainAgent 的权限确认；Handoff、Delegate、Cancel、Done 仍需批准，启用时状态栏显示 YOLO 标识。
- `/rules` 现在即使没有已存规则也能打开，可添加 session/project/global 级的 allow/ask/deny 规则；记住规则的选择器允许保存前先编辑建议的 pattern。
- Git 状态侧边栏：紧凑、可折叠的 Git 摘要（分支或 detached commit、worktree 名、改动/暂存/stash 数量、ahead/behind），异步刷新且不阻塞渲染。
- LSP：Python 诊断新增 `ruff` 快速后端；大文件会自动改用它，而不是阻塞在完整分析上。新增顶层 `diagnostics.*` 配置控制各后端命令与输出上限。
- Headless：新增 `local_shell` 命令/事件用于执行 `!` 风格本地命令，`Handoff` 会发出结构化 `handoff_request` 事件并支持 `handoff` 批准/拒绝命令。
- Service tier（`/tier`、`Ctrl+R`）现在会同步作用到 SubAgent。
- Thinking 译文按 block 持久化到会话目录，并在恢复会话时还原。
- 对话卡片、查看器和输入框之间的鼠标文本选择保持一致（双击选词、三击选行）；`yy` 把工具卡复制为结构化 Markdown。
- 会话导入会把可识别的外部工具（`read`、`shell`、`grep`、`glob`、`edit`、`write`、`delete`）转换为最接近的 Chord 工具卡。

### 改进与修复

- `@` 文件补全在非空根目录前缀查询时回退到直接匹配根目录，因此像 `AGENTS.md` 这种被 `.gitignore` 排除出 Git 索引的文件也能补全。
- `Grep` 和 `Glob` 降低了默认结果上限并新增字节上限，避免过宽的搜索挤占更相关的上下文。
- Codex：access token 必须包含可解析的 account ID，token / refresh token 的认证失败会被归类为不可恢复，不再做无意义的重复 refresh。`key_order: smart` 只把完全用尽（100%）的窗口视为耗尽——99% 仍算可用——并分开比较短窗口与周窗口。WebSocket 链状态不一致的 400 会重置链状态并以全量重发重试一次；usage-limit 错误跳过缓慢的 HTTP fallback，直接进入 key/配额处理。
- 共享同一 access token 的重复凭证 slot 现在会同步更新 cooldown、recovering、quota-exhausted 和 success 状态，避免已耗尽 token 通过另一个 slot 被再次选中。
- 兼容网关：看起来是临时态的 400（如 `Concurrency limit exceeded`）会冷却并轮换 key 后继续重试；官方 API 的请求参数类 400 仍立即停止。API 400 被当作模型级失败处理，使 client 可以尝试另一个配置的模型。
- 恢复的会话在送往 provider 前会修复结构破损的回合，没有对应工具调用的孤儿 tool result 会被丢弃。
- OAuth slot 只在真实认证失败后才标记为 expired，而非依据本地 `expires` 元数据；`auth.state.yaml` 变化时 Codex 运行时状态会自动重载（其它 Chord 进程的更新无需重启即可生效）。
- Loop 模式：`Done` 不再有验证状态门槛（未完成 TODO、活跃 subagent 仍会阻止退出）；自动与手动压缩现在都能在 loop 模式下运行，让长会话能在 context 预算耗尽后继续。`/compact` 在后台运行，可在回合进行中触发并在下一个安全节点应用。
- 信息面板的 Bytes / Messages 现在反映将真正发送给模型的内容，并带剪裁后百分比。
- LSP 诊断在编辑后会等待新结果（减少 gopls 等 server 的瞬时误报），并裁剪为简洁、按优先级排序的块。
- Plan 执行通过 `@<plan-path>` file mention 传递 plan，而不是把它内联进系统提示词。
- 确认弹窗与 `Question` 的拒绝原因会完整保留你的原文，包括换行。
- TUI 打磨：`gg`/`G` 把焦点移到第一个/最后一个卡片，恢复的工具卡保留状态和 diff，`Ctrl+T` 消息目录内联渲染并支持翻页，补全显示自定义命令 scope 与 `/tier` 一致性，overlay 移除未公开的快捷键，大会话中的滚动与焦点恢复更顺畅。
- `chord cleanup sessions` 也会删除只剩空壳的 per-project 会话目录。
- `git stash show -p`、`git stash list --patch` 等只读 stash 子命令不再被当作交互式拦截。

## 0.6.0 - 2026-05-20

### 亮点

- 请求时上下文剪裁（`context.reduction`）让长会话在需要压缩前保持精简。
- `config.yaml` 缺失时的首次启动初始化向导。
- Loop 模式以 `Done`（必须带完成报告）作为唯一退出入口。
- `chord import` 可导入 Claude Code、Codex 和 OpenCode 的会话。
- `chord worktree finish` 重做为 merge-then-squash，并提供不改动任何东西的 `--check` 预检。

### 重大变更

- **配置：** `context.compact_threshold` 重命名为 `context.compaction.threshold`，不提供兼容别名。
- **配置：** 移除 `context.auto_compact`。现在 `context.compaction.threshold > 0` 时启用自动压缩，设为 `0` 可关闭。
- **配置：** 移除 `context.compact_model`。压缩现在只接受 `context.compaction.model_pool`；未设置时会克隆当前 agent 的模型池，而非回退到单个模型。
- **Headless：** 移除对外的 `tool_result` 事件。非 loop 的 `Done` 报告改用专门的 `done_completion` 事件；loop 模式的 `Done` 退出仍用 `confirm_request`，并显式携带 `done_report` / `done_reason` 字段。

### 新功能

- `context.reduction` 下的确定性请求时上下文剪裁，含陈旧工具结果的剪裁阈值，loop 模式下保持关闭。
- 默认 `chord` 命令的首次启动初始化向导：写入最小 `config.yaml`（必要时再写 `auth.yaml`），可完成 Codex OAuth 登录，复用已匹配的现有凭据，并打印实际使用的路径。
- `Done` 现在要求带非空 `report`（完整完成报告）。loop 模式下它是唯一退出入口：过早调用会被拒回给模型，满足条件的退出会弹出展示该报告的确认框。
- `chord import` 导入 Claude Code、Codex、OpenCode 的外部会话，生成可恢复的 session 和 `import-report.json`。

### 改进与修复

- 源码构建与 release 产物现在要求 Go 1.26.3+（修补了可达标准库漏洞的 toolchain）。
- OAuth 账号状态改为存放在 `auth.state.yaml`，新增 `invalidated` 状态，并不再写入 `auth.yaml`。
- `chord worktree finish` 会先把目标分支合并进 worktree 分支以在那里暴露冲突，再把结果以单个 squash commit 合回目标分支。`--check` 在不触碰真实 worktree 或目标分支的前提下预检这次 merge；若已有进行中的 rebase 或 merge，`finish` 会拒绝启动。
- 自动输入法切换只在前台标签页/窗口运行，后台标签页不再干扰当前标签页的输入法。
- 加固了本地文件/路径安全（拒绝 device 类路径），并让 config/auth 写入变为原子操作。
- 图片粘贴会对按键和终端粘贴事件去重，一次粘贴不再插入两张图片。
- 后台确认提醒会持续闪烁终端标题直到你聚焦窗口；修复压缩后信息面板 `TOKENS` 显示陈旧值的问题。

## 0.5.3 - 2026-05-11

### 新功能

- 用 `chord doctor models` 替换 `chord test-providers`：支持精确 `provider/model[@variant]` 检查、模型池审计、all-model/all-pool 模式、按目标 timeout、JSON 输出和可选 `--retry`。
- 项目 `.chord/config.yaml` 现在在启动、auth 登录和诊断中走同一套合并逻辑；格式错误的项目配置会明确报错而非静默忽略。新增 `stream_retry_rounds` 可为自动化限制公开 LLM 重试轮数。

### 改进与修复

- 恢复的会话会重建持久化的 `Read` 文件状态，因此之后的 `Edit`/`Write` 仍保留先读后写保护，又不会误要求重读每个文件。
- 未配置 `limit.input` 时，压缩和模型池 fallback 的预算会从 `limit.context` 中预留输出额度，减少超大 prompt 重试。
- `chord doctor models` 在多目标诊断间复用刷新后的 OAuth 凭据状态，避免陈旧 token 导致误报。
- 修复 Markdown 预览语法高亮：文件末尾的有序列表、标题等行在 `Read`/`Write` 卡片和代码块中保持与前面行一致的颜色。

## 0.5.2 - 2026-05-11

### 重大变更

- 模型可见的命令执行工具从 `Bash` 重命名为 `Shell`（无运行时别名）。升级前请更新权限规则（`permission.Shell`）、hook 工具过滤器、skills 的 `allowed_tools`、已保存/导入的工具调用、headless 消费方，以及引用旧 `Bash` 名的提示词。

### 新功能

- `chord worktree finish --check`：在临时隔离 worktree 中预检一次 rebase，提前告诉你能否干净收尾，同时不改动真实 worktree、也不会把它留在半个 rebase 状态。
- `Write` 工具卡片现在用带行号、语法高亮的预览展示写入的文件，和 `Read` 卡片一致。

### 改进与修复

- 侧边栏文件列表从 `EDITED FILES` 改名为 `CHANGED FILES`；被删除文件显示为删除线，且不再有伪造的 `-1` 行数。
- 默认快捷键对齐：`Ctrl+P` 为模型选择器，消息目录移到 `Ctrl+T`，默认 `Ctrl+F` 图片附加绑定已移除（可配置 `insert_attach_file` 恢复）。
- API `402` 用量/余额错误现在按 per-key 限流处理——冷却已耗尽的 key 并在 fallback 前尝试其它 key。
- 收窄非交互 Shell/Spawn 防护：普通 `read`/`select` 的 stdin 读取可正常执行，依赖 TTY 的命令仍被拦截。
- Codex 用量轮询和 OAuth 浏览器/设备码登录会在 Ctrl+C 或关闭时及时取消。
- 减少 Ghostty/cmux 在切换标签页或 resize 恢复后的残影。

## 0.5.1 - 2026-05-09

### 新功能

- 针对 `manual: true` 的 MCP server 新增手动运行时控制：`/mcp`（`status`、`enable`、`disable`）和 MCP 选择器（`Ctrl+O`），可在运行时连接/断开按需 server。自动启动的 server 保持只读。

### 改进与修复

- 修复初始 LLM client 未使用 builder agent 完整模型池的问题，使首个请求失败时能正确跨模型 fallback，而不仅在 API key 之间轮换。
- `Write` 卡片显示清晰的行数/字节摘要，不再为整文件写入展示「只写了几行」的误导性 diff。
- Thinking 翻译在某个模型返回空结果时会尝试模型池中的下一个模型。
- `Bash` 和 `Spawn` 在执行前拒绝高置信度交互式命令并使用非交互默认环境；超时/取消会从优雅终止升级为 force-kill，避免顽固子进程导致调用悬挂。
- Codex 用量轮询在 WebSocket 流安静时主动唤醒，保持 RATE LIMIT 面板及时更新。
- 升级 Bubble Tea 渲染栈，修复 Ghostty/cmux 在焦点/resize 恢复后的残影。

## 0.5.0 - 2026-05-08

### 新功能

- `chord import` 导入 Claude Code、Codex、OpenCode 的外部会话，生成可恢复的 session 和 `import-report.json`。
- 请求前的模型兼容标准化：切换 provider/model 时安全回放或降级历史中的 provider 专用 payload（Anthropic signed thinking、结构化 tools）。

### 改进与修复

- agent 配置用 `mode: main` 表示 MainAgent、`mode: subagent` 表示 SubAgent（`sub_agent`/`sub` 也接受）；hook `agent_kind` 过滤器使用 `main`/`subagent`。
- 修复工具 batch/turn 在等待共享执行配额时被取消可能导致的卡死——现在会生成 cancelled 工具结果，界面不会卡住。
- `chord worktree finish` 在 rebase 冲突时给出分步恢复命令，并在已有进行中 rebase 时提前退出。
- 修复忙碌时通过 `/models` 切换模型池的时序，使排队消息在下次请求边界使用新池。
- 工具/Bash 的 spinner 动画每个 tick 推进一帧（不跳帧）；后台会话保持相同节奏。
- 修复 reasoning 紧接 assistant 正文时 THINKING 卡片的顺序问题。
- 后台 agent 由 busy 转 idle 时在终端标题显示一次性 `✅` 完成标记。

## 0.4.0 - 2026-05-07

### 亮点

- Git worktree 支持：`chord --worktree [name]` 创建或进入一个隔离的 worktree，拥有独立的会话、缓存和导出。
- 新增 Google Gemini 一等公民 provider。

### 新功能

- `chord --worktree [name]`（默认命令和 `chord headless` 都支持）创建或进入 chord 管理的 git worktree，按 worktree 隔离 sessions/cache/exports；可与 `--continue`/`--resume` 组合。值为空时自动命名，已挂到 worktree 的分支会被复用。
- `chord worktree list` / `remove <name>` 管理 worktree，`chord resume <session-id>` 会定位会话所在的 worktree 并恢复。
- `worktree.branch_prefix` 配置可覆盖默认的 `chord/` 分支前缀（非法 ref 在启动时被拒绝）。
- Google Gemini 一等公民 provider（`type: generate-content`）：流式文本/工具/思考输出、内联图片、function calling 工具，以及 `Retry-After` 处理。

### 改进与修复

- `session-meta.json` 新增 worktree 字段；已有会话保持兼容。
- 本地 slash 命令（`/export`、`/models`）始终在主事件循环中执行，修复在 LLM 重试中途提交时界面卡在「忙碌」的问题。
- slash 补全在超过 8 项时滚动也能保持选中命令可见；`/new` 会清空侧边栏文件列表。

## 0.3.0 - 2026-05-07

### 亮点

- 运行时模型池：把模型分组为命名池，通过 `/models` 或 TUI 选择器在运行时切换当前池。

### 重大变更

- agent 模型配置现在必须引用一个或多个顶层 `model_pools`；不再接受 per-agent 的扁平 `models` 列表。每个 agent 至少要有一个池，列表中的第一个池为默认值。

### 新功能

- 模型池与 `/models`（`status`、`<pool>`、`--agent <name> <pool>`）；忙碌时切换会在下一个请求边界应用，无需等待完全 idle。
- diagnostics 和 `chord --version` 中加入构建身份（commit、dirty 状态、build/VCS 时间、Go 版本、可执行文件 mtime）。

### 改进与修复

- SKILLS 侧边栏不再把加载失败或未知的 skill 显示为已加载，并移除旧的 "(loaded)" 后缀。
- Codex RATE LIMIT 面板不再在窗口 reset 后卡在 "1s"；会隐藏倒计时并及时刷新用量。
- 延迟的 diagnostics/export 状态卡在当前 assistant 卡片结束后立即出现，而非等到 idle。
- 修复权限确认的编辑/拒绝理由输入区无法 `Cmd+V` 粘贴的问题。

## 0.2.0 - 2026-05-05

### 重大变更

- 通过 `workspace/configuration` 提供的 LSP `options` 现在对所有 server 都必须按 section 组织（Pyright 请用嵌套的 `python` / `python.analysis` 键，而非扁平顶层键）。
- Headless：移除 `notification` envelope 类型——请改用 `idle` envelope 渲染 ready/idle 状态。
- SubAgent `Complete` 移除 `blockers_remaining` 字段；非阻塞遗留事项用 `remaining_limitations` 报告，真正的阻塞走 Escalate 或 `blocked` mailbox。

### 新功能

- Pyright LSP 自动发现项目本地 `.venv`/`venv`/`env` 解释器（类 Unix/WSL 与 Windows 布局），并把相对解释器路径按 LSP root 规范化。
- 新增会话范围的 `SaveArtifact` / `ReadArtifact` 工具，以及结构化 SubAgent 完成交接（修改文件、已运行验证、剩余限制、风险、后续建议、artifact 引用）。
- loop 的 `verify` assessment 注入专用 `LOOP VERIFY` notice 并给出明确验证指引；文档补充 `/loop on [target]`。

### 改进与修复

- 自动（阈值触发）压缩现在会在压缩后的上下文上继续推进任务，而非回到 idle；手动 `/compact` 仍返回 idle。
- 压缩后的会话列表预览/标题更准确，改用显式元数据而非从文本推断。（旧版本压缩的会话可能仍显示被污染的标题，直到重新压缩。）
- 工具卡片按终端安全的纯文本渲染（转义 ANSI、不误按 Markdown 渲染）；修复 emoji/ZWJ 字符附近的背景色异常和留白不一致。
- 修复 `ee`/fork 编辑：从会话历史路径恢复的图片会被重新读入并随消息再次发送；鼠标拖选复制会保留最后一个字符。
- 修复长会话中转录区裁剪/漂移导致最后几张卡片被隐藏或鼠标选择错位的问题。
- 本地路径工具（`Read`/`Write`/`Edit`/`Delete`/`Grep`/`Glob`）在可能时显示项目相对路径。
- 日志切换为 golog 原生纯文本输出；不再输出此前伪结构化的 `level=... msg=... key=value` 格式。
- 补充 Ghostty/cmux 在快速滚动/resize/焦点变化后的残影修复。

## 0.1.0 - 2026-04-29

- Chord 首次公开发布。
- 本地优先的终端编码 Agent，包含 Vim 风格导航、会话管理、模型/服务商配置、工具执行、LSP 集成、图片输入和 headless 远程控制。
- 提供 macOS、Linux 和 Windows 的跨平台发布构建。
