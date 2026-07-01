# 变更记录

本项目采用语义化版本风格发布。1.0 之前的版本可能包含不兼容变更。

## 未发布

### 新功能

- 新增 Azure OpenAI Responses provider 的 `preset: azure`，包括 Azure `api-key` 鉴权、兼容 Azure Responses 的请求头 / 默认 `store: true`、official API 400 处理、初始化模板支持，以及 `/openai/v1/responses` 配置示例文档。Provider 类型自动检测现在也会按 URL path 后缀判断并忽略 query / fragment，因此带 `?api-version=...` 的 endpoint 能被正确识别。
- 新增 provider 级 `auth_scheme`，让兼容 endpoint 可以把凭据 header 与请求传输类型分开覆盖；支持 `anthropic-api-key`、`bearer` 和 `api-key`。

### 改进

- 改进长 agent 循环中的 prompt cache 稳定性：动态环境信息现在放入 session-context reminder 而不是 system prompt；请求级增量剪裁会冻结已剪裁前缀；Anthropic 显式 cache breakpoint 可以落在冻结的已剪裁前缀边界上。
- Thinking 翻译现在会更严格校验模型输出，拒绝纯符号或过度压缩的译文；并改为在 assistant thinking 持久化后再翻译，而不是流式过程中翻译，避免 rollback / retry 路径留下过期翻译。
- TUI 流式渲染现在减少 assistant / thinking 增量的逐 token 缓存失效，降低长流式响应期间的重绘开销。
- 文件工具现在提供更完整的 not-found 路径建议，包括对常见模型生成路径的空白修复提示，并把同一套建议流程覆盖到 `read`、`view_image`、`edit` 和 `patch`；`patch` 仍会保留使用 `write` 创建文件的提示。

### 修复

- 自动压缩现在有 usage 缺失兜底：在收到可信的非零 provider usage 后，如果后续响应缺少 usage 或返回 0，Chord 会按当前会进入上下文的消息 bytes 相对校准样本的比例估算输入 token，使长会话仍能在撞到 provider 上限前压缩。如果实际尝试过的所有候选模型都返回 `context_length_exceeded` 且自动压缩已关闭，Chord 现在会停止并给出可操作错误，而不是退回到泛化的 fallback exhausted。
- 恢复会话或跨 provider 回放历史时，现在会跳过空的或不可回放的 reasoning-only assistant 消息，避免旧 reasoning / thinking 内容导致 provider API 拒绝请求。
- TUI 流式输出现在会在工具调用卡片出现前先 flush 已缓冲的 thinking 增量，避免 provider 交错发送 thinking 与 tool-use 事件时生成多余的 thinking 卡片。
- Patch 工具现在兼容模型常见的 `@@` 纯锚点写法：当一个 hunk 只含未修改的上下文行、而整个 patch 至少有一处 `+`/`-` 修改时，该 hunk 被接受为 no-op 锚点——它匹配当前文件并推进后续 hunk 的搜索位置，但不修改文件。兼容提示只会追加到模型可见的工具结果上下文（不展示到 TUI），让模型以更低的失败重试成本学到推荐的单 hunk 写法。工具描述与错误信息现在明确区分上下文 marker 空格与源码缩进，并给出纯插入示例。整 patch 只含上下文行时仍会被拒绝，并给出可操作信息。

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
- TUI 现在提供 `Ctrl+E` 错误面板，记录当前对话的中间重试错误和最终错误，并在 `/new` 开始全新对话时清空；可查看 provider、model、key 后缀、HTTP 状态码，以及可用时的结构化 API code/type 字段。
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
- `view_image` 等工具返回的图片现在会保留在对应的工具结果上，在 TUI 的工具结果卡片中显示为可打开的缩略图，并会对支持该能力的 API 通过 provider 原生的多模态 tool/function result 格式发送。`view_image` 只会在权限允许、有效 model pool 的第一个模型支持 image 输入且该模型不是 OpenAI Chat Completions 时可见；会话已包含图片/PDF 工具结果后，Chord 会跳过不支持所需 image/PDF 输入或使用 Chat Completions 的 fallback 候选。

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
