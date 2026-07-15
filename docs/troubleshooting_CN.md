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

`auth.yaml` 中包含数百或数千个 OpenAI / ChatGPT OAuth 账号时，Chord 会在后台加载凭据元数据。仅缺少元数据不应阻止启动。

- 个人 Plus/Pro 账号可能只有 `user_id`，没有 `chatgpt_account_id`。这类账号仍可用于普通请求，但不会发送 `ChatGPT-Account-ID`，也不会参与依赖 account id 的 Codex usage / rate-limit polling。
- 若日志显示 `account_user_id mismatch` 或 `account_id mismatch`，说明配置里显式写出的身份和 token 自身能解析出的身份冲突；这类应修正或移除对应 credential。

手动转换 Codex/sub2api 导出的账号时，请保留能获取到的 `email`、`account_id` 和 `account_user_id`。需要逐项诊断时，运行 `chord doctor models`。

## 429 / quota exhausted

常见原因：key 已达配额上限、provider 限流、并发或高频请求触发了速率限制。

建议：换一个 key、降低并发或减少重试、检查是否存在异常循环调用。

如果要判断哪些 key 或模型反复限流 / 报错：

- 在 TUI 的 Normal 模式下按 `Ctrl+E` 打开错误面板；这里会记录包含 429 在内的重试错误，并显示 provider、model 和打码后的 `key=...` 标识。
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

遵循官方 API 错误语义的端点请设置 `official_api: true`，此时 Chord 会把 HTTP 400 视为终止性请求错误。聚合或代理网关可能把上游故障包装成 HTTP 400；这类端点可设置 `official_api: false` 或省略该字段，让未知 400 进入正常的重试和备用模型流程。`preset: codex` 会自动按官方 API 处理。

如果请求长时间停在 `connecting` 后重试，请直接测试端点、检查代理设置并查看错误面板。Chord 会限制连接等待时间，避免单个不可用密钥或网关无限阻塞。

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

这类 WebSocket 会话状态不一致通常会由 Chord 使用本地完整对话自动重试恢复。如果错误反复出现，请导出诊断包，并在反馈中附上会话 ID。

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

Chord 会在恢复前自动修复进程中断造成的不完整轮次。如果恢复后的模型 / 服务商状态或对话顺序异常，请使用当前版本导出诊断包，并附上会话 ID。

## 委派的 SubAgent 看似卡住，或 `escalate` 卡片一直执行

当前版本会自动恢复旧会话中可能暴露的两类故障：

如果恢复会话后聚焦 SubAgent，右侧 MODEL 或 Pool 为空，当前版本会先读取任务、recovery snapshot 和 usage ledger 中保存的模型；旧记录没有模型时，会按最新 Agent 配置解析。因此不需要手动编辑 `subagents/tasks.json` 或 meta 文件。

恢复时，durable `task_id` 是委派工作的稳定身份；`explorer-4`、`explorer-6` 之类的 `agent_id` 只是该任务历次 runtime instance。历史 instance 的 transcript 会按 task 合并，但 sidebar 和焦点路由只暴露该 task 的 canonical 最新实例。不要把旧 `agent_id` 当作另一项独立任务，也不要通过手工复制/删除 instance 文件改变恢复结果。

如果切换到大型 SubAgent transcript，或在 parked SubAgent 视图按 Enter 继续后整个 TUI 不再响应，当前版本会在焦点切换时使用有界 transcript 窗口，并将上下文读取、rehydrate 与继续请求移出 TUI 更新热路径。此前还有一条独立故障链：rehydrate 后 info panel 会触发 `MainAgent.InvokedSkills → SubAgent.InvokedSkills → MainAgent.InvokedSkills` 递归并导致 stack overflow；该路由循环现已移除。工作区共享 skill catalog，但 MainAgent 与每个 SubAgent 会按各自最新权限过滤可见项，并分别记录 invoked 状态。

进程 stderr 现在直接写入 rotating log 文件，不再由 Go goroutine 从 pipe 读取后回灌日志系统。因此即使出现 runtime fatal，完整堆栈也会写入 `chord.log` 并让进程退出，不会再次表现为所有按键（包括 raw-mode Ctrl+C）都失效的永久卡死。若旧版本仍无响应，先从另一个终端终止对应进程并用 `reset` 恢复原终端，然后导出 diagnostics。

- Parked SubAgent 被 rehydrate 后，排队输入会显式唤醒其事件循环。如果 worker 保持 `running` 却没有创建 turn，启动 watchdog 会自动重试一次唤醒。
- 如果 worker 仍无法启动，或者 provider / 模型重试最终失败，Chord 会把任务标记为 failed、记录 `risk_alert`，并唤醒 owner/MainAgent，由其重试、重新委派或报告 blocker；系统不会伪造成功的 `complete` 结果。

`escalate` 是本地协调事件，不是长时间运行的网络操作。旧版本可能把它的 tool result 落盘在 assistant tool call 之前，导致恢复后一个已经完成的卡片看起来仍是 pending。当前版本会在相邻消息具有相同 `tool_call_id` 时做严格的局部修复；无法匹配的 orphan result 仍会被丢弃。

如果旧版本创建的会话已经卡住：

1. 重新构建或安装当前 Chord，并通过 `--resume <session-id>` 重启；
2. 重试或定向通知委派任务时使用稳定的 `task_id`，不要使用旧 runtime 的 `agent_id`；
3. 不要手工编辑 `agents/*.jsonl`、`subagents/tasks.json` 或 mailbox 文件；
4. 开启 `log_level: debug` 后，在 `chord.log` 中搜索 `startup watchdog retrying wake`、`SubAgent failed` 或 `removed orphan tool messages`。

正常的响应后 watchdog 可能提醒仍存活的 worker 调用 `complete` 或 `escalate`。无法恢复的 worker 则会以 failed 终态关闭并路由回 owner；失败不会被当作成功完成。

## 查看日志 / dump / shell 输出时，TUI 卡片出现异色、背景泄漏或换行错乱

查看诊断 dump、原始命令输出或其他外部文本时，工具卡片、本地 shell 结果、问题对话框或确认摘要出现异常颜色、背景泄漏或换行错乱：

- 重新执行同样的 `read`、`shell`、`web_fetch` 或本地 shell 操作
- 如果仍能复现，同时保留原始文件/输出和截图

Chord 会用终端安全的纯文本展示外部工具输出。如果同一段内容持续导致布局错乱，请同时附上原始文本和截图，以便复现渲染问题。

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

排查 dump 时注意：

- 如果崩溃前最后一个 shell/tool 结果已经写入 `main.jsonl`，通常说明该工具输出没有丢。
- 如果崩溃时正在等待 LLM SSE 流，`dumps/llm/*.json` 里可能只有 `request_body`、部分 `sse_chunks` 和 `reading SSE stream: context canceled`，表示该次 LLM 响应只 dump 到中途，没有完整 final text。
- `context canceled` 多数是进程关闭后的结果，不一定是根因。

反馈问题时，请附上 panic 栈、诊断包、终端名称与版本，以及当时正在渲染的内容。

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

## 长会话里转录区底部内容滚不到

看到最后几行转录内容像被裁掉、最后一个卡片几乎贴着输入分隔线，或已经滚到底但最新对话仍有一部分不可见：

- 留意问题是否出现在长会话中的后台任务结束或状态卡更新之后
- 如果仍能复现，同时保留截图和日志，便于比对转录状态与底部渲染结果

## 文件编辑工具提示文件在观察后发生变化

这个警告表示文件在 Agent 上次读取后发生了变化。Chord 会基于当前内容验证编辑；`write` 和 `delete` 继续执行前还可能创建备份。

常见原因：

- 你在 `read` 与 `edit` 之间用编辑器/格式化器等外部进程改动了文件；
- 另一个 Agent 或 Chord 进程改动了文件；
- 格式化器、代码生成器或构建步骤改动了文件。

重试前请重新 `read`。如果 Chord 创建了备份，工具结果会显示其在当前会话目录下的路径。`edit` 和 `patch` 的匹配行为详见[编辑工具](./edit-tools_CN.md)。

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
- 尝试在不同终端中对比

渲染与流式输出的优化原理、以及如何采集 CPU profile 用于反馈，见[性能](./performance_CN.md)。

## 上下文压缩不触发 / 触发过频繁

**现象**：上下文使用率很高但一直没有压缩；或相反，频繁压缩影响使用体验。

排查步骤：

1. 确认 `context.compaction.threshold` 是否已设置且大于 0（0 表示关闭自动压缩）。
2. 检查 TUI 底部栏或信息面板的 `Context` 百分比。它按**可用输入预算**计算，不是按总窗口大小，所以可能比预期的低（详见 [配置 — 上下文压缩](./configuration_CN.md#上下文压缩compaction)）。
3. 如果设置了 `context.compaction.reserved`，由于会先扣除预留再应用 `threshold`，自动压缩会在更低的绝对 token 数触发；若压缩过于频繁，可检查 reserved 是否设得过大。
4. `/compact --no` 会临时关闭当前会话的自动压缩；重新启动会话或执行 `/compact` 可恢复。
5. 如果网关返回缺失或为 0 的用量数据，请开启 `log_level: debug`，并在自动压缩日志中查看 `estimated_input_tokens` 和 `effective_input_tokens`。

**注意**：loop 模式不会禁用自动压缩；它只会对新增消息禁用请求级上下文剪裁。

## 上下文剪裁误裁重要内容

**现象**：模型似乎"忘了"之前的工具输出，但会话文件里内容还在。

排查步骤：

1. 这是上下文剪裁（Reduction）的正常行为：每次 LLM 请求前，过时的工具输出会被从 prompt 中裁剪，但**不会修改**磁盘上的会话文件。
2. 如果你经常需要回头参考较早的读取/搜索结果，可调高 `read_like_age_turns` 和 `read_like_output_bytes`。
3. 如果构建 / 测试日志仍然是重要上下文，可调高 `shell_success_bytes`。
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

可通过 `--logs-dir <path>` 或环境变量 `CHORD_LOGS_DIR=<path>` 覆盖。快速复现并收集日志：

```bash
chord --logs-dir ./chord-logs
```

## 相关文档

- [快速开始](./quickstart_CN.md)
- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [Headless 集成](./headless_CN.md)
