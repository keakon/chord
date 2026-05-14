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

### DeepSeek / OpenAI 兼容 thinking 模式 400

如果你使用的是 DeepSeek 这类 `chat-completions` provider，并看到下面这类报错：

- `The reasoning_content in the thinking mode must be passed back to the API.`
- `Invalid assistant message: content or tool_calls must be set`

通常说明这个 provider 要求把上一轮工具调用里的 thinking/reasoning 内容按严格的 assistant message 形状一并带回后续请求。如果同一类报错持续重复，请保留对应的 session dump / trace 供排查。

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

## 查看日志 / dump / shell 输出时，TUI 卡片出现异色、背景泄漏或换行错乱

查看诊断 dump、原始命令输出或其他外部文本时，工具卡片、本地 shell 结果、问题对话框或确认摘要出现异常颜色、背景泄漏或换行错乱：

- 升级到包含外部文本渲染修复的版本
- 重新执行同样的 `Read`、`Shell`、`WebFetch` 或本地 shell 操作
- 最新版仍能复现时，同时保留原始文件/输出和截图

最近构建会在这些界面中按字面显示含 ANSI 的外部文本，不再次执行其中嵌入的终端 escape/control sequence；这也包括裸 `\r` 进度刷新文本。这样既能查看原始序列内容，也不会再让诊断 dump 或其他原始终端输出污染周围卡片的渲染。普通工具结果即使包含看起来像 Markdown 的标题、列表、表格或代码块，也会按纯文本处理，避免日志、diff、JSON/YAML 或抓取页面被意外重新排版。

## 切换 tab 或重新获焦后画面错乱

切换 tab、切回终端窗口或重新获得焦点后，TUI 偶发出现旧行残留、横线伪影或工具卡片局部错位：

- 升级到包含最新焦点恢复 redraw 修复的版本
- 画面已错乱时，轻微调整终端窗口尺寸或切走再切回，通常可强制触发一次完整重绘
- 最新版仍能复现时，同时保留 diagnostics bundle 和截图

最近构建覆盖了两类焦点恢复 redraw 场景：获焦后立即到达的更新，以及终端在后台期间已发生的转录区/布局变化。检测到后台变化后，Chord 会等待焦点稳定，强制触发 host redraw 并附带 fallback 重绘，因此 Ghostty、cmux、iTerm2 等终端在宿主 surface invalidation 持续更久时仍能可靠恢复。diagnostics bundle 也会记录 background-dirty 状态，便于排查残留的 stale-display 现象。

如果现象主要发生在**获焦后的流式输出过程中**，请确认使用的是最新版本。旧版本可能在多次 `View()` 调用后出现 stale cells，因为 cached/deferred frame 在 host-side `ClearScreen` 后字节完全相同。新版本会让 redraw replay marker 在整个 generation 周期内持续有效且不存入 cached view，使渲染器始终有机会恢复。

如果 diagnostics 显示内部渲染正常但真实终端仍错乱，优先把它当作 host redraw/replay 问题排查。不要通过修改组件 padding 或 ANSI 处理来绕过；请同时保留 diagnostics bundle 和截图供调查。

补充：画面错乱时看到类似 `;250m pyright` 的残片，通常不是 LSP 内容，而是被截断的终端控制序列（ANSI/OSC）尾部字符。新版本已将窗口标题更新改为通过框架 `WindowTitle` API 输出，避免直接写 stdout 与渲染输出交错。

## 恢复长会话后滚轮翻页时出现重复卡片

如果问题主要出现在 `--resume` / `--continue` 后，向上滚动历史内容时，尾部刚完成的工具卡、状态卡或任务卡偶尔又在当前窗口里出现一张“重复卡片”：

- 升级到包含 deferred transcript 去重修复的版本
- 复现时尽量记录是否是“恢复长会话后 + 鼠标滚轮翻到历史窗口 + 随后工具/任务结果到达”这个组合
- 最新版仍能复现时，保留 session dump / diagnostics bundle 和截图

这个问题的根因通常不是消息源真的重复，而是 startup deferred transcript 已切到历史窗口后，隐藏在尾部窗口里的 live 卡片收到更新时，被旧版本误当成“当前窗口里不存在的新卡片”重新追加了一次。新版本会同时在当前 viewport 和 deferred transcript 源数据中定位原卡片，并原位更新，避免滚轮翻页过程中出现重复工具卡、任务卡或状态卡。

## 切回标签页后看似在底部，但向下滚轮还能翻出旧卡片

如果长会话恢复后切到别的标签页，过一段时间再切回，画面看起来已经在最底部，但鼠标滚轮向下仍会翻出更老的卡片：

- 升级到包含 deferred transcript 尾窗恢复修复的版本
- 复现时尽量记录是否满足“切出标签页时正在跟随最新内容，切回后未手动上滚就能继续向下翻旧页”
- 最新版仍能复现时，同时保留 diagnostics bundle、session dump 和截图

旧版本把“当前 deferred 窗口已经滚到底”误当成“全文已经在尾部”。当标签页失焦期间 startup deferred transcript 仍停留在历史窗口时，重新获焦后画面可能只恢复到“当前窗口底部”，而不是全文尾窗；这会让鼠标滚轮向下继续合法地翻到下一段旧窗口。新版本会在失焦前记录当前 deferred transcript 是否真正钉在尾窗并跟随最新内容；若是，则在回焦时先恢复真实尾窗，再应用后续滚动，从而避免“看起来已在底部却还能向下翻出旧页”。

## 长会话中的 `20jj` / `100kk` / `[count]↑↓` 跨窗口跳错

如果恢复的大型会话启用了 deferred transcript，使用 `20jj`、`100kk`、`[count]up`、`[count]down` 等带计数的导航后，位置出现跳错、过早停住、不能稳定回到底部，或者感觉跨窗口后顺序不连续：

- 升级到包含 deferred transcript counted navigation 修复的版本
- 复现时记录具体按键序列、起始卡片以及预期应该停留的目标卡片
- 最新版仍能复现时，保留截图和 diagnostics bundle，便于对照可见窗口与全文块顺序

旧版本只在操作开始前检查一次 deferred 窗口边界，后续剩余步数仍按当前窗口局部状态推进；一旦计数动作跨过多个 deferred 窗口，就可能出现跳页、少走、顺序错位或无法稳定饱和到边界。新版本会把带计数的卡片/行导航按全文逻辑位置逐步消费：每一步都先判断是否需要切换 deferred 窗口，再继续前进或后退，因此超过剩余范围时会稳定停在第一张/最后一张卡片，而不会丢失焦点或翻出重复窗口。

## 长会话里转录区底部内容滚不到

看到最后几行转录内容像被裁掉、最后一个卡片几乎贴着输入分隔线，或已经滚到底但最新对话仍有一部分不可见：

- 升级到包含最新 TUI 转录区裁剪修复的版本
- 留意问题是否出现在长会话中的后台任务结束或状态卡更新之后
- 最新版仍能复现时，同时保留截图和日志，便于比对转录状态与底部渲染结果

最近修复解决了两类转录高度统计错误：

- 较早的状态卡后续更新时，旧版本可能让 viewport 高度小于真实转录内容。
- 后台缓存丢弃时可能漏算空行偏移，造成滚动逐步漂移。

## Edit 报 `file ... has not been read in this conversation`

较新的构建会要求：当前会话里必须先对同一文件执行过一次被跟踪的 `Read`，`Edit` 才会继续。这样可以减少“盲改”导致的 stale edit 和无效重试。

看到这个错误时：

- 先对目标文件执行一次 `Read`；
- 复制 `old_string` 时只取原始源码部分，不要带上显示出来的行号 gutter 和分隔 tab；
- 如果之前已有 `Edit`、格式化器或其他外部工具可能改过文件，重试前重新读取最小且唯一的 2-4 行块。

## Edit 报 `changed on disk since the last read`（即使上一次 Edit 已成功）

这个错误来自 Chord 进程内的乐观文件锁（FileTracker）：Chord 认为当前磁盘内容已经不再匹配本 agent 上一次 `Read` 时记录的内容哈希。

常见原因：

- 你在 `Read` 与 `Edit` 之间用编辑器/格式化器等外部进程改动了文件；
- speculative 工具调用被丢弃/回滚，finalize 阶段的工具调用与其发生竞态；
- provider 将 tool arguments 以 JSON 字符串形式包了一层（wrapped arguments）。新版会一致地对 tool arguments 做 unwrap；如果你在旧版上，`path` 可能没有被正确跟踪，从而触发“假 stale”。

如果在最新版仍能复现，请同时提供 session JSONL 和当前文件 diff，便于检查 tool-call 的顺序与被跟踪的路径。

## 流式工具卡片已改文件后，`Edit` 又报 `old_string not found`

在排查启用了 streaming tool execution 的开发构建时，可能会看到 `Edit` 报错，但目标文件里已经包含预期的新内容。这通常表示某个 speculative `Write` / `Edit` / `Delete` 在 LLM finalize 前提前执行，随后因为 args drift、过滤或回滚被丢弃，而 finalized 路径在 speculative 文件变更完成回滚前又尝试正式重跑。

较新的构建会在允许 finalized 执行路径重跑前，同步回滚已完成的 speculative 文件变更。如果仍然看到这类现象：

- 在日志中查找 `args_drift`、`filtered`、`rollback`、`length_recovery` 等 speculative discard 原因
- 确认 finalized `Edit` 没有复用来自更早文件快照的 stale `old_string`
- 保留 session JSONL 和当前文件 diff，便于检查 speculative discard / rollback 的先后顺序

## 性能问题

感觉滚屏、流式输出或大消息渲染明显变慢：

- 先缩小当前会话上下文规模
- 检查是否存在异常长输出；流式 assistant / thinking 输出会先把稳定结构（空行分段、已闭合代码围栏）走 markdown，长单段文本会先留在更便宜的纯文本路径上，直到结构稳定
- 尝试在不同终端中对比

维护项目本身时，还可进一步使用仓库内的性能检查脚本与 pprof。例如可运行 `go test ./internal/tui -run '^$' -bench 'BenchmarkRenderAssistantStreamingLongTextCardProfile' -cpuprofile cpu.out -memprofile mem.out`，继续区分剩余热点是在 block 渲染还是 viewport 切片。

## 何时检查日志

遇到以下问题时，优先查看日志：

- provider 请求失败但终端只显示摘要错误
- MCP / LSP 初始化异常
- hook 执行结果与预期不符
- headless 集成事件不完整

默认日志目录：`${XDG_STATE_HOME:-~/.local/state}/chord/logs/`。当前日志文件为 `chord.log`，轮转文件为 `chord.log.1` 和 `chord.log.2`。

当前构建使用 golog 原生纯文本日志格式，如 `[I 2026-05-02 12:00:00 file:123 pwd=/path/to/workspace pid=1234 sid=20260502015258426] message key=value`。其中的 key-value 片段仅视为便于人工阅读的文本，不是稳定的结构化日志 schema；运行时 logger 不再输出旧的 `level=... msg=...` 伪结构化行。

可通过 `--logs-dir <path>` 或环境变量 `CHORD_LOGS_DIR=<path>` 覆盖。快速复现并收集日志：

```bash
chord --logs-dir ./chord-logs
```

## 相关文档

- [快速开始](./quickstart_CN.md)
- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [Headless 集成](./headless_CN.md)
