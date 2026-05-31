# 常见问题排查

聚焦安装、配置、认证、会话、扩展和性能相关的常见问题。

## 启动失败

先看看这几项：

- Go 版本是否满足要求
- 是否使用了正确入口：`go run ./cmd/chord/`
- `config.yaml` 是否缺失或已损坏
- `auth.yaml` 是否存在明显的 YAML 格式错误

如果 `config.yaml` 缺失，请在交互式终端里先运行一次 `chord` 来启动初始化向导。即使 stdin 被重定向，只要还能打开控制 TTY，向导仍会在那里运行；只有没有控制 TTY 时，Chord 才会立即返回初始化错误。若 `config.yaml` 已存在但 YAML 损坏，请先修好文件；向导只会在缺失配置时触发。

排查命令：

```bash
go test ./cmd/chord/...
```

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

## 429 / quota exhausted

常见原因：key 已达配额上限、provider 限流、并发或高频请求触发了速率限制。

建议：换一个 key、降低并发或减少重试、检查是否存在异常循环调用。

界面说明：

- 右侧 RATE LIMIT 面板展示的是 Codex 最近一次用量/限流快照（如 `5h: 42% 2h30m`）。到达 reset 时间点后倒计时会短暂消失，Chord 触发一次用量刷新；由于服务端可能使用滚动窗口，刷新后百分比不一定立即变成 0%，可能是逐步下降。
- Codex OAuth 运行时状态也会在其它 Chord 进程更新 `auth.state.yaml` 后自动重新加载，因此额度快照、reset 计时、账号元数据和账号状态变化无需重启当前会话也应能生效。
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

对于非官方 OpenAI-compatible 网关，Chord 会把未知 HTTP 400 视为可能来自坏掉的上游 channel 或被包装的 provider 错误。当前 key 会进入最多 1 分钟的短冷却，然后 Chord 可以尝试其它 key、model 或下一轮重试。已知的请求形状错误，例如缺少参数或非法 assistant message 形状，仍会立即停止。

如果连接建立超时，或在超时前没有收到首 token，Chord 会把当前 key 标记为 recovering，使下一次重试优先选择其它健康 key。

### DeepSeek / OpenAI 兼容 thinking 模式 400

如果你使用的是 DeepSeek 这类 `chat-completions` provider，并看到下面这类报错：

- `The reasoning_content in the thinking mode must be passed back to the API.`
- `Invalid assistant message: content or tool_calls must be set`

通常说明这个 provider 要求把上一轮工具调用里的 thinking/reasoning 内容按严格的 assistant message 形状一并带回后续请求。如果同一类报错持续重复，请保留对应的 session dump / trace 供排查。

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
- Ruff quick diagnostics 不更新 LSP 侧边栏，只出现在 `ApplyPatch`/`Write` 工具结果中，并会明确提示完整 Python 语义诊断已跳过。

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

完整推荐配置见 [配置 — Provider/model 诊断](./configuration_CN.md#providermodel-诊断)。

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

- 重新执行同样的 `Read`、`Shell`、`WebFetch` 或本地 shell 操作
- 如果仍能复现，同时保留原始文件/输出和截图

Chord 会在这些界面中按字面显示含 ANSI 的外部文本，不再次执行其中嵌入的终端 escape/control sequence；这也包括裸 `\r` 进度刷新文本。这样既能查看原始序列内容，也不会让诊断 dump 或其他原始终端输出污染周围卡片的渲染。普通工具结果即使包含看起来像 Markdown 的标题、列表、表格或代码块，也会按纯文本处理，避免日志、diff、JSON/YAML 或抓取页面被意外重新排版。

## 切换 tab 或重新获焦后画面错乱

切换 tab、切回终端窗口或重新获得焦点后，TUI 偶发出现旧行残留、横线伪影或工具卡片局部错位：

- 画面已错乱时，轻微调整终端窗口尺寸或切走再切回，通常可强制触发一次完整重绘
- 如果仍能复现，同时保留 diagnostics bundle 和截图

Chord 覆盖两类焦点恢复 redraw 场景：获焦后立即到达的更新，以及终端在后台期间已发生的转录区/布局变化。检测到后台变化后，Chord 会等待焦点稳定，强制触发 host redraw 并附带 fallback 重绘，因此 Ghostty、cmux、iTerm2 等终端在宿主 surface invalidation 持续更久时仍能可靠恢复。diagnostics bundle 也会记录 background-dirty 状态，便于排查残留的 stale-display 现象。

如果现象主要发生在**获焦后的流式输出过程中**，优先把它当作 host redraw/replay 问题排查。不要通过修改组件 padding 或 ANSI 处理来绕过；请同时保留 diagnostics bundle 和截图供调查。

补充：画面错乱时看到类似 `;250m pyright` 的残片，通常不是 LSP 内容，而是被截断的终端控制序列（ANSI/OSC）尾部字符。Chord 通过框架 `WindowTitle` API 输出窗口标题，避免直接写 stdout 与渲染输出交错。

## 恢复长会话后滚轮翻页时出现重复卡片

如果问题主要出现在 `--resume` / `--continue` 后，向上滚动历史内容时，尾部刚完成的工具卡、状态卡或任务卡偶尔又在当前窗口里出现一张“重复卡片”：

- 复现时尽量记录是否是“恢复长会话后 + 鼠标滚轮翻到历史窗口 + 随后工具/任务结果到达”这个组合
- 如果仍能复现，保留 session dump / diagnostics bundle 和截图

这个问题的根因通常不是消息源真的重复，而是 startup deferred transcript 切到历史窗口后，隐藏在尾部窗口里的 live 卡片收到更新时，被当成“当前窗口里不存在的新卡片”重新追加。Chord 会同时在当前 viewport 和 deferred transcript 源数据中定位原卡片，并原位更新，避免滚轮翻页过程中出现重复工具卡、任务卡或状态卡。

## 切回标签页后看似在底部，但向下滚轮还能翻出旧卡片

如果长会话恢复后切到别的标签页，过一段时间再切回，画面看起来已经在最底部，但鼠标滚轮向下仍会翻出更老的卡片：

- 复现时尽量记录是否满足“切出标签页时正在跟随最新内容，切回后未手动上滚就能继续向下翻旧页”
- 如果仍能复现，同时保留 diagnostics bundle、session dump 和截图

这通常表示“当前 deferred 窗口已经滚到底”被误当成“全文已经在尾部”。当标签页失焦期间 startup deferred transcript 仍停留在历史窗口时，重新获焦后画面可能只恢复到“当前窗口底部”，而不是全文尾窗；这会让鼠标滚轮向下继续合法地翻到下一段旧窗口。Chord 会在失焦前记录当前 deferred transcript 是否真正钉在尾窗并跟随最新内容；若是，则在回焦时先恢复真实尾窗，再应用后续滚动，从而避免“看起来已在底部却还能向下翻出旧页”。

如果失焦期间尾部又追加了新消息，回焦后还需要保持当前尾窗宽度，而不是把尾窗重新裁成固定最新块数；否则尾窗起点会前移，随后向下滚轮时就可能看到“之前没出现过的新旧内容”，视觉上像是又多滚了几屏才真正到底。当前修复已覆盖这条 `live append + focus restore + mouse wheel` 路径。

## 长会话中的 `20jj` / `100kk` / `[count]↑↓` 跨窗口跳错

如果恢复的大型会话启用了 deferred transcript，使用 `20jj`、`100kk`、`[count]up`、`[count]down` 等带计数的导航后，位置出现跳错、过早停住、不能稳定回到底部，或者感觉跨窗口后顺序不连续：

- 复现时记录具体按键序列、起始卡片以及预期应该停留的目标卡片
- 如果仍能复现，保留截图和 diagnostics bundle，便于对照可见窗口与全文块顺序

带计数的卡片/行导航按全文逻辑位置逐步消费：每一步都先判断是否需要切换 deferred 窗口，再继续前进或后退，因此超过剩余范围时会稳定停在第一张/最后一张卡片，而不会丢失焦点或翻出重复窗口。

## 长会话里转录区底部内容滚不到

看到最后几行转录内容像被裁掉、最后一个卡片几乎贴着输入分隔线，或已经滚到底但最新对话仍有一部分不可见：

- 留意问题是否出现在长会话中的后台任务结束或状态卡更新之后
- 如果仍能复现，同时保留截图和日志，便于比对转录状态与底部渲染结果

Chord 会处理两类转录高度统计风险：

- 较早的状态卡后续更新时，viewport 高度可能小于真实转录内容。
- 后台缓存丢弃时可能漏算空行偏移，造成滚动逐步漂移。

## ApplyPatch 报 `file ... has not been read in this conversation`

Chord 要求：当前会话里必须先对同一文件执行过一次被跟踪的 `Read`，`ApplyPatch` 才会继续。这样可以减少“盲改”导致的 stale edit 和无效重试。

看到这个错误时：

- 先对目标文件执行一次 `Read`；
- 使用足够唯一的 `@@` 上下文重试一个小 patch hunk；
- 如果之前已有修改、格式化器或其他外部工具可能改过文件，重试前重新读取最小且唯一的 2-4 行块。

## ApplyPatch 报 `changed on disk since the last read`（即使上一次 patch 已成功）

这个错误来自 Chord 进程内的乐观文件锁（FileTracker）：Chord 认为当前磁盘内容已经不再匹配本 agent 上一次 `Read` 时记录的内容哈希。

常见原因：

- 你在 `Read` 与 `ApplyPatch` 之间用编辑器/格式化器等外部进程改动了文件；
- speculative 工具调用被丢弃/回滚，finalize 阶段的工具调用与其发生竞态；
- provider 将 tool arguments 以 JSON 字符串形式包了一层（wrapped arguments）。Chord 会一致地对 tool arguments 做 unwrap；如果路径没有被正确跟踪，请保留日志和 session JSONL。

如果仍能复现，请同时提供 session JSONL 和当前文件 diff，便于检查 tool-call 的顺序与被跟踪的路径。

## ApplyPatch 报 `hunk not found` 或 `hunk is not unique`

`ApplyPatch` 按行匹配 hunk。它可以容忍常见空白和 Unicode 标点差异，但每个 hunk 仍必须在已读取文件中唯一定位。

看到这个错误时：

- 重新 `Read` 目标文件，并基于最新内容重建 patch；
- 如果错误提示 hunk 不唯一，使用错误中的候选行号去 `Read` 目标位置附近，并在 `@@` hunk 中加入附近未变化的唯一上下文行；
- 如果错误提示找不到 hunk，从最新 `Read` 输出中重新复制目标块，并确认 context/removal 行没有带上展示用行号 gutter，且缩进与当前文件一致；
- 把过大的 patch 拆成更小的单文件 patch 或更小的 hunk；
- 不要通过 `Shell` 执行外部 `apply_patch`；请使用 Chord 原生 `ApplyPatch`，这样权限、stale tracking、diff、LSP 和 rollback 才会保持接入。

## 性能问题

感觉滚屏、流式输出或大消息渲染明显变慢：

- 先缩小当前会话上下文规模
- 检查是否存在异常长输出；流式 assistant / thinking 输出会先把稳定结构（空行分段、已闭合代码围栏）走 markdown，长单段文本会先留在更便宜的纯文本路径上，直到结构稳定
- 尝试在不同终端中对比

维护项目本身时，还可进一步使用仓库内的性能检查脚本与 pprof。例如可运行 `go test ./internal/tui -run '^$' -bench 'BenchmarkRenderAssistantStreamingLongTextCardProfile' -cpuprofile cpu.out -memprofile mem.out`，继续区分剩余热点是在 block 渲染还是 viewport 切片。

## 上下文压缩不触发 / 触发过频繁

**现象**：上下文使用率很高但一直没有压缩；或相反，频繁压缩影响使用体验。

排查步骤：

1. 确认 `context.compaction.threshold` 是否已设置且大于 0（0 表示关闭自动压缩）。
2. 检查 TUI 底部栏或信息面板的 `Context` 百分比。它按**可用输入预算**计算，不是按总窗口大小，所以可能比预期的低（详见 [配置 — 上下文压缩](./configuration_CN.md#上下文压缩compaction)）。
3. 如果设置了 `context.compaction.reserved`，由于会先扣除预留再应用 `threshold`，自动压缩会在更低的绝对 token 数触发；若压缩过于频繁，可检查 reserved 是否设得过大。
4. `/compact --no` 会临时关闭当前会话的自动压缩；重新启动会话或执行 `/compact` 可恢复。

**注意**：loop 模式不会禁用自动压缩；它只会对新增消息禁用请求级上下文剪裁。

## 上下文剪裁误裁重要内容

**现象**：模型似乎"忘了"之前的工具输出，但会话文件里内容还在。

排查步骤：

1. 这是上下文剪裁（Reduction）的正常行为：每次 LLM 请求前，过时的工具输出会被从 prompt 中裁剪，但**不会修改**磁盘上的会话文件。
2. 如果你经常需要回头参考较早的读取/搜索结果，可调高 `read_like_age_turns` 和 `read_like_output_bytes`。
3. 如果构建/测试日志很重要，可调高 `shell_success_bytes`。
4. 如果希望更保守的裁剪行为，整体调高各 `*_age_turns` 和 `*_bytes` 参数。

详见 [配置 — 上下文剪裁](./configuration_CN.md#上下文剪裁reduction)。

## 请求被拒绝：`context length` / `input too large`

**现象**：provider 返回类似 "context length exceeded" 或 "input too large" 的错误。

排查步骤：

1. 确认模型 `limit.input` 和 `limit.context` 配置正确。如果 provider 公布了单独的输入上限，必须同时配置 `limit.input`。
2. 检查 `context.compaction.threshold` 是否过高导致自动压缩触发偏晚。
3. 增大 `context.compaction.reserved` 可提前触发压缩，避免请求被拒。
4. 如果频繁出现，可使用 `/compact` 立即手动压缩，或降低 `threshold` 提前触发自动压缩。
5. 在 `log_level: debug` 的日志中搜索 `oversize`，确认是否触发了 oversize recovery（压缩后再重试）。

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
