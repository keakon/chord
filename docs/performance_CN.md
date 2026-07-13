# 性能

Chord 面向长时间交互会话做了性能优化：大 transcript、模型流式输出、滚屏，以及后台 agent 活动都应保持可控。本页说明 Chord 为保持流畅做了什么、感觉变慢时你能做什么，以及上报问题时该收集哪些信息。

## 优化目标

1. 模型输出文本或思考过程时，TUI 仍保持响应。
2. 长回复期间 CPU 受控，避免按 token 做高成本工作。
3. 加载或浏览大历史会话时内存稳定。
4. 后台任务活跃时，滚屏和键盘输入仍然顺滑。

## 工作方式

- **流式批处理** —— 流式文本以很小的 delta 到达；Chord 会合并 provider delta，让一次 UI 更新处理多个 delta，而不是每个小片段都唤醒 TUI。
- **渲染 cadence** —— 流式内容按节奏刷新到屏幕，而不是每个 token 都重绘。真正的结构变化（新 block、布局边界、rollback）仍会及时刷新。
- **streaming cheap path** —— assistant 和 thinking block 流式输出期间，只有稳定下来的内容走完整 Markdown 渲染；正在变化的尾部走更便宜的纯文本路径。所以长段落在流式期间看起来更朴素——这是预期行为，不是渲染故障。
- **View 缓存** —— 主 viewport、info panel、status bar 等高成本区域按帧缓存，只有输入变化时才重新渲染。
- **滚屏批处理** —— 鼠标滚轮和触摸板 delta 会合并后按短 cadence 应用；大 transcript 只有可见窗口保持热数据，屏幕外区域保持冷却。
- **会话懒加载** —— 恢复大会话时先加载当前窗口供交互，搜索 / 跳转 / 目录的 metadata 和更早的 transcript 区域在后台逐步加载。
- **有界搜索状态** —— transcript 搜索只缓存当前查询在渲染结果中的命中位置，不再长期保留每一行的渲染副本。搜索可检查已 spill 的冷卡片，但不会让其正文或派生索引常驻热窗口。

## 请求与上下文成本

性能不只在 UI 侧。Chord 还会在请求时裁剪陈旧工具输出、保留结构化摘要，并在长会话接近模型限制前做压缩。这些优化能降低延迟、token 用量和 provider 成本，同时不删除持久会话历史。

可配置项见[配置与认证](./configuration_CN.md#上下文剪裁reduction)。

## 感觉变慢时

- 缩小当前会话上下文：运行 `/compact`，或为无关工作另开新会话。
- 换一个终端模拟器对比——不同终端的渲染开销差异明显。
- 超大会话里首次访问很早的 transcript 区域会比热窗口慢，再次访问会命中缓存。

如果某个交互持续偏慢，复现时采集一份 CPU profile，连同诊断包（`Ctrl+G`）一起附在反馈里：

```bash
CHORD_PPROF_PORT=6060 chord
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=15
```

## 贡献者入口

性能敏感改动的 benchmark 套件、回归检查、热点解读和调参取舍见 [CONTRIBUTING](https://github.com/keakon/chord/blob/main/CONTRIBUTING.md#performance-sensitive-changes)；`./scripts/bench_tui_regression.sh` 是权威验证入口。

## 相关

- [配置与认证](./configuration_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
