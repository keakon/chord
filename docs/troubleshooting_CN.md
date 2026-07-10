# 常见问题排查

聚焦安装、配置、认证、会话、扩展和性能相关的常见问题。

## 启动失败

先看看这几项：

- Go 版本是否满足要求
- 是否使用了正确入口：`go run ./cmd/chord/`
- `config.yaml` 是否缺失或已损坏
- `auth.yaml` 是否存在明显的 YAML 格式错误

如果 `config.yaml` 缺失，请在交互式终端里先运行一次 `chord` 来启动初始化向导。即使 stdin 被重定向，只要还能打开控制 TTY，向导仍会在那里运行；只有没有控制 TTY 时，Chord 才会立即返回初始化错误。若 `config.yaml` 已存在但 YAML 损坏，请先修好文件；向导只会在缺失配置时触发。

如果是构建好的二进制，建议重新运行并查看终端错误输出。

## 401 / 403 / 认证失败

检查：

- `auth.yaml` 中 provider 名称是否与 `config.yaml` 对应
- API key 是否有效
- OAuth provider 是否配置了 `preset: codex`

可以用以下命令排查：

```bash
chord doctor models
```

如果要缩小到明确的模型或模型池：

```bash
chord doctor models --model openai/gpt-5.5@high
chord doctor models --pool thinking
```

### OAuth 账号池很大时启动慢

对于 `preset: codex` 的 OpenAI / ChatGPT OAuth provider，`auth.yaml` 里可能有数百或上千个账号。启动路径不会同步解析每个 access token 的 JWT，也不会因为某个 token 缺少 `account_id` 阻塞 provider 初始化：Chord 会先把 access token 加入可用 key pool，然后在后台解析和回填 `account_user_id`、`account_id`、`email`、`expires` 等 metadata。

注意区分两类情况：

- 个人 Plus/Pro 账号可能只有 `user_id`，没有 `chatgpt_account_id`。这类账号仍可用于普通请求，但不会发送 `ChatGPT-Account-ID`，也不会参与依赖 account id 的 Codex usage / rate-limit polling。
- 若日志显示 `account_user_id mismatch` 或 `account_id mismatch`，说明配置里显式写出的身份和 token 自身能解析出的身份冲突；这类应修正或移除对应 credential。

如果手动转换 Codex/sub2api 导出的账号，建议保留能拿到的 `email`、`account_id`、`account_user_id`，但缺失这些字段不应导致启动失败。需要逐项诊断时再运行 doctor/检查命令，而不是把完整 JWT 扫描放在启动热路径。

## 429 / quota exhausted

常见原因：key 已达配额上限、provider 限流、并发或高频请求触发了速率限制。

建议：换一个 key、降低并发或减少重试、检查是否存在异常循环调用。

如果要判断哪些 key 或模型反复限流 / 报错：

- 在 TUI 的 Normal 模式下按 `Ctrl+E` 打开错误面板；这里会记录包含 429 在内的重试错误，并显示 provider、model 和 key 后缀。
- 看错误面板里的模式：如果总是同一个 key 返回 429，通常是这个 key 被限流；如果同一 provider 的多个 key 都返回 503，更可能是 provider 侧异常。

界面说明：

- 右侧 RATE LIMIT 面板展示的是 Codex 最近一次用量/限流快照（如 `5h: 42% 2h30m`）。到达 reset 时间点后倒计时会短暂消失，Chord 触发一次用量刷新；由于服务端可能使用滚动窗口，刷新后百分比不一定立即变成 0%，可能是逐步下降。
- Codex OAuth 运行时状态也会在其它 Chord 进程更新 `auth.state.json` 后自动重新加载，因此额度快照、reset 计时、账号元数据和账号状态变化无需重启当前会话也应能生效。
- 如果 RATE LIMIT 面板长期不更新，可打开 `log_level: debug`，在 `chord.log` 中搜索 `responses codex ws: rate_limits event ...`（收到事件）或 `responses codex ws: rate_limits event ignored ...`（事件未识别/解析失败）。

## TUI 启动了但无法正常请求

检查：

- 当前 provider / model 是否存在
- 网络是否能访问对应 API
- 代理配置是否生效

例如：

```bash
curl -I https://api.anthropic.com
curl -I https://api.openai.com/v1
```

### OpenAI 兼容网关的 400 与超时

对于按官方 API 语义工作的 provider 端点，HTTP 400 通常表示请求本身无效，Chord 会停止而不是反复重试同一个错误请求。这类 provider 请设置 `official_api: true`。如果是聚合或代理网关，并希望 Chord 把未知 400 视为可能可恢复的网关错误，请设置 `official_api: false` 或不配置该字段。`preset: codex` 的 provider 会自动按官方 API 处理。

对于非官方 OpenAI-compatible 网关，Chord 不会仅凭 HTTP 400 就认定请求本身有问题。很多网关会把上游过载、限流或 provider 失败统一包装成 400，因此非官方 400 默认会被视为可重试：当前 key 会进入最多 1 分钟的短冷却，然后 Chord 可以尝试其它 key、model 或下一轮重试。只有明确的请求形状错误信号，例如结构化的 `invalid_request` / `invalid_parameter` 代码，或“缺少必需参数”这类清晰文案，才会立即停止。

如果连接建立超时，或在超时前没有收到首 token，Chord 会把当前 key 标记为 recovering，使下一次重试优先选择其它健康 key。

Responses HTTP provider 的初始 `connecting` 阶段也有边界。如果上游或网关迟迟不返回 HTTP response header，Chord 会在约 25 秒后让本次尝试失败，而不是无限等待；随后正常进入重试 / fallback 路径。这个限制只针对等待 response header 的阶段；健康流一旦开始，后续仍由 stream-idle timeout 管理，不是固定总请求时长。

### DeepSeek / OpenAI 兼容 thinking 模式 400

如果你使用的是 DeepSeek 这类 `chat-completions` provider，并看到下面这类报错：

- `The reasoning_content in the thinking mode must be passed back to the API.`
- `Invalid assistant message: content or tool_calls must be set`

通常说明这个 provider 要求把上一轮工具调用里的 thinking/reasoning 内容按严格的 assistant message 形状一并带回后续请求。如果同一类报错持续重复，请保留对应的 session dump / trace 供排查。

请在受影响的 model 或 provider 上启用
`compat.reasoning_continuity.mode: openai_visible`。该选项只负责回放
assistant `reasoning_content`；provider 专属思考字段应通过
`compat.request_overrides.body` 添加。

GLM Preserved Thinking 的 body override 需要包含 `thinking.type: enabled` 和
`thinking.clear_thinking: false`；DeepSeek 只需要 `thinking.type: enabled`。
两种情况下，回放的 `reasoning_content` 都必须保持完整、未修改且顺序不变。

### Codex WebSocket 400 "No tool call found for function call output"

Codex WebSocket 传输按 `previous_response_id` 发增量请求，服务端在该 id 下保存自己侧的对话快照。如果本地拼出的 input 与该快照对不齐（例如两轮之间 request signature 发生变化），即便本次发送的 input 里 `function_call` 与 `function_call_output` 是配对完整的，服务端依然可能返回 `400 No tool call found for function call output with call_id …`。

Chord 现在识别到这类 400 时，会清空 WebSocket 链状态（`previous_response_id`、baseline、signature），并在同一个 WebSocket 上立即以**全量、不带 `previous_response_id`** 的方式重发一次。这等价于让服务端基于当前完整 input 开启一段新对话，可就地修复链状态不一致而无需走 HTTP 兜底。只有原始 400 被判定为链状态不一致才会触发该重试；重试仍失败说明 input 本身存在问题，HTTP 路径会以同样的 input 失败，因此不再进一步回退、直接返回错误。

绝大多数情况下用户无需任何操作 —— 该恢复是自动完成的。如果某次会话中该错误反复出现，请保留对应 trace 和 session id 以便排查会话内容。

## MCP 一直未就绪

先确认：

- MCP 地址是否可访问
- 配置名称是否正确
- 本地模式下是否仅是异步初始化尚未完成

启动后短暂显示灰色的 pending 状态不一定是错误。

## 会话空闲一段时间后，LSP / MCP 行变成灰色

如果右侧环境面板里的 LSP 或 MCP 行在 agent 空闲一段时间后变成灰色，这**不一定**表示集成坏了。

Chord 会在会话空闲时主动卸载空闲的 LSP / MCP 运行时资源，以降低后台占用。处于这种状态时：

- 行会显示为灰色的 idle 状态，而不是错误色；
- 这表示资源是因空闲而被**主动卸载**，不是连接或配置一定出错；
- 下一次真实请求 / busy 周期开始前，Chord 会先恢复这些运行时依赖，再重建请求面。

只有当该行持续显示红色、明确给出连接/配置错误，或下一次请求时仍恢复失败，才应把它当作故障排查。

## 写文件后没有诊断

已配置 LSP 但写文件后没有看到诊断：

- 检查本机是否安装了对应语言服务器
- 检查 `lsp` 配置格式是否正确
- 确认目标文件类型与 `file_types` 是否匹配
- 检查是否通过 `diagnostics.enabled: false` 关闭了工具后诊断

Python 还需要注意：

- 小文件使用 `diagnostics.python.semantic_backend`，通常是 `lsp.pyright`。请确认 `diagnostics.python.semantic_backend.server` 与 `lsp` 下的 server key 一致。
- 大 Python 文件在 `PATH` 中能找到 `ruff` 时使用 Ruff quick diagnostics。
- 如果大 Python 文件提示因为 Ruff 不可用而跳过诊断，可以安装 Ruff，或设置 `diagnostics.python.large_file.run_semantic_when_quick_unavailable: true`，强制大文件也运行 Pyright。
- Ruff quick diagnostics 不更新 LSP 侧边栏，只出现在 `edit`、`patch` 或 `write` 工具结果中，并会明确提示完整 Python 语义诊断已跳过。

推荐 Python 配置骨架：

```yaml
lsp:
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]

diagnostics:
  python:
    quick_backend:
      type: command
      command: ruff
```

完整推荐配置见 [配置 — 工具后诊断](./configuration_CN.md#工具后诊断)。

## 会话恢复异常

`--continue` 或 `--resume` 没按预期工作：

- 确认当前目录与原会话是否属于同一项目
- 尝试显式使用 `--resume <session-id>`
- 检查是否只是恢复过程较慢而非真的丢失

## 会话恢复 / restore 行为说明

对于 `--continue`、`--resume`、新建会话、fork 会话和 plan execution：

- 先确认当前目录与原会话属于同一项目
- 需要精确恢复目标时，优先使用 `--resume <session-id>`
- 如果恢复后的 model/provider 状态异常，请保留相关 session 日志和 trace 供排查

会话恢复本身不受影响。内部传输层的清理不会影响 `--continue` 或 `--resume` 的正常使用。如恢复后出现异常，请保留当前版本的日志供排查。

会话恢复时，Chord 还会在把对话送入 provider 之前修复结构性破损：

- 若尾部 assistant 消息的 `stop_reason=interrupted`（进程在流式输出中途被中断），会被丢弃，由下一轮 user/system 触发一次新的 assistant 回复
- 对每个未持久化匹配 tool 结果的 assistant `tool_call`，会在其位置追加一条合成的 `error` tool 消息（`ToolStatus=error`），使要求严格 `function_call ↔ function_call_output` 配对的 provider（OpenAI Responses、Anthropic `tool_use`/`tool_result`）能接受该 input

仅做结构修复 —— 已持久化的 tool 消息文本和 `ToolStatus` 不会被改写。tool 输出里恰好出现 `denied`、`cancelled` 等词的内容不会再被反向解读为失败。

## 查看日志 / dump / shell 输出时，TUI 卡片出现异色、背景泄漏或换行错乱

查看诊断 dump、原始命令输出或其他外部文本时，工具卡片、本地 shell 结果、问题对话框或确认摘要出现异常颜色、背景泄漏或换行错乱：

- 重新执行同样的 `read`、`shell`、`web_fetch` 或本地 shell 操作
- 如果仍能复现，同时保留原始文件/输出和截图

Chord 会在这些界面中按字面显示含 ANSI 的外部文本，不再次执行其中嵌入的终端 escape/control sequence；这也包括裸 `\r` 进度刷新文本。这样既能查看原始序列内容，也不会让诊断 dump 或其他原始终端输出污染周围卡片的渲染。普通工具结果即使包含看起来像 Markdown 的标题、列表、表格或代码块，也会按纯文本处理，避免日志、diff、JSON/YAML 或抓取页面被意外重新排版。

## 输出触发 TUI 渲染 panic / 进程被 killed

如果外层只显示：

```text
Error: program was killed: program experienced a panic
```

并且当前会话需要重新 `--resume` 才能继续，先查看 `~/.local/state/chord/logs/chord.log` 末尾的 Go panic 栈。`main.jsonl` 通常不会保存这句 panic，因为它发生在 TUI 渲染层，而不是作为会话消息写入。

如果栈里出现以下路径，优先按 TUI markdown/ANSI 渲染问题处理，不要直接归因到模型或工具本身：

```text
github.com/charmbracelet/x/ansi.(*Parser).Advance
charm.land/lipgloss/v2.(*WrapWriter).Write
charm.land/glamour/v2/ansi.(*HeadingElement).Finish
github.com/keakon/chord/internal/tui.renderMarkdownContent
```

这类问题可能发生在 assistant、tool report、content viewer 或 compaction summary 渲染 markdown 时。已知上游修复版本是：

- `charm.land/glamour/v2 >= v2.0.1`
- `charm.land/lipgloss/v2 >= v2.0.4`

排查 dump 时注意：

- 如果崩溃前最后一个 shell/tool 结果已经写入 `main.jsonl`，通常说明该工具输出没有丢。
- 如果崩溃时正在等待 LLM SSE 流，`dumps/llm/*.json` 里可能只有 `request_body`、部分 `sse_chunks` 和 `reading SSE stream: context canceled`，表示该次 LLM 响应只 dump 到中途，没有完整 final text。
- `context canceled` 多数是进程关闭后的结果，不一定是根因。

稳定处理原则：Chord 应先升级上游 renderer 依赖修复已知根因，同时在 TUI 渲染边界保留 `recover` / fallback。面对模型输出、工具输出和历史 session 内容这类不可信输入，渲染 helper 必须 best-effort：宁可降级为纯文本，也不能让 panic 穿透到 Bubble Tea 主循环并杀掉整个进程。

## 切换 tab 或重新获焦后画面错乱

切换 tab、切回终端窗口或重新获得焦点后，TUI 偶发出现旧行残留、横线伪影或工具卡片局部错位：

- 画面已错乱时，轻微调整终端窗口尺寸或切走再切回，通常可强制触发一次完整重绘
- 如果仍能复现，同时保留 diagnostics bundle 和截图

如果现象主要发生在**获焦后的流式输出过程中**，请同时保留 diagnostics bundle 和截图，方便维护者对比 Chord 最近渲染的 frame 与终端实际可见输出。

补充：画面错乱时看到类似 `;250m pyright` 的残片，通常不是 LSP 内容，而是被截断的终端控制序列（ANSI/OSC）尾部字符。

### 重复分隔线 / 旧边框残留

如果主要现象是横线重复、输入区或状态栏分隔线重复、旧卡片边框残留，或右侧栏旧边框残留：

1. 先截图，不要先调整窗口尺寸。截图应包含完整终端窗口，尤其是输入区、状态栏和右侧栏。
2. 立即导出 diagnostics bundle（`Ctrl+G`），尽量在 resize 之前完成——bundle 记录了 Chord 最近渲染的 frame，维护者据此能区分是 Chord 画出的重复线还是终端残留伪影。
3. 把两者连同终端名称和版本一起附在反馈里。

两个本地观察也有助于缩小范围：

- 把终端宽度缩小一两列后这条线就消失的话，请在反馈里说明——这指向右边界 wrap 行为。
- 伪影出现在图片预览、粘贴图片或导出 diagnostics 之后的话，也请说明。

Chord 会禁用终端硬滚动优化，因为这些序列可能在 Chord 的 sticky transcript 布局中留下残影；剩余报告多数最终归因于终端自身的重绘行为，「截图 + bundle」这对证据是定位的关键。

## 长会话里转录区底部内容滚不到

看到最后几行转录内容像被裁掉、最后一个卡片几乎贴着输入分隔线，或已经滚到底但最新对话仍有一部分不可见：

- 留意问题是否出现在长会话中的后台任务结束或状态卡更新之后
- 如果仍能复现，同时保留截图和日志，便于比对转录状态与底部渲染结果

## 文件编辑工具提示文件在观察后发生变化

这个警告来自 Chord 进程内的文件跟踪：当前文件已经不再匹配本 agent 上次记录的内容哈希。Chord 不再因为每一次 stale 文件编辑直接拒绝；`edit` 会基于当前文件内容做 old/new 匹配，`patch` 会基于当前文件内容验证 hunk，`write` 和 `delete` 会在继续前备份有风险的非空写前内容。

常见原因：

- 你在 `read` 与 `edit` 之间用编辑器/格式化器等外部进程改动了文件；
- speculative 工具调用被丢弃/回滚，finalize 阶段的工具调用与其发生竞态；
- provider 将 tool arguments 以 JSON 字符串形式包了一层（wrapped arguments）。Chord 会一致地对 tool arguments 做 unwrap；如果路径没有被正确跟踪，请保留日志和 session JSONL。

如果创建了备份，工具结果会包含当前会话目录下的备份路径。空文件和无风险的连续 agent-owned 编辑不会创建备份。备份上限为每 path 10 个、每 session 200 个、单文件 10 MiB、每 session 总计 50 MiB；如果必须备份但超过这些上限或因其他原因失败，编辑仍可继续，但工具结果会说明未创建备份及原因。会话目录被删除时，备份也会随之清理。

## Patch 报 `hunk not found` 或 `matched multiple locations`

`patch` 按行匹配 hunk，并应用当前搜索位置之后的第一个匹配。它可以容忍常见空白和 Unicode 标点差异，但重复块仍需要足够的邻近上下文，让目标位置明确。

看到这个错误时：

- 重新 `read` 目标文件，并基于最新内容重建 patch；
- 如果成功输出提示某个 hunk `matched multiple locations`，使用 note 中的候选行号去 `read` 目标位置附近，并在后续相关编辑的 `@@` hunk 中加入附近未变化的唯一上下文行；
- 如果错误提示找不到 hunk，从最新 `read` 输出中重新复制目标块，并确认 context/removal 行缩进与当前文件一致；如果 hunk 来自旧的带编号输出，先移除复制进来的行号前缀；
- 把过大的 patch 拆成更小的单文件 patch 或更小的 hunk；
- 不要通过 `shell` 执行外部 `apply_patch`；请使用 Chord 原生 `patch`，这样权限、stale tracking、diff、LSP 和 rollback 才会保持接入。

## 性能问题

感觉滚屏、流式输出或大消息渲染明显变慢：

- 先缩小当前会话上下文规模（`/compact`，或为无关工作另开新会话）
- 检查是否存在异常长输出；流式 assistant / thinking 输出会先把稳定结构（空行分段、已闭合代码围栏）走 markdown，长单段文本会先留在更便宜的纯文本路径上，直到结构稳定
- 尝试在不同终端中对比

渲染与流式输出的优化原理、以及如何采集 CPU profile 用于反馈，见[性能](./performance_CN.md)。

## 上下文压缩不触发 / 触发过频繁

**现象**：上下文使用率很高但一直没有压缩；或相反，频繁压缩影响使用体验。

排查步骤：

1. 确认 `context.compaction.threshold` 是否已设置且大于 0（0 表示关闭自动压缩）。
2. 检查 TUI 底部栏或信息面板的 `Context` 百分比。它按**可用输入预算**计算，不是按总窗口大小，所以可能比预期的低（详见 [配置 — 上下文压缩](./configuration_CN.md#上下文压缩compaction)）。
3. 如果设置了 `context.compaction.reserved`，由于会先扣除预留再应用 `threshold`，自动压缩会在更低的绝对 token 数触发；若压缩过于频繁，可检查 reserved 是否设得过大。
4. `/compact --no` 会临时关闭当前会话的自动压缩；重新启动会话或执行 `/compact` 可恢复。
5. 如果网关返回缺失 usage 或 0 usage，Chord 会用最近一次可信的非零 usage 样本和当前会进入上下文的消息 bytes 做兜底触发。开启 `log_level: debug` 后，可在自动压缩日志中查看 `estimated_input_tokens` 和 `effective_input_tokens`。

**注意**：loop 模式不会禁用自动压缩；它只会对新增消息禁用请求级上下文剪裁。

## 上下文剪裁误裁重要内容

**现象**：模型似乎"忘了"之前的工具输出，但会话文件里内容还在。

排查步骤：

1. 这是上下文剪裁（Reduction）的正常行为：每次 LLM 请求前，过时的工具输出会被从 prompt 中裁剪，但**不会修改**磁盘上的会话文件。
2. 如果你经常需要回头参考较早的读取/搜索结果，可调高 `read_like_age_turns` 和 `read_like_output_bytes`。
3. 成功 shell 输出会保留大小、行数、有代表性的成功信号行（如有）以及尾部 fallback；命令仍可从关联 tool call 获取。如果构建/测试日志仍然很重要，可调高 `shell_success_bytes`。
4. 如果希望更保守的裁剪行为，整体调高各 `*_age_turns` 和 `*_bytes` 参数。

详见 [配置 — 上下文剪裁](./configuration_CN.md#上下文剪裁reduction)。

## 请求被拒绝：`context length` / `input too large`

**现象**：provider 返回类似 "context length exceeded" 或 "input too large" 的错误。

排查步骤：

1. 确认模型 `limit.input` 和 `limit.context` 配置正确。如果 provider 公布了单独的输入上限，必须同时配置 `limit.input`。
2. 检查 `context.compaction.threshold` 是否过高导致自动压缩触发偏晚。
3. 增大 `context.compaction.reserved` 可提前触发压缩，避免请求被拒。
4. 如果频繁出现，可使用 `/compact` 立即手动压缩，或降低 `threshold` 提前触发自动压缩。
5. 在 `log_level: debug` 的日志中搜索 `oversize`，确认是否触发了 oversize recovery（压缩后再重试）。如果自动压缩已关闭，Chord 会停止并报告实际尝试过的所有候选模型都超过当前上下文，而不是无限重试。

## 何时检查日志

遇到以下问题时，优先查看日志：

- provider 请求失败但终端只显示摘要错误
- 上下文压缩未触发 / 上下文超限
- MCP / LSP 初始化异常
- hook 执行结果与预期不符
- headless 集成事件不完整

默认日志目录：`${XDG_STATE_HOME:-~/.local/state}/chord/logs/`。当前日志文件为 `chord.log`，轮转文件为 `chord.log.1` 和 `chord.log.2`。

当前构建使用 golog 原生纯文本日志格式，如 `[I 2026-05-02 12:00:00 file:123 pwd=/path/to/workspace pid=1234 sid=20260502015258426] message key=value`。其中的 key-value 片段仅视为便于人工阅读的文本，不是稳定的结构化日志 schema。

可通过 `--logs-dir <path>` 或环境变量 `CHORD_LOGS_DIR=<path>` 覆盖。快速复现并收集日志：

```bash
chord --logs-dir ./chord-logs
```

## 相关文档

- [快速开始](./quickstart_CN.md)
- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [Headless 集成](./headless_CN.md)
