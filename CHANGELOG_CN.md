# 变更记录

本项目采用语义化版本风格发布。1.0 之前的版本可能包含不兼容变更。

## 未发布

- Tools / ApplyPatch：当前局部文件编辑工具面已切换为原生单文件 `ApplyPatch`，用于修改已有文件的局部内容，以减少旧 `Edit` 工作流中 exact-string 替换不匹配导致的编辑失败；`Write` 继续负责完整写入，`Delete` 继续负责整文件删除。ApplyPatch 现在会在 pre-diff planning 读取目标文件前先执行 read-before-patch / stale protection，并按当前搜索位置之后的第一个 hunk 匹配应用补丁；如果某个 hunk 匹配到多个位置，会在成功输出中附带候选行提示，prompt 也会要求在重复块中加入唯一上下文。相关 prompt 与权限提示也已从旧 `Edit` 语义迁移。
- Tools / 文件编辑：`ApplyPatch` 现在接受通过 `Read`、之前成功的 `Write` / `ApplyPatch`，或系统解析的 `@file` mention 观察过的文件。如果文件在观察后发生变化，只要工具仍能基于当前内容验证操作，文件编辑工具会警告而不是直接拒绝；有风险的非空写前内容会备份到会话目录，并在工具结果中提示备份路径。空文件和无风险的连续 agent-owned 编辑不会创建备份。备份上限为每 path 10 个、每 session 200 个、单文件 10 MiB、每 session 总计 50 MiB；备份失败会在工具结果中说明但不阻塞编辑，备份会随会话目录删除而清理。
- Tools / Grep & Glob：降低搜索/路径列表输出的默认结果上限并新增字节上限（`Grep` 现在最多返回 120 条匹配 / 约 12 KiB，`Glob` 最多返回 250 个路径 / 约 16 KiB），避免过宽的搜索结果挤占更相关的上下文。
- Session import：Codex、Claude Code 与 OpenCode 导入现在会尽量把可识别的外部工具转换成最接近的当前 Chord 工具卡（`Read`、`Shell`、`Grep`、`Glob`、`ApplyPatch`、`Write`、`Delete`）；参数不足或无法识别的工具仍保留为 unsupported tool card。导入 provenance 仍会在内部保留，因此外部文件修改不会恢复 Chord FileTracker 的 read/write 状态；导入后继续编辑前仍应重新 Read 相关文件。
- LLM / Codex：Codex OAuth access token 现在必须包含可解析的 account ID；缺失 account ID 的 token 会被拒绝，不再当作可用凭据。尚不知道账号的 refresh-only Codex OAuth 凭据仍可先参与选择；如果 refresh 在账号未知前发生不可恢复失败，Chord 会在 `auth.state.yaml` 中写入 `refresh_sha256` 状态，之后由用户执行 `chord auth state clean` 清理不可用凭据，而不是在请求处理中直接删除 `auth.yaml`。`token_invalidated`、revoked、无法解析认证 token，以及 refresh token 返回 401 等情况现在会被归类为不可恢复的 OAuth 失败，并避免无意义的重复 refresh。
- LLM / Codex：Codex `key_order: smart` 现在把缓存的额度快照仅作为调度提示：只有报告为 `100%` 的窗口会被放到最后尝试，`99%` 仍视为有可用额度，并且 primary / secondary 窗口会分开比较，让短窗口额度能在过期前优先使用，而不会和周额度混为一个分数。
- LLM / Codex：Codex WebSocket 返回 400 且错误信息表明服务端增量会话状态与本地 input 不一致（例如 `No tool call found for function call output with call_id …`，或消息中包含 `previous_response_id`）时，Chord 现在会清空 WebSocket 链状态并立即在同一 WS 上以**全量、不带 `previous_response_id`**的方式重发一次；只有原始 400 属于链状态不一致才会触发该重试，重试仍失败时直接返回错误、不再回退 HTTP，因为 HTTP 会发送相同的 input、注定同样失败。
- Agent / 会话恢复：恢复的对话现在会在送入 provider 前修复结构上的破损——尾部 `stop_reason=interrupted` 的 assistant 消息会被丢弃，每个 assistant `tool_call` 若缺失对应 tool result，会补一条合成的 `error` tool 消息（`ToolStatus=error`）。仅做结构修复；tool 消息的文本和 `ToolStatus` 字段不再被改写，1.0 之前基于文本启发式（搜索 `denied` / `cancelled` 等子串）猜测状态的逻辑不再恢复。
- TUI / 消息目录：Ctrl+T 消息目录现在在主视图区域内渲染，不再遮挡右侧信息面板；条目按 1 开始编号显示，新增 `Ctrl+F` / `PgDown` / `Ctrl+B` / `PgUp` 一次翻一页。
- LLM / Key pool：多个 credential slot 如果共享同一个 access token，现在会同步应用 cooldown、recovering、quota-exhausted 和 success 状态，避免已耗尽 token 通过另一个 slot 再次被选中。
- LLM / Codex：Codex WebSocket 返回 usage-limit / quota exhausted 类错误时，现在会跳过 HTTP fallback，直接进入 key quota/cooldown 处理，避免先等到响应头超时才切换下一个账号或 fallback 模型。
- LLM / 兼容网关：非官方兼容网关返回看起来是临时态的 HTTP 400（例如 `Concurrency limit exceeded`）时，现在会冷却当前 key、轮换到下一个 key，并允许跨完整池继续重试；官方 API 的 400 以及请求参数/模型不兼容类 400（包括 `Store must be set to false` / `Stream must be set to true`）仍立即停止。
- Runtime / 压缩：fallback 压缩摘要现在会把最新 `Done rejected:` 原因作为当前请求锚点；当模型不可用、无法做 TODO 相关性判断时，会把压缩前 TODO 移入 stale/superseded。Loop continuation prompt 也会包含精确的 Done 拒绝原因，避免用户拒绝中变更范围后被降级成泛泛的“继续当前目标”。恢复会话时，如果最新压缩摘要明确将旧 TODO 标为 stale/superseded，会丢弃这些压缩前 TODO；但会保留压缩后新 TodoWrite 写入的列表。非 Done 工具的用户拒绝原因如果包含可执行的新指令，也会作为 latest-request evidence 保留。
- CLI / 清理：`chord cleanup sessions` 现在会在删除会话目录后，把只剩 `project.json` 的项目会话目录也一并移除，因此旧会话清理不会留下空的 per-project 容器。`--older-than` 现在使用删除子目录前的项目会话目录时间戳，确保删除旧会话后项目容器变空时，dry-run 预览与 `--yes` 实际清理保持一致。
- Tools / Shell：`git stash show -p`、`git stash list --patch` 等非交互式 `git stash` 子命令不再被识别为交互式补丁流程；只有在无管道输入时的 `git stash push -p` 和 `git stash save --patch` 仍会被拦截。
- TUI / Service tier：`/tier` slash 补全现在和 `Ctrl+R` 快捷键保持一致——预测的下一个 tier 相同；如果当前唯一支持的 tier 就是已生效的 `standard`，`/tier` 会从补全列表隐藏，快捷键也变为 no-op。
- TUI / 自定义命令：slash 补全现在会内联显示每个自定义命令的 scope，例如 `/commit  [project] ...`，便于区分 project / global 命令且不额外占用菜单行。
- TUI / YOLO：新增 `--yolo`、`/yolo on|off` 和 `Ctrl+Y`，可在运行中切换临时 MainAgent 权限绕过。Handoff、Delegate、Cancel 和 Done 权限仍会生效；启用时状态栏显示 YOLO。
- TUI / 权限：`/rules` 现在即使没有已记住规则也会打开，并支持手动添加 session/project/global 级 allow/ask/deny 规则。确认弹窗的记住规则选择器现在允许保存前手动编辑建议 pattern；Delete 确认会提供保守的路径级候选，而不是完全禁用记住规则。
- TUI / 鼠标选择：文本选择体验现在在对话卡片、Done/Handoff Markdown viewer 和 composer 输入框之间保持一致。双击选中当前词，三击选中当前可见行，拖拽选择继续保持原行为。
- TUI / 快捷键：各 overlay 不再保留未提示的关闭/操作键。Stats 浮层移除 `q`，hint 同时展示 `esc/$ close`；Model Select 移除 `ctrl+d`（esc 是文档化的关闭键）；Handoff Select 移除未公开的 `d/D`，只用 `r` 进入 deny-reason 流；通用 Confirm 对话框新增 `enter` 作为 Allow，并在 UI 显示 `[Enter/A] Allow`、`[Esc/D] Deny`。Help 与 Stats 浮层在状态栏也会显示 `esc ⇢ close`。
- TUI / 工具卡：已恢复/已继续的文件修改卡片现在使用与实时结果一致的展开终态。没有持久化 diff 的 Edit 卡片会显示保存的结果文本，不再只渲染空 header；恢复后的 Write/Edit 错误和 Delete 完成结果也无需手动展开即可保持可见；确认弹窗里记住规则的快捷键现在是 `M`，让 `A` 保留给 Allow。
- Auth / OAuth：不再把本地 access token 的 `expires` 元数据当作凭据已失效的证明；只有 provider 或 token endpoint 的认证失败确认不可用后，才会将 OAuth slot 标记为 `expired`。
- Auth / Codex：Codex OAuth 运行时状态现在会在 `auth.state.yaml` 变化后自动重新加载，因此其它 Chord 进程写入的额度快照、reset 计时、刷新的账号元数据以及 invalidated/deactivated 状态无需重启当前会话即可生效。
- **不兼容 / 配置：** 将 provider / 模型 HTTP 请求的 `User-Agent` 覆盖移到 provider 级 `user_agent`（仅影响普通模型请求）。旧的 Anthropic transport 兼容字段 `user_agent` 已移除；provider / 模型请求默认使用 `User-Agent: chord/<version>`，除非显式覆盖。
- LSP / 诊断：Write / Edit 后追加到工具结果中的 diagnostics 现在会等待 fresh 的 publishDiagnostics 快照和短暂 settle 窗口。若 server 提供 document version，会忽略旧版本 diagnostics；无 version 的 diagnostics 也必须在 edit 通知之后到达，从而减少 gopls 等异步 server 的瞬时误报。
- Headless / 本地 shell：新增 `local_shell` stdin 命令和 `local_shell_result` 事件，让 headless 集成可以执行 `!` 风格的本地 shell 命令，并接收带超时和输出大小限制的 stdout/stderr 合并结果。
- Headless / Handoff：`Handoff` 现在会在 headless 模式下发出结构化的 `handoff_request` 事件，包含完整已保存 plan 以及可选 agent / model pool；headless 也新增 `handoff` 命令，可批准执行或拒绝后继续规划。
- **不兼容 / 配置：** 移除未使用的 `context.reduction.model_pool` 配置项和未使用的 `maintenance.size_check_interval_hours` 配置项。Context reduction 仍是确定性剪裁，不会调用辅助模型；需要 LLM 参与的持久化上下文压缩请使用 `context.compaction.model_pool`。
- Runtime / 上下文剪裁：将默认字节阈值调成更偏 prompt cache 友好的配置，同时继续剪掉大型旧输出。`shell_success_bytes` 现在是 `8000`（之前 `4000`），`read_like_output_bytes` 现在是 `4000`（之前 `2500`）；age gate 和 `min_tool_results_prune` 保持不变。
- Runtime / 上下文剪裁：低 Codex 额度时的 request-surface 冻结现在不再依赖 loop 模式。当 Codex 5h 或 7d 额度窗口剩余不足 10% 时，连续自动 continuation 会保持已准备的消息表面、系统提示词和工具定义稳定，使 `stop_reason=tool_call` 链尽可能继续执行到 `end_turn`；回到 idle 或发送真实用户消息会解除冻结，让下一次请求重新构建 surface。
- Runtime / Loop：loop 模式下的 `Done` 退出请求不再有机器强制的验证状态门槛。未完成 TODO、活跃 subagent、blocked 状态以及格式错误/混用的 `Done` 批次仍会阻止自动完成；类似验证的工具结果仍会作为 stall 检测的进展信号。
- TUI / 导航：当用户手动关闭 sticky follow 后，在底部执行无位移的向下滚动不会再重新开启 sticky follow，从而保留手动滚动状态。
- TUI / 工具卡：卡片 header 现在显示从 1 开始的 block 序号（例如 `TOOL CALL #12`），方便明确引用渲染出来的卡片。
- TUI / Git 侧边栏：当当前目录位于 Git 仓库内时，右侧 info panel 现在会显示紧凑、可折叠的 Git 摘要，包括分支或 detached commit、linked worktree 名、改动文件数、已暂存文件数、stash 数量以及 ahead/behind 数量。Git 状态会在启动、文件修改工具或 Shell git 命令后异步刷新，并通过低频定时器更新，不阻塞渲染。
- TUI / Handoff：planner 触发 Handoff 后，选择器现在按审批决策处理：Enter/A 表示 approve 并启动计划执行，R 表示填写拒绝原因并让模型继续当前回合，Esc 只关闭选择器并保留已保存的 plan，等待用户后续输入。工具确认弹窗统一使用 A 表示 Allow、D 表示 Deny、R 表示 Deny+Reason、M 表示添加记住规则；Done 确认弹窗刻意不提供 Deny，只接受 A/R/V/esc，确保拒绝时必须带原因。选择器现在也会预览已保存的 plan 内容，较长 plan 可用鼠标滚轮滚动，并提供 View 操作用全屏 Markdown 视图查看完整 plan。等待用户决策时，状态栏现在会回到 idle。
- LLM / Fallback：API 400 错误现在会作为候选模型级失败处理，而不是立即视为整个请求失败，因此 client 可以继续尝试另一个可能接受同一段对话历史的配置模型。对于 request-shape 400，client 仍不会在同一模型上轮换 key，并会在模型池耗尽后停止。
- LSP / Python 诊断：Python 文件的 Edit / Write 完成后诊断新增 quick 回退后端。默认配置下，主诊断走 `pyright` LSP，quick 回退走 `ruff check ... --output-format json`；大文件（可配置 `diagnostics.python.large_file.line_threshold` / `byte_threshold`，默认 5000 行 / 250000 字节）会自动改走 quick 后端，避免阻塞在完整语义分析上；当 quick 后端不可用时，可通过 `run_semantic_when_quick_unavailable: true` 让大文件继续跑语义诊断。新增顶层 `diagnostics.*` 配置，包含分后端的命令/服务选择，以及 `diagnostics.python.output.*` 用于控制追加到工具结果中的诊断文本长度与裁剪窗口。完整字段见 `docs/configuration_CN.md`。
- LSP / 诊断输出：追加到工具结果中的 LSP 与 Ruff 诊断现在会按优先级裁剪为更简洁的块（错误和警告优先；还有剩余名额时再显示 info 和 hint）。无诊断时不再追加冗余状态行；只有诊断集合确实变化时，才追加简短的 `Diagnostics changed: N new, M resolved.` 摘要。
- TUI / 复制：在工具卡片上按 `yy` 会复制结构化的 Markdown 块（`# Tool call` / `## Arguments` / `## Result` / `## Diff`）；Done 拒绝卡片额外包含 `## Rejection reason` 段，方便粘贴到外部时保留 Chord 的展示结构。
- Runtime / 压缩：`/compact` 现在与自动压缩共用同一个后台 worker；可以在 turn 进行中触发，进度显示在后台压缩状态槽，并在下一个安全的 continuation / idle 节点应用，而不要求先达到 idle barrier。
- Runtime / Loop / 压缩：在 loop 模式下也会执行自动和手动上下文压缩，让长跑 loop 会话能在 context 预算耗尽后继续运行。新增消息的请求级 reduction 现在只会在 `preset: codex` provider 且 5h 或 7d 任一配额窗口剩余不足 10% 时关闭；其它情况下，loop 模式仍保持正常上下文剪裁。
- Runtime / Service tier：`/tier standard` / `/tier fast` / `/tier slow` 和 `Ctrl+R` 现在会将 service-tier 状态同步作用到 SubAgent。已存在的 SubAgent LLM client 会立即更新；新创建、恢复、rehydrate 或切换模型的 SubAgent client 会继承当前 service-tier 状态。空的 `/tier` 不是状态查询命令；侧边栏/状态栏会在 `standard` 时隐藏 tier 标识，并且只在当前 provider/model 实际支持并应用该档位时显示 `tier: fast` / `tier: slow`。
- **不兼容 / 配置：** 移除旧的模型字段 `supports_fast`。之前使用 `supports_fast: true` 的模型请迁移为 `supported_service_tiers: [fast]`；省略 `supported_service_tiers` 表示使用 preset/provider 默认值；如果 provider/model 支持多个非 standard tier，可显式配置如 `[fast, slow]`。
- Runtime / Delegate todo：当当前角色有 `Delegate` 工作流时，`TodoWrite` 允许多个 `in_progress` 条目，但每个 in_progress 条目必须有唯一的 `active_form`，明确对应一个真实的 live workstream。无 `Delegate` 的角色仍保持单 in_progress 限制。
- TUI / Thinking 翻译：译文现在持久化到 `<session_dir>/thinking_translations.json`，按 `(message, block)` 定位、用内容 hash 校验；恢复会话后会直接复用。修改 `thinking_translation.target_language` 不会重新翻译已经存在的 block —— 同一个 thinking block 最多翻译一次。译文仅用于 UI，不会写回模型上下文。
- TUI / Thinking 翻译：如果翻译模型误回显内部包装标记（包括只有开头 `<TRANSLATION>`、缺少闭合标签的情况），Chord 会在持久化、恢复和渲染前移除这些标记，避免它们被 Markdown 解析成 HTML block 导致翻译卡片格式失效。
- Runtime / Thinking 翻译：翻译模型返回的空、明显截断或不是目标语言的结果会视为软失败，自动切到模型池下一个候选，而不是把破损的翻译卡片留在界面上。
- Runtime / 拒绝原因：`Question` / 确认弹窗的 deny reason 现在完整保留用户原文（包括内部换行），同时影响给模型的 tool result 和 TUI 卡片。
- TUI / 限额侧边栏：codex 限额侧边栏不再在切换 key 后复用 provider 范围的 inline snapshot；切换 key 后，侧边栏会显示新 key 的 inline snapshot、该 key / account 已 polled 的 `/wham/usage` 快照，或者在新数据到来前留空。
- TUI / 导航：normal 模式下 `gg` / `G` 现在把焦点移动到第一个 / 最后一个可见卡片，而不再只是单纯滚动到底/顶，与 vim 风格的焦点导航一致。
- TUI / 工具卡：header 截断时不会再丢掉运行中的进度后缀；已删除文件的 Delete 卡片在恢复会话后仍会显示拒绝 / 完成 reason；toast 边界变化会触发重绘，让边界标记保持同步。
- TUI / 焦点恢复：延迟到达的 tail-window 焦点恢复事件会被重新应用，让聚焦的卡片在多次重绘后仍保持可见。
- Runtime / 上下文 reduction 统计：info-panel 的 `Reduced` 行在 `/compact` 或自动压缩重写会话历史后会刷新，loop 模式下同样有效。
- Runtime / 压缩摘要：持久化压缩摘要的小节标题从单一的 `## Goal` 拆为 `## Current User Request` / `## Active Objective` / `## Background Goals`。最新用户请求和最近一次 Done 拒绝原因现在被视为同等优先级的 latest-request anchor（按消息序号取最新），不会再被一条更早的约束反过来盖住新到的 Done 拒绝。`## Todo State` 显式使用 `Active/relevant to latest request` / `Completed/background` / `Stale/superseded` 三个子组，让压缩后的 agent 按最新请求重新评估旧 todo，而不是默认沿用。
- Runtime / 上下文剪裁：剪裁现在会保留最近的未完成 tool-call 链在当前尾部，而不是直接归档，这样没有 result 的 tool call 会继续和 assistant 消息保持配对；当这类未完成 tool call 足够陈旧时，仍然可以被压缩掉。每次发送 LLM 请求前，凡是已经没有有效 assistant tool-call 锚点的孤儿 tool result 都会被丢弃，不会再发送给 provider。
- Runtime / Plan 执行：开始执行 plan 时，prompt 现在通过 `@<plan-path>` file mention + 附带文件 part 的方式把 plan 内容传给 LLM，不再把 plan 文本直接内联进 system prompt；这样 plan 体积有界，引用方式与普通文件 mention 一致。
- Runtime / SubAgent prompt：SubAgent 系统提示现在与 MainAgent 一致，platform 字段输出完整 `<goos>/<goarch>`，而不是只 `<goos>`。
- CLI / Done 自动化：统一了自动 Done 拦截在启动、运行时和 TUI 中的行为。启动/TUI 创建/关闭路径现在更易测试，浏览器启动式 auth 规划也可以在不实际拉起浏览器命令的情况下验证，loop 模式的 Done 退出拦截文档也已保持一致。`CHORD_PPROF_PORT` 设置成非法值时，`--continue` / `--resume` / `--worktree` 及其它启动流程会继续生效（pprof 仅记一条 warn 后跳过），不再被静默丢弃。
- TUI / Done 内部机制：集中收口了 Done 相关 UI effect 与流式渲染失效处理，拆分了更聚焦的回归测试，并保持 rejected 与 auto-rejected Done completion 的界面呈现一致。
- Config / Auth 持久化：将 auth 持久化锁统一到共享的配置变更锁实现，并补充了写锁行为与 named pipe 测试可移植性的分平台回归覆盖。
- **不兼容 / 权限：** 记住的权限规则现在直接写入 agent 配置文件，不再写入单独的 permissions overlay。`session` 规则仍只保存在内存中；`project` 规则更新 `<project>/.chord/agents/<role>.yaml`；`global` 规则更新 `<config-home>/agents/<role>.yaml`；`/rules` 删除规则时也会从同一个 agent 文件移除。内置 planner 现在默认只允许在 `.chord/plans/*` 下执行 `Write` / `Edit`。先前写入 `<project>/.chord/permissions/<role>.yaml` 或 `~/.chord/permissions/<role>.yaml` 的规则不会再被加载，也不会再输出旧文件提示；如仍需要，请手动移入对应 agent 配置文件。
- TUI / 性能：降低后台标签页和长对话的 CPU 与渲染开销，让大会话中的滚动和重新聚焦恢复更顺畅；权限判定与工具定义构造热路径也做了优化。
- **不兼容 / 兼容路径：** 移除剩余 pre-1.0 历史兼容路径。Codex 导入现在只接受当前 `type` + `payload` rollout schema；`--config` 不再作为 `--config-home` 的别名；headless 模型切换只接受 `set_current_model_pool`；会话恢复不再恢复旧的、基于启发式的 dangling / duplicate tool-result 修补；model-pool state 与 recovery snapshot 不再读取 `current_role` / `model_pool_current_role`；OAuth 凭据更新要求账号身份，不再用 access token 或索引兜底。Anthropic usage 解析在嵌套 `usage.cache_creation` 存在时优先读取其 TTL 拆分，若仅返回扁平 `cache_creation_input_tokens`（部分 Anthropic-compatible 网关）则以扁平字段兜底统计 cache write tokens。
- TUI / Delete：`Delete` 工具卡片现在会在实时会话和恢复后的会话中显示必填的 `reason`；恢复工具卡片也会优先使用已持久化的状态和耗时元数据，让继续会话后的历史展示与实时完成态保持一致。
- Docs：明确 agent 权限保存在 `agents/<role>.yaml`，确认弹窗和 headless 记住规则都会更新对应 agent 配置文件；顶层配置 keys 表新增 `diagnostics` 条目；补充了 `thinking_translation.max_chars` 预览范围的说明。

## 0.6.0 - 2026-05-20

- Build / 依赖：源码构建与 release artifact 现在要求 Go 1.26.3+，直接运行时依赖已更新到当前兼容最新版，并刷新了实际构建图中的间接依赖。CI 与 release workflow 都从 `go.mod` 读取 Go 版本，因此发布二进制会使用已修补的 Go toolchain 构建。
- Runtime / 上下文：新增 `context.reduction` 下的确定性请求级上下文裁剪控制，包括陈旧工具结果剪裁阈值和专用 reduction 模型池预留配置；loop 模式仍保持不做请求级裁剪。
- Auth / Runtime：将 OAuth 账号状态的权威来源迁移到 `auth.state.yaml`，新增 `invalidated` 状态与 `key_invalidated` 流式增量，并确保旧版 `status` 不再写入 `auth.yaml`。
- **不兼容 / 配置：** 将 `context.compact_threshold` 重命名为 `context.compaction.threshold`；旧字段不再提供兼容别名。
- **不兼容 / 配置：** 移除 `context.auto_compact`。现在当 `context.compaction.threshold > 0` 时启用自动上下文压缩；设置 `context.compaction.threshold: 0` 可关闭。
- **不兼容 / 配置：** 移除 `context.compact_model`。上下文压缩现在只接受 `context.compaction.model_pool` 来指定专用压缩模型池；未设置时，压缩会克隆当前 agent 的模型池，而不是回退到单个已选模型。
- **不兼容 / Headless：** 从 headless/protocol 集成面移除对外 `tool_result` 事件。非 loop 的 `Done` 报告现在使用专门的 `done_completion` 事件；loop 模式的 `Done` 退出申请继续使用 `confirm_request`，并显式携带 `done_report` / `done_reason` 字段。普通工具结果仍保留在 agent/TUI 内部生命周期中，不再转发给 headless 客户端。
- TUI / 输入法：自动切换输入法现在只会在当前 Chord 所在标签页/窗口实际处于前台激活时执行，避免后台标签页中的 chord / mode 切换干扰当前正在使用的标签页输入法；当收到 `FocusMsg` 或切回该标签页时，如果当前 mode 仍需要英文输入法，Chord 会重新应用已配置的英文 IME target。
- CLI / 初始化：默认 `chord` 命令在全局 `config.yaml` 缺失时，新增首次启动初始化向导。向导会在控制 TTY 上运行，写入最小 `config.yaml`，必要时再写入 `auth.yaml`；也可以在初始化阶段直接完成 Codex OAuth 登录；如果已有匹配凭据会尽量复用，并在结束时打印实际使用的路径。
- Runtime / Loop / Done：`Done` 工具现在要求必传非空 `report` 参数，用来承载完整的最终完成报告。loop 模式把 `Done` 作为唯一的退出申请入口：过早的 `Done` 会被拒绝并作为 tool result 回给模型继续运行；满足退出条件时，则弹出本地确认框并展示这份报告。
- TUI / Done：`Done rejected:` 与 `Done rejected automatically:` 现在都会按 rejected completion 渲染，统一复用现有失败态样式（`✗ Done`）并显示精简后的 rejected reason 行，让 loop 自动拒绝和手动拒绝在界面上保持一致可见。
- TUI / 剪贴板：图片粘贴现在会对同一次 `Ctrl+V` / `Cmd+V` 按键事件与终端 `PasteMsg` 做去重，避免终端同时发出两条路径时偶发重复插入两张图片附件。
- Tools / 安全：补强本地文件/路径安全，对文件读取与搜索工具统一路径校验逻辑。`Read` 与 `Grep` 现在复用同一套已存在路径检查，并会显式拒绝标准流设备文件等受限 device-style 路径。
- Config / 持久化：补强 setup 与 auth 持久化时的配置写入流程。初始配置创建现在使用配置级锁文件 + 临时文件 + 原子安装；auth/config 保存路径也复用同一套原子写入基础设施。
- Worktree：`chord worktree finish` 现在会先把目标分支合并进真实 worktree 分支，把冲突前移到那里处理；随后再把完成后的 worktree 状态以单个 squash commit 合回目标分支。`--check` 也改为在临时 worktree 中预检这一步 merge，而不改动真实 worktree 或目标分支；另外，若真实 worktree 已有进行中的 rebase 或 merge，`finish` 会直接拒绝启动。
- CLI / 导入：新增 `chord import`，支持从 Claude Code（`claude`）、Codex（`codex`）和 OpenCode（`opencode`）导入外部会话。导入会生成可恢复的 Chord session 和 `import-report.json`；Codex 默认使用保守的 `auto` 工具导入，OpenCode 默认以纯文本导入工具历史且现在能处理当前 `{info, parts}` 导出并以可读 fallback 保留不支持的工具，Claude 则在兼容时默认 `auto` 保留结构化工具调用。
- TUI / 标题告警：确认 / 问题请求在 Chord 后台出现时，终端标题栏仍会闪烁以吸引注意；用户重新聚焦该标签页/窗口后，当前请求的告警会保持可见但停止闪烁。
- TUI / 用量：usage/context 更新现在会让信息面板渲染缓存失效，修复上下文压缩或其它用量刷新事件后侧边栏 `TOKENS` 可能保持旧值的问题。
- Docs：明确 `chord auth state clean` 会清理无效运行时状态和匹配的过期/已停用 OAuth 凭据，并更新 README 亮点，说明后台压缩与 `/loop` 可支撑长时间连续运行任务。

## 0.5.3 - 2026-05-11

- Runtime / 文件安全：恢复或继续会话时，现在会重建持久化的 `Read` 文件状态；之后的 `Edit` / `Write` 仍保留“先读后写”保护，但不会再误要求所有文件都重新读取一遍。
- Runtime / 压缩：未配置 `limit.input` 时，自动压缩和模型池 fallback 的输入预算会从 `limit.context` 中预留有效请求输出，减少超大 prompt 重试和过早/过晚触发阈值的问题。
- Config / Runtime：项目级 `.chord/config.yaml` 现在在启动、auth login 和模型诊断中走同一套合并逻辑；格式错误的项目配置会明确报错，不再静默忽略，并新增 `stream_retry_rounds` 以便自动化场景限制公开 LLM 重试轮数。
- TUI：修复 Markdown 预览的语法高亮；文件末尾的有序列表、标题等按行识别的语法标记，现在会在 `Read` / `Write` 工具卡片和 fenced code block 中保持与前面行一致的颜色。
- CLI：用 `chord doctor models` 替换旧的 `chord test-providers` 入口，新增精确 `provider/model[@variant]` 检查、模型池审计、all-model/all-pool 模式、按目标 timeout、默认单次诊断且可通过 `--retry` 显式重试、fail-fast、JSON 输出，并覆盖 model / variant tuning。
- CLI / Doctor：`chord doctor models` 在多目标诊断过程中会复用最新的 OAuth 凭据状态（刷新/过期/停用等），避免使用过期快照导致误报。

## 0.5.2 - 2026-05-11

- Worktree：`chord worktree finish` 现支持 `--check`，会在临时隔离 worktree 中预检一次 rebase，让你提前知道能否干净收尾，同时不改动真实 worktree，也不会在冲突时把它留在半个 rebase 状态。
- **不兼容变更：** 模型可见的命令执行工具从 `Bash` 重命名为 `Shell`。运行时不提供别名或兼容映射；升级前请同步更新权限规则（`permission.Shell`）、hook 的工具过滤器、skills 的 `allowed_tools`、已导入/已保存的结构化工具调用、headless / tool event 消费方、gateway，以及所有引用旧 `Bash` 工具名的自定义提示词或集成。
- TUI：增强了 Ghostty/cmux 在切换标签页或重新获焦后的恢复。较晚的 `post-focus-settle-fallback` 现在会在重放整帧前重新校验终端尺寸，减少首轮 `focus-restore` redraw 后仍残留横向分隔线伪影或旧 cell 的情况。
- TUI：侧边栏 / 信息面板中的文件列表从 `EDITED FILES` 改名为 `CHANGED FILES`；新产生的 `Delete` 工具结果会把被删除文件显示为删除线文件名，并且不再显示伪造的 `-1` 行数统计。
- TUI：`Write` 工具卡片现在会用带行号、语法高亮的预览展示写入后的文件内容，并与 `Read` 卡片共享默认前 10 行、按空格展开的行为。
- TUI：默认快捷键已在各模式间对齐：`Ctrl+P` 现保留给模型选择器（移除 Insert 模式历史记录绑定）；消息目录/树从 `Ctrl+J` 移至 `Ctrl+T`；默认 `Ctrl+F`“从输入附加图片路径”绑定已移除（如仍需要，可自行配置 `insert_attach_file`）。
- Runtime / LLM 重试：API `402` 用量/余额耗尽错误现在会按 per-key 限流处理：Chord 会冷却已耗尽的 key，并在 fallback 前优先尝试同模型下配置的其它 key，避免反复重试同一个已耗尽 key。
- Tools/Safety：收窄非交互 Shell/Spawn 防护规则。普通 stdin 读取（如 shell `read`/`select`）现在会看到 EOF，不再在执行前被拒绝；依赖终端/TTY 的命令仍会被拦截。
- Runtime/Codex 限流：provider 用量轮询现在会继承应用上下文，因此关闭/取消时会中止待处理的 Codex 用量刷新，而不会继续挂在脱离生命周期的后台上下文上。
- Auth/Codex：浏览器登录与设备码登录的 HTTP 请求现在会继承 CLI 命令上下文，因此按 Ctrl+C 或父级关闭时，可以及时取消进行中的设备码与 token 交换请求。

## 0.5.1 - 2026-05-09

- Runtime / TUI：新增针对 `manual: true` MCP server 的手动运行时控制。Chord 现在提供 `/mcp`（`status`、`enable`、`disable`）以及 TUI 内的 MCP 选择器（`Ctrl+O`），可在运行时按需连接或断开这类 server。自动启动的 server 保持只读，且 MCP 状态刷新时选择器会继续保持打开。
- Runtime：修复了初始 LLM client 未按 builder agent 的 model pool 配置的问题。此前即使配置了多个模型，冷启动后的首个请求在失败时也只会在第一个模型的多个 API key 之间重试，不会切换到池中的其他模型。现在初始 client 会正确携带 builder agent 的完整模型池，因此首轮失败也能触发跨模型的 fallback。
- TUI：修复了 Ghostty/cmux 在焦点恢复或 resize 恢复后出现 stale cells 残影/叠帧的问题。`focus-resize freeze` 路径现在直接把已绘制的 screen buffer 按整帧序列化并保留 trailing spaces，而不是依赖字符串级别的清行或 render 后补空格。
- TUI：将 Bubble Tea 渲染栈升级到更新的兼容版本（`bubbletea/v2` 2.0.6、`bubbles/v2` 2.1.0、`lipgloss/v2` 2.0.3，以及更新的 `ultraviolet`、`x/ansi`），吸收已有的 renderer 和终端行为修复。
- TUI：Write 工具卡片不再展示 diff 预览。结果现在显示清晰的行数+字节数摘要（`Successfully wrote N lines, N bytes`），而非从 unified diff 中提取新增行来展示，避免「只写了 3 行」的误导显示。
- TUI：移除了 Write 工具的预读 + diff 生成流程。Write 工具结果不再携带 `Diff`、`DiffAdded`、`DiffRemoved` 元数据。Edit 工具结果继续正常展示 diff。
- Write 工具 `Execute` 的成功消息现在同时报告写入行数和字节数。
- Runtime/Codex 限流：当 Responses WS 活跃流在一段时间内没有新的 `rate_limits` 事件时，Chord 现在会主动唤醒 Codex 轮询刷新，确保 RATE LIMIT/用量面板及时更新，避免长时间显示陈旧窗口。
- Thinking translation：当某个翻译模型返回语义上为空的结果时，Chord 现在会继续尝试模型池中的下一个模型，而不是在第一个模型空译文时直接失败。
- Tools/Safety：Bash 与 Spawn 现在会在执行前拒绝高置信度交互式 shell 命令，并注入明确的 non-interactive 环境默认值（例如禁用 git 凭据/编辑器提示）以降低卡死风险。
- Tools/进程生命周期：超时/取消后的回收流程现会在宽限期后从 graceful terminate 升级为 force-kill 进程组，避免顽固子进程导致 Bash/Spawn 调用悬挂。

## 0.5.0 - 2026-05-08

- Config：agent 配置现用 `mode: main` 表示 MainAgent 角色，`mode: subagent` 表示 SubAgent。`sub_agent` 和 `sub` 也可作为 SubAgent 别名；空值或其他值按 `main` 处理。Hook `agent_kind` 过滤器使用 `main` / `subagent`。
- CLI：新增 `chord import`，支持从 Claude Code（`claude`）、Codex（`codex`）和 OpenCode（`opencode`）导入外部会话。导入生成可 `/resume` 的 Chord session 及 `import-report.json`；Codex 默认使用保守的 `auto` 工具导入，OpenCode 默认以纯文本导入工具历史，Claude 默认 `auto`。
- Runtime：新增请求前的模型兼容标准化——切换 provider/model 时对历史中 provider 专用 payload（如 Anthropic signed thinking、结构化 tools）进行安全回放或降级，避免协议错误。
- Runtime：修复潜在卡死：工具 batch/turn 在等待共享流式工具执行配额（slot）时被取消，现在会生成一条 cancelled 的工具结果，确保 PendingToolCalls 归零、界面不会卡在 busy。
- TUI：修复运行中工具/Bash 的 spinner 动画：现在每次 visual animation tick 推进一帧，而非按 wall-clock 时间采样，避免确定性跳帧和旋转不均匀；后台 active 会话保持与前台一致的视觉 spinner cadence，同时保留较慢的内容 flush。
- TUI：修复 agent 忙碌时通过 `/models` 选择器切换模型池的时序。现在选择器立刻将切换请求提交到主事件循环，已排队的用户消息在下次实际发起请求时使用新池，无需等到再次发送 draft 或完全回到 idle。
- Worktree：改进 `chord worktree finish` 的失败可操作性。若目标 worktree 已存在进行中的 rebase，`finish` 提前退出并提示先收尾当前 rebase，避免启动嵌套 rebase；若 rebase 出现冲突，错误信息给出分步恢复命令（`git status`、`git rebase --show-current-patch`，再按情况选择 `--skip` / `--continue` / `--abort`），并基于 `git cherry -v` 提供「可能是冗余提交」的提示，辅助判断何时可安全 `--skip`。
- TUI：Chord 在后台运行时，当前聚焦的 Agent 从 busy 变为 idle 后，终端标题栏显示一次性的 `✅` 完成标记；重新聚焦终端会清除该标记。
- TUI：修复 reasoning 输出很快切换到 assistant 正文时 THINKING 卡片的顺序问题。现在 Chord 在发送首段正文前先关闭并 flush 已缓冲的 thinking；正文开始后到达的 late reasoning 不再重新打开 THINKING，因此 THINKING 不会出现在答案正文下方。
- TUI：修复 input separator 上方偶发的「双横线」。当 viewport 锚定到对话底部、最后一个 block 是带 `MarginBottom(1)` 的卡片（assistant / user / thinking / tool / compaction summary）时，卡片透明的 marginBottom 行与上方带 dim 背景的 padBottom 行形成颜色台阶，在 input separator 上方看起来像第二条横线。现在卡片纵向 margin 区间继承卡片背景色，横向 marginLeft / marginRight 仍透明，卡片左缩进视觉效果不变。

## 0.4.0 - 2026-05-07

- 在默认 TUI 命令和 `chord headless` 中新增 `--worktree [name]`：启动前创建或进入 chord 管理的 git worktree。worktree 路径为 `<state-dir>/worktrees/<repo-id>/<slug>`，分支名 `chord/<slug>`（若配置了 `worktree.branch_prefix` 则为 `<branch_prefix><slug>`）；每个 worktree 拥有独立的 ProjectKey，session、runtime cache 与 exports 自动隔离。`--worktree` 可与 `--continue` / `--resume` 组合，作用域为该 worktree 自身的会话。值为空时按 `task-YYYYMMDD-HHMMSS` 自动命名；分支已挂在某个 worktree 时会直接复用（fast resume）。`chord headless` 启动时若使用 `--worktree`，`ready` 事件 payload 新增 `worktree: { name, branch, path, repo_root }` 字段。
- 新增 `chord worktree` 命令组：`list`（按最近使用排序列出当前仓库的 chord 管理 worktree）、`remove <name>`（删除 worktree 目录及其 sessions/cache/exports，默认保留分支；`--delete-branch` 仅在已合并时删除分支，`--force` 强制删除脏工作区和分支）。创建/进入 worktree 属于启动级动作，由 `chord --worktree` flag 承担、不归属 `chord worktree` 子命令；如需「进入并继续」，使用 `chord --worktree <name> --continue`。
- 新增 `chord resume <session-id>`：根据 session metadata 自动定位会话所在的 worktree（或主仓库），切换目录后继续；与仅在当前项目内恢复的 `chord -r <id>` 互补。
- `config.yaml` 新增 `worktree.branch_prefix`：覆盖默认的 `chord/` 分支前缀（同时影响 `git worktree list --porcelain` 的过滤）。空值回退为 `chord/`；末尾未带 `/` 时自动补齐；会产生非法 git ref 的取值（以 `/` 或 `-` 开头、包含 `..` / `//` / 空白字符，或含 `[a-zA-Z0-9._/-]` 之外的字符）会在启动时直接报错。
- 扩展每会话的 `session-meta.json`：新增 `repo_id`、`repo_root`、`worktree_name`、`worktree_branch`、`worktree_path`、`is_main_worktree` 字段。已有 session 保持兼容；仅含 worktree 字段的元数据文件现在能被正确识别。
- 新增 Google Gemini 一等公民 provider（`type: generate-content`，`api_url` 以 `/models` 结尾）：支持流式文本/工具/思考输出、多模态内联图片、function calling 工具，以及带 `Retry-After` 解析的 Gemini 错误响应。
- 修复本地 slash 命令（`/export`、`/models`）：现始终在主 agent 的事件循环中执行，而非从 TUI 输入 goroutine 直接调用。此前在 LLM 重试中途提交这两个命令可能清掉当前 turn，使界面卡在「忙碌」状态且无法取消；同时 cancel-busy 按键路径在 agent 报告无活动 turn 时也能正确恢复。
- 修复 slash 命令补全下拉列表：命令数量超过 8 个时，用上下键选择会自动滚动可见窗口，确保当前选中命令始终可见。
- 修复 `/new`：执行后清空侧边栏 EDITED FILES 区域，不再保留上一 session 的文件列表。

## 0.3.0 - 2026-05-07

- 新增运行时模型池（model pool）：
  - **不兼容变更：** agent 模型配置现在必须通过 `model_pools` 引用 `config.yaml` 顶层定义的一个或多个池；旧的 per-agent 扁平 `models` 列表不再接受。内部 `AgentConfig.Models` 表示为 `map[string][]string`（池名 → 有序模型引用列表）。
  - 所有 agent 必须定义至少一个池。池名由用户自定义且无保留字，如 `default`、`base`、`fast`、`strong` 均为合法池名。未显式选择池时，Chord 回退到该 agent `model_pools: [...]` 列表中的第一个池。
  - Agent 可通过 `model_pools` 复用全局定义的池；配置中的内联 `models` 与 `model_pools` 互斥。运行时池策略按项目持久化当前角色池、按 agent 覆盖及 last-picked 状态。
  - `/model` 命令替换为 `/models`，支持 `status`、`<pool>` 和 `--agent <name> <pool>` 子命令。TUI 选择器现在选择模型池而非单个模型。
  - agent 忙碌时选择模型池会立即提交到主事件循环。当前已发起的请求继续使用开始时快照到的 client，而排队用户消息和其他后续请求边界在无需再次发送 draft 或完全回到 idle 的情况下使用新池。pending switch 失败现通过 TUI 的 Update 消息路径回流处理，不再由后台 goroutine 直接改动视图状态。
- Diagnostics 与启动日志中加入构建身份信息。`chord --version`、diagnostics bundle 和 TUI dump 现包含或展示 commit、dirty 状态、注入的 build time、VCS time、Go 版本和可执行文件 mtime 等信息；MCP client info 继续使用精简的应用版本号。
- 修复 SKILLS 侧边栏状态：`Skill` 工具加载失败不再被标记为已加载（绿色），未发现/不存在的 skill 不再显示，且移除旧的 "(loaded)" 后缀。
- 修复 Codex RATE LIMIT 信息面板：倒计时到期后不再卡在 "1s"；窗口到达 reset 时间点时隐藏倒计时，并触发尽力而为的用量刷新，使新窗口尽快更新展示。
- 修复 TUI diagnostics/export 状态卡延迟显示：assistant 流式输出期间排队的状态卡现在在当前 assistant 卡片结束后立即出现，不再等到 agent idle。
- 修复权限确认的编辑/拒绝理由输入区无法通过 `Cmd+V` 粘贴剪贴板文本的问题。

## 0.2.0 - 2026-05-05

- 重构 TUI 渲染缓存布局：`viewCacheState` 现仅包含可安全批量清零的 draw 循环缓存，动画、ticker、本地 shell 和 startup transcript 相关运行态移入独立 runtime state，并会在 `invalidateDrawCaches` 后保留。缓存失效逻辑仍可对缓存结构体整体清零（同时保留 `cachedMainSearchBlockIndex = -1` 不变量），不再逐字段写约 80 行的归零语句。删除从未被读取或赋值的 `cachedDirKey`、`cachedHelpKey`、`cachedStatusActivitiesKey`、`cachedStatusChordDisplay`、`cachedStatusSessionSwitchKey` 字段；将 5 个 `renderSlashCache*` 字段合并为子结构体 `slashRenderCache`（`m.slashCache`）；将 `OverlayList` 与 `OverlayTable` 中重复的 `renderVersion / renderCacheWidth / renderCacheText / renderCacheValid` 四元组抽成共享的 `widthKeyedRenderCache` 嵌入字段。
- 拆分 `agent.AgentForTUI` 接口为按职责划分的子接口（`MessageSender`、`PromptResolver`、`ModelSelector`、`SessionController`、`SubAgentInspector`、`LoopController`、`RoleController`、`UsageReporter`、`KeyHealthReporter`、`CompactionController`、`PlanExecutor`），原 `AgentForTUI` 通过组合这些子接口得到。现有实现（`MainAgent`、headless adapter、TUI 测试 stub）和消费方继续满足组合后的接口；新增 TUI 消费方应直接依赖更小的子接口，而非依赖整个 `AgentForTUI`。
- 重构 `MainAgent.Shutdown`：将原本约 170 行的单函数拆为 `cancelActiveWork`、`closeSubAgentMCPServers`、`buildShutdownSnapshot` 三个独立 helper，主函数压缩至约 92 行，各阶段顺序与 budget 处理可独立审计。
- 移除未使用的 `tools.TruncateOutput` 包装函数（包内调用者已全部迁移至 `TruncateOutputWithOptions`）。仓库外调用方需切换至 `TruncateOutputWithOptions(output, sessionDir, tools.TruncateOptions{})` 以保持原有行为。

- 改进 Pyright LSP 配置体验：未显式配置 Python 解释器时，Chord 按当前平台的 virtualenv 布局自动发现项目本地 `.venv`、`venv`、`env` 解释器（类 Unix/WSL 使用 `bin/python`，Windows 使用 `Scripts\python.exe`）；相对的 `python.pythonPath`、`python.defaultInterpreterPath`、`python.venvPath` 按 LSP root 规范化为绝对路径；`workspace/configuration` 也按 section 返回配置，确保 pyright-langserver 能正确读取 `python` 配置。不兼容变更：通过 `workspace/configuration` 提供的 LSP `options` 现对所有 LSP server 都必须按 section 组织，而不仅是 Pyright；对于 Pyright，请使用 `python`、`python.analysis` 这类嵌套键，而非旧的扁平顶层键。
- 移除已废弃的 headless `notification` envelope 类型：删除 `protocol.TypeNotification` 与 `protocol.NotificationPayload`，并从 headless 订阅白名单中移除 `"notification"`。无任何代码路径会发出该 envelope，gateway 应基于 `idle` envelope 自行渲染 ready/idle 状态。
- 将运行时日志从原先的 `slog` 风格适配层全面迁移为直接使用 `golog`。日志现为 golog 原生纯文本输出，并由 golog 直接记录调用位置；此前伪结构化的 `level=... msg=... key=value` 格式及默认 logger 的 `With(...)` 上下文字段不再自动输出。
- 修复带图用户消息通过 `ee` / fork 编辑后再次发送时，来自会话历史路径恢复的图片不会被重新读入并随消息发送的问题。
- 修复 TUI 工具卡片渲染：工具参数/结果按终端安全的纯文本展示，ANSI/control sequence 会被转义；看起来像 Markdown 的普通工具输出不再自动按 Markdown 渲染；超大的折叠 Bash 结果不会再 wrap 隐藏尾部；折叠状态的 hidden-line 提示也不再重复计算第一条隐藏行。
- 删除一批 1.0 前不应继续保留的兼容路径与死代码，覆盖 compaction、LLM 会话处理、仅供测试的 LSP/helper、tools 与 TUI 内部实现。此次清理移除了未使用的 `ResetResponsesSession` / 旧 responses-session reset 链路，删除了旧的同步 compaction fallback 路径，将仅测试使用的 helper 迁入 `_test.go`，抽取了 fallback summary 共享渲染，并完成了工具名向 `tools.NameXxx` 常量的统一收口；同时补齐了 plan execution 新会话路径上的 session identifier 同步。
- 修复长会话中的 TUI 转录区裁剪：较早的后台状态卡在 spill/hydrate 恢复后再接收晚到更新时，现会先恢复并重算转录高度，避免底部若干行甚至最后几张卡片无法滚动到。
- 删除 SubAgent `Complete` 工具参数及 `CompletionEnvelope` 中已废弃的 `blockers_remaining` 字段；SubAgent 应使用 `remaining_limitations` 报告非阻塞遗留事项，真正的阻塞需走 Escalate 或 `blocked` mailbox 流程，而非直接 `Complete`。
- 统一 SubAgent artifact 表示：mailbox 消息、durable task 记录、实例 meta 文件及内存中的运行时状态现统一通过 `ArtifactRef` / `[]ArtifactRef` 引用 artifact；删除并行的 `artifact_ids` / `artifact_rel_paths` / `artifact_type` 字段及配套的旧适配函数。
- 将 TUI 渲染、搜索、hooks、agent 执行路径和编辑追踪中残留的 `Read` / `Write` / `Edit` / `Delete` / `Grep` / `Glob` 字面量替换为集中维护的 `tools.NameXxx` 常量。
- 删除无调用方的 `skill.Loader.Scan()` 包装方法（现有调用方已使用 `ScanMeta` 加按需 `Load`）。
- 改进 MCP initialize 握手元数据：运行时管理的 MCP client 现发送 build-time 注入的真实 Chord 版本，不再使用陈旧的硬编码版本；同时保留默认的 `mcp.NewClient` / `NewPendingManager` / `NewManager` 便捷入口，并新增显式 `WithClientInfo` 变体，供需要自定义握手身份的调用方使用。
- 将 TUI 展开逻辑和 compaction 用到的本地工具 trait（`Read` / `Grep` / `Glob` / `WebFetch` 与文件修改类工具）集中到 `internal/tools/tool_traits.go`，减少散落的字符串分支。
- 删除历史保留的 `ProviderConfig.UpdatePolledRateLimitSnapshot` 测试兼容包装方法，统一改为显式调用 `UpdatePolledRateLimitSnapshotForCredentialIndex`。
- 新增结构化 SubAgent 完成交接信息，支持记录实际修改文件、已运行验证、剩余限制、已知风险、推荐后续事项和 artifact 引用。
- 修复 TUI 工具卡片：排队徽标与换行内容现保持一致的右侧留白。
- 新增会话范围内的 `SaveArtifact` / `ReadArtifact` 工具，用于 SubAgent 交接 artifact，并支持通过 mailbox、task registry、snapshot 和会话恢复持久化。
- 改进 SubAgent 协调快照：展示近期完成信息、artifact 引用、写入范围和疑似停滞原因。
- 修复 TUI 转录区在长会话里可能逐步漂移的问题：会导致最后一张卡片/内边距被裁剪，且鼠标拖选命中到错误的行。
- 修复 TUI 转录区鼠标拖选复制：用 `Cmd+C` 复制时，拖选文本会保留最后一个字符；同时补充了转录区复制行为说明。
- 改进 loop 验证续跑：`verify` assessment 现在注入专用 `LOOP VERIFY` notice，并明确提示运行相关验证；同时文档补充 `/loop on [target]` 用法。
- 修复 LSP 侧边栏诊断：编辑后 self-review 若已清零，会持久化 `0E/0W` snapshot，避免语法错误修复后仍显示旧错误。
- 修复 TUI 卡片在 emoji、variation selector 和 ZWJ 组合字符附近出现背景色异常的问题：wrap、padding、truncate 现与 viewport 的 grapheme-width 计算保持一致。
- 改进 TUI 工具调用中的本地路径显示：`Read`、`Write`、`Edit`、`Delete`、`Grep`、`Glob`，以及 Bash 中当前已可见的路径元信息，会在可能时优先显示相对于当前活动项目根目录的路径；恢复会话、启动时恢复和 spill/hydrate 恢复后保持同样逻辑，项目根之外的路径仍显示绝对路径。
- 改进 AGENTS.md 处理：仅在检测到仓库指令存在时，才在 stable system prompt 中加入一小段 framing；AGENTS.md 正文仍保留在 session `<system-reminder>` 上下文层。
- 修复 sticky fallback 模型的 variant 状态：已 pin 的 fallback 请求保留自身 `@variant`，且不会将主模型的 variant 泄漏到无 variant 的 fallback 运行中。
- 修复分类后的循环阻塞消息会渲染成未命名状态卡的问题。
- 修复 Ghostty/tab 恢复焦点后的界面残影：现跟踪终端处于后台期间发生的转录区/布局变化，并在 focus-settle 后检测到这些后台变化时强制触发 host redraw；同时在 diagnostics 中记录 background-dirty 与输入分隔线位置，便于继续排查残留的 stale-display 场景。
- 修复 Ghostty/cmux 在快速滚动/resize/布局变化后分隔线偶发显示为两条的残影问题：在这些终端下，Chord 现对每一行渲染结果追加「清到行尾」，避免空行遗留旧 cell。
- 改进排队中的工具调用徽标：保持右侧留白，并在工具标题宽度不足时隐藏。
- 改进 assistant/thinking 流、压缩摘要和状态卡的 TUI Markdown 渲染缓存。
- 修复类似 Markdown 的工具输出在折叠状态下隐藏行数计算不准确的问题。
- 修复 headless idle 事件：Chord 现只发送一个 `idle` envelope，不再额外发送重复的 ready `notification` envelope；gateway 应自行将 idle 状态渲染给用户。

- 修复一组 compaction 后续行为问题：恢复/保留终端标题时，更可靠地忽略被 compaction 摘要污染的首条消息；自动 compaction 在 continuation barrier apply 失败时不再重复发送 idle 转换；活动标题动画在恢复 spinner 驱动状态前始终重新同步 terminal-title ticker。
- 调整自动（usage / 阈值触发）compaction 的续期行为：durable summary 应用完成后，agent 现主动在压缩后的上下文上启动新的 LLM turn 继续推进任务，而非回到 idle 等待用户再次输入。手动 `/compact` 仍返回 idle。loop 模式在自动续 turn 时同步推进 loop state。
- 改进 compaction 后会话列表预览/终端标题的准确性：不再通过文本内容推断 compaction 摘要，而是在 `usage-summary.json` 和 session summary 中持久化显式元数据（`*_is_compaction_summary` 标志）并据此决策。不兼容变更：旧版本创建/压缩的会话可能仍显示被污染的标题/预览，直到用本版本再次进行 compaction。

## 0.1.0 - 2026-04-29

- Chord 首次公开发布。
- 提供本地优先的终端编码 Agent，包含 Vim 风格导航、会话管理、模型/服务商配置、工具执行、LSP 集成、图片输入和 headless 远程控制能力。
- 增加 macOS、Linux 和 Windows 的跨平台发布构建。
