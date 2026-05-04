# 常见问题排查

本页面向使用者，聚焦安装、配置、认证、会话、扩展和性能相关的常见问题。

## 启动失败

先检查：

- Go 版本是否满足要求
- 是否使用了正确入口：`go run ./cmd/chord/`
- `config.yaml` / `auth.yaml` 是否存在明显 YAML 格式错误

可执行：

```bash
go test ./cmd/chord/...
```

如果是构建好的二进制，建议重新运行并查看终端错误输出。

## 401 / 403 / 认证失败

检查：

- `auth.yaml` 中 provider 名称是否与 `config.yaml` 对应
- API key 是否有效
- OAuth provider 是否配置了 `preset: codex`

可执行：

```bash
chord test-providers
```

## 429 / quota exhausted

常见原因：

- key 已达配额上限
- provider 限流
- 并发或高频请求触发了速率限制

建议：

- 换一个 key
- 降低并发或减少重试
- 检查是否存在异常循环调用

## TUI 启动了，但无法正常请求

检查：

- 当前 provider / model 是否存在
- 网络是否能访问对应 API
- 代理配置是否生效

例如：

```bash
curl -I https://api.anthropic.com
curl -I https://api.openai.com/v1
```

## MCP 一直未就绪

先确认：

- MCP 地址是否可访问
- 配置名称是否正确
- 本地模式下是否只是还在异步初始化

注意：启动后的短暂灰色 pending 状态不一定是错误。

## 写文件后没有诊断

如果你已经配置了 LSP，但写文件后没有看到诊断：

- 检查本机是否安装了对应语言服务器
- 检查 `lsp` 配置格式是否正确
- 确认目标文件类型是否与 `file_types` 匹配

## 会话恢复异常

如果 `--continue` 或 `--resume` 看起来没有按预期工作：

- 确认当前目录是否与原会话属于同一项目
- 尝试显式使用 `--resume <session-id>`
- 检查是否只是恢复过程较慢而非真的丢失

## 会话恢复 / restore 行为说明

最近的一轮内部清理删除了一个已经无效的旧 LLM responses-session reset 路径，并把会话边界处理统一收口到当前 provider / session identifier 上。对普通使用者来说这应当是无感变更；但如果你在排查会话恢复、plan execution 或 key/model 切换行为，建议：

- 先确认使用的是最新构建，而不是拿 1.0 前旧版本行为做对照
- 在 `--continue`、`--resume`、新建会话、fork 会话或 plan execution 之后，以当前行为为准，不要再假设存在一个额外的手动 responses-session reset 步骤
- 如果你怀疑 Codex/OpenAI 的会话边界有回归，请使用最新构建采集日志，确保日志反映的是清理后的传输层生命周期

这次改动并没有删除会话恢复能力；删除的是一条已经不再影响 HTTP 请求行为的旧内部 reset 管线，同时保留并收敛了当前仍有效的 WebSocket / session 生命周期处理。

## 在查看日志 / dump / shell 输出时，TUI 卡片出现异色、背景泄漏或换行错乱

如果你在查看诊断 dump、原始命令输出或其他外部文本时，工具卡片、本地 shell 结果、问题对话框或确认摘要出现异常颜色、背景泄漏或换行错乱：

- 升级到包含外部文本渲染修复的版本
- 重新执行同样的 `Read`、`Bash`、`WebFetch` 或本地 shell 操作
- 如果最新版本里仍能复现，请同时保留原始文件/输出和截图

最近的构建会在这些界面中按字面显示 ANSI-rich 外部文本，而不会再次执行其中嵌入的终端 escape/control sequence；这也包括裸 carriage return / `\r` 进度刷新文本。这样既能查看原始序列内容，也不会再让诊断 dump 或其他原始终端输出污染周围卡片的渲染。普通工具结果即使包含看起来像 Markdown 的标题、列表、表格或代码块，也会按纯文本处理，避免日志、diff、JSON/YAML 或抓取页面被意外重新排版。

## 切换 tab 或重新获焦后出现画面错乱

如果在切换 tab、切回终端窗口或重新获得焦点后，TUI 偶发出现旧行残留、横线伪影，或工具卡片局部错位：

- 升级到包含最新焦点恢复 redraw 修复的版本
- 如果画面已经错乱，轻微调整终端窗口尺寸，或切走再切回，通常可以强制触发一次完整重绘
- 如果最新版本里仍能复现，请同时保留 diagnostics bundle 和截图

最近的构建覆盖了两类焦点恢复 redraw 场景：一类是重新获焦后立即到达的更新，另一类是终端处于后台期间已经发生的转录区/布局变化。检测到后台变化后，Chord 会等待 focus-settle，先触发一次强 host redraw，并为同一轮 focus 周期显式挂上一轮更晚的 fallback redraw。即使较早的 `post-focus-settle-redraw` 已经执行，这轮更晚的 `post-focus-settle-fallback` 也不会被它自己取消，因此 Ghostty/cmux 在宿主 surface invalidation 持续更久时仍会再得到一次恢复机会。diagnostics bundle 也会记录 background-dirty 状态和 fallback 已 arm 的事件，便于把残留的 stale-display 现象与内部最终 screen buffer 对照。

## 长会话里转录区底部内容滚不到

如果你看到最后几行转录内容像被裁掉、最后一个卡片几乎贴着输入分隔线，或者已经滚到底但最新对话仍有一部分不可见：

- 升级到包含最新 TUI 转录区裁剪修复的版本
- 留意问题是否出现在长会话中的后台任务结束或状态卡更新之后
- 如果在最新版本里仍能复现，请同时保留截图和日志，便于比对转录状态与底部渲染结果

最近的修复解决了一类转录高度统计错误：长会话里较早的状态卡在后续更新时，旧版本可能让 viewport 记录的总高度小于真实转录内容，导致最后几行甚至最后几张卡片无法滚动到。

## 性能问题

如果你感觉滚屏、流式输出或大消息渲染明显变慢：

- 先缩小当前会话上下文规模
- 检查是否存在异常长输出
- 尝试在不同终端中对比

如果你在维护项目本身，还可以进一步使用仓库内的性能检查脚本与 pprof。

## 何时查看日志

遇到以下问题时，优先查看日志：

- provider 请求失败但终端只显示摘要错误
- MCP / LSP 初始化异常
- hook 执行结果与预期不符
- headless 集成事件不完整

默认日志目录：`${XDG_STATE_HOME:-~/.local/state}/chord/logs/`。当前日志文件是 `chord.log`；轮转文件是 `chord.log.1` 和 `chord.log.2`。

当前构建使用 golog 原生纯文本日志格式，例如 `[I 2026-05-02 12:00:00 file:123 pwd=/path/to/workspace pid=1234 sid=20260502015258426] message key=value`。其中的 key-value 片段只应视为便于人工阅读的文本，不是稳定的结构化日志 schema；运行时 logger 不再输出旧的 `level=... msg=...` 伪结构化行。

可以通过 `--logs-dir <path>` 或环境变量 `CHORD_LOGS_DIR=<path>` 覆盖。快速复现并收集日志：

```bash
chord --logs-dir ./chord-logs
```

## 相关文档

- [快速开始](./quickstart_CN.md)
- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [Headless 集成](./headless_CN.md)
