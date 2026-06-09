# 变更记录

本项目采用语义化版本风格发布。1.0 之前的版本可能包含不兼容变更。

## 未发布

### 改进

- JSON 热路径处理更快，包括 LLM 流解析、MCP JSON-RPC 编解码、会话导入 JSONL 解析和 `auth.state.json` 加载。
- 本地文件工具读取已有文本文件时现在优先使用 UTF-8 或带 BOM 的 Unicode，并保留对 GB18030、Big5、Shift-JIS 等常见地区性编码的受限支持。无法明确识别或不受支持的编码仍会快速失败；`web_fetch` 仍会按 HTTP 响应声明的 charset 解码。
- `read` 现在返回不带行号 gutter、也不额外缩进的原始文件文本，复制片段用于 patch hunk 或缩进敏感格式时更安全。
- `grep.path` 现在会说明它只接受一个文件或目录路径；当把多个已存在路径用空格拼成一个路径传入时，会给出更有针对性的提示。
- 请求级上下文剪裁现在会保护近期高风险工具输出，例如 diff、失败断言、stack trace、权限/安全错误，避免它们仅因很长的单轮工具链推进了 effective age 就被剪裁。默认 `context.reduction.read_like_age_turns` 也基于近期会话统计从 1 上调到 2，让刚读取的文件上下文以较低的观测 token 成本多保留一个 effective turn。
- 上下文剪裁现在会等到同一模型连续第 3 次请求时才保护 prompt cache surface；切换模型会重置这个连续计数，因此新模型的前两次请求在压力下仍可剪裁已缓存上下文。
- 当所有 TODO 完成后，上下文剪裁现在会给下一次 main-model 请求一个单请求收尾宽限窗口，避免最终回复前临时剪裁打破已有 prompt cache；模型切换或高压上下文会跳过该宽限。可通过 `context.reduction.wrap_up_grace_requests` 配置。
- 上下文剪裁现在默认只保留更短的成功 shell 输出片段，减少上下文压力下保留的低信号终端输出。
- Codex OAuth 运行时状态现在用用户在工作区内的组合键（`account_user_id`）识别账号，不再只按 account/workspace ID 区分，避免多个用户共享同一工作区时 quota/status 更新互相覆盖。只含 refresh 的凭据在首次成功刷新后会从临时 `refresh_sha256:<digest>` state key 迁移；OAuth access token 现在需要能解析出 account 与 user/account-user claim。
- Codex OAuth 运行时状态现在改用 `auth.state.json`，不再使用已发布版本中的 `auth.state.yaml`；已有运行时缓存可由 warm-up/轮询重新生成，但 YAML 文件中的 quota/reset/账号状态缓存不会自动迁移。
- `chord auth refresh <provider>` 现在可以刷新某个 provider 下所有带 refresh token 的 Codex OAuth 凭据，逐账号报告成功 / 失败 / 跳过状态，并保留 rate-limit reset 提示。
- `view_image` 等工具返回的图片现在会保留在对应的工具结果上，在 TUI 的工具结果卡片中显示为可打开的缩略图，并会对支持该能力的 API 通过 provider 原生的多模态 tool/function result 格式发送。`view_image` 只会在权限允许、有效 model pool 的第一个模型支持 image 输入且该模型不是 OpenAI Chat Completions 时可见；会话已包含图片/PDF 工具结果后，Chord 会跳过不支持所需 image/PDF 输入或使用 Chat Completions 的 fallback 候选。

### 修复

- TUI 消息渲染现在会在绘制卡片前转义原始控制字符，避免粘贴内容或模型输出包含 `\x01` 等字节时出现背景色异常。
- 请求进行中切换 model pool 现在会在下一次请求边界生效，不再打断正在进行的请求；状态栏与信息面板会显示下一次请求将使用的模型。
- 失败的 `edit` 工具卡片现在会先显示本次尝试的 patch，再显示错误文本，便于在阅读诊断前先检查失败 hunk。
- 向聚焦 agent 提交消息时，现在会附带 `@file` 引用的文件内容 parts，不再只发送纯文本。
- 当 provider 报告账号或 workspace 已停用时，OAuth key 现在会从选择中永久移除，包括以 HTTP 402 返回的停用错误。
- `view_image` 后恢复会话时，工具返回的图片不再显示成用户手动发送的消息。
- TUI 渲染现在会禁用终端硬件滚动区域，避免受 DECSTBM 滚动恢复问题影响的终端出现重复分隔线或旧边框残留。
- 在 TUI 输入框粘贴剪贴板图片时，现在不会再重复添加图片附件；删除 inline 图片占位符时也会同步移除对应附件 chip。

## 0.6.3 - 2026-06-05

### 亮点

- 多模态：跨 provider 的 PDF 输入支持，以及用于本地图片的内置 `view_image` 工具
- 上下文剪裁现在对大型工具结果使用类型化摘要，保留关键信息
- 卸载空闲的 LSP/MCP 资源以降低内存占用
- 改进 `edit` patch 容错性，TUI 工具结果显示更清晰

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
- **配置：** HTTP `User-Agent` 覆盖移到 provider 级 `user_agent`，旧的 Anthropic transport 字段已移除。请求默认使用 `User-Agent: chord/<version>`，除非显式覆盖。
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
