# 性能优化指南

Chord 面向长时间交互会话做了性能优化：大 transcript、模型流式输出、滚屏，以及后台 agent 活动都应保持可控。本指南总结当前性能优化方案，以及修改性能敏感路径前建议运行的验证命令。

本文是公开用户文档，只说明公开行为和验证方式，不包含内部实现备忘或私有调试记录。

## 目标

Chord 的性能优化围绕四个目标：

1. 模型输出文本或思考过程时，TUI 仍保持响应。
2. 长回复期间 CPU 受控，避免按 token 做高成本工作。
3. 加载或浏览大历史会话时内存稳定。
4. 后台任务活跃时，滚屏和键盘输入仍然顺滑。

## 主要优化方向

### 1. 流式事件批处理

流式文本通常以很小的 delta 到达。如果每个 delta 都单独唤醒一次 TUI，即使命中渲染缓存，也会产生明显开销。Chord 在两层合并流式文本：

- agent runtime 会先合并 provider delta，再发布 stream event；
- TUI 事件订阅层对 `StreamTextEvent` 增加很短的 micro-batch 窗口，让多个按节奏到达的文本 delta 由一次 `Update` 处理。

非流式事件本身不会开启这条 TUI micro-batch 窗口。如果它紧跟在 stream text delta 之后到达，可能会随这批进行中的事件在短窗口结束后一起返回；状态切换、idle 事件、错误和用户可见边界仍保持低延迟。

验证命令：

```bash
go test ./internal/tui -run 'TestWaitForAgentEvent'
go test ./internal/tui -run '^$' -bench 'BenchmarkWaitForAgentEvent.*MicroBatch' -benchmem
```

paced stream benchmark 会报告 `events/batch` 和 `batches/event`。`events/batch` 越高、`batches/event` 越低，表示同样的流式输出需要更少 TUI 唤醒。

### 2. 流式渲染 cadence

流式输出不会在每个 delta 上完整渲染。Chord 会把 stream content 标记为 dirty，延迟高成本 view 刷新，并按 cadence 刷新可见内容。真正的内容边界事件仍可强制刷新，例如新 block、布局边界、rollback 或其他必须及时显示的结构变化。

关键规则是：不要把普通 token 或换行 delta 变成立即 redraw。否则长回复会让 CPU 跟着 token 频率走，而不是跟着帧率 cadence 走。

常用检查：

```bash
go test ./internal/tui -run 'Test.*Stream.*Flush|Test.*Stream.*Boundary|Test.*Streaming'
go test ./internal/tui -run '^$' -bench 'BenchmarkStream.*' -benchmem
```

### 3. streaming cheap path

Assistant 和 thinking block 在流式输出期间会走更便宜的路径：

- 稳定前缀可以缓存复用；
- 正在变化的尾部尽量避免完整 Markdown 渲染；
- 内容 settled 后，或遇到真正边界时，再做最终格式化。

这样可以避免长回复每追加一个小片段就重新渲染整篇 Markdown。

常用检查：

```bash
go test ./internal/tui -run '^$' -bench 'BenchmarkRenderAssistantStreaming|BenchmarkStreamThinking' -benchmem
go test ./internal/tui/markdownutil -run '^$' -bench 'BenchmarkFindStreamingSettledFrontier|BenchmarkStreamingFrontierScanner' -benchmem
```

### 4. View / Draw 缓存

TUI 对高成本区域维护帧级缓存，例如主 viewport、info panel、status bar，以及宿主 redraw workaround 使用的 replay suffix。缓存 key 必须覆盖所有会影响可见输出的输入；漏掉输入会导致 UI 过期，过度失效则会增加 CPU。

常用检查：

```bash
go test ./internal/tui -run '^$' -bench 'BenchmarkModelViewCached|BenchmarkRender.*' -benchmem
```

### 5. Status bar 与动画 cadence

状态指示、spinner、耗时计时和进度展示都应受 cadence 限制。动画 tick 应通过统一的动画调度路径启动，确保旧 tick 可以失效，避免重复 tick 链累积。

如果没有可见内容变化时 CPU 仍然很高，应优先检查动画和 status tick 路径。

常用检查：

```bash
go test ./internal/tui -run 'Test.*Activity|Test.*Animation|Test.*Status'
```

### 6. 滚屏与 viewport 批处理

鼠标滚轮和触摸板手势可能产生大量事件。Chord 会合并 scroll delta，按短 cadence 应用滚动，并避免每个 wheel event 都重放图片或重建 viewport 测量。

大历史会话还依赖 viewport metadata cache 和屏幕外 block 的冷却机制，使热数据集中在可见窗口附近。

常用检查：

```bash
go test ./internal/tui -run 'Test.*Scroll|Test.*Viewport'
go test ./internal/tui -run '^$' -bench 'BenchmarkViewportVisibleWindowBlockIDs|BenchmarkFindMatchesAtWidth' -benchmem
```

### 7. 启动与大 session 内存

恢复大 session 时，Chord 不会急切地把整段 transcript 全部变热。当前窗口会先加载用于交互，metadata 支持搜索、跳转、目录和后续 hydrate。空闲后台任务可以预热附近 metadata，并释放屏幕外缓存。

常用检查：

```bash
go test ./internal/tui -run 'Test.*Deferred|Test.*Startup|Test.*Transcript'
```

### 8. 请求与上下文成本

性能不只在 UI 侧。Chord 还会在请求时裁剪陈旧工具输出、保留结构化摘要，并在长会话接近模型限制前做压缩。这些优化能降低延迟、token 用量和 provider 成本，同时不删除持久会话历史。

可配置项见[配置与认证](./configuration_CN.md#请求级上下文裁剪)。

## 修改性能敏感路径前的推荐验证

小范围 TUI streaming 或 rendering 改动：

```bash
go test ./internal/tui
go test ./internal/tui -run '^$' -bench 'BenchmarkWaitForAgentEvent.*MicroBatch|BenchmarkStream.*|BenchmarkModelViewCached|BenchmarkRender.*' -benchmem
```

更大范围改动建议用 `benchstat` 对比前后结果：

```bash
go test ./internal/tui -run '^$' -bench 'BenchmarkWaitForAgentEvent.*MicroBatch|BenchmarkStream.*|BenchmarkModelViewCached|BenchmarkRender.*|BenchmarkViewportVisibleWindowBlockIDs|BenchmarkFindMatchesAtWidth' -benchmem > /tmp/chord-bench-new.txt
benchstat /tmp/chord-bench-old.txt /tmp/chord-bench-new.txt
```

如果 benchmark 无法解释真实 CPU 占用，建议在问题交互期间采集 CPU profile：

```bash
CHORD_PPROF_PORT=6060 chord
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=15
```

## 常见热点解读

- `bubbletea.(*Program).render`、终端 write 或 renderer cell output：redraw 过频，或 stream event 太碎。
- 活跃 streaming 期间出现 Markdown / glamour / goldmark 热点：streaming cheap path 被绕过。
- viewport 测量、line wrapping 或 ANSI width 热点：visible-window cache 或 line-count cache 可能失效过频。
- 没有内容变化时动画或 status tick handler 仍然很热：可能存在旧 tick 或重复 tick 链。

## 实用调参取舍

- 更大的 stream micro-batch 窗口能降低 CPU，但会让文本显示稍微不那么即时。
- 更长的 content flush cadence 能减少 redraw，但流式输出会更成块。
- 更积极保留缓存能提高重绘速度，但会增加内存。
- 更积极释放屏幕外缓存能降低内存，但首次访问旧 transcript 区域可能变慢。

优先选择能解决实测瓶颈的最小调参，并让 benchmark 尽量贴近正在优化的路径。
