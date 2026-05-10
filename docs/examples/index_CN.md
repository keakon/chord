# 配置示例

这里不再把多个配置文件混在一个 YAML 里讲。按下面的场景，直接照着创建对应路径的文件即可。

## 先看选型

| 场景 | 适用情况 | 页面 |
| --- | --- | --- |
| 最小可用 | 先跑起来，一个 provider、一个 key、一个模型池 | [最小可用](./examples-minimal_CN.md) |
| Codex + LSP | 用 Codex OAuth，想配 Go / Python LSP，再加一个 reviewer | [Codex + LSP](./examples-codex-workstation_CN.md) |
| OpenAI 兼容网关 | 多 key 轮询、主备 endpoint 故障转移 | [OpenAI 兼容网关](./examples-openai-compat_CN.md) |
| 团队仓库 | 项目级 `.chord/`、hooks、共享命令、多 agent 分工 | [团队方案](./examples-team_CN.md) |

这些示例是起点，不是模板生成器。字段含义和完整规则见[配置字段速查表](../configuration_CN.md#配置字段速查表)。

## split-limit 模型怎么配

如果 provider 公布了 split limit，建议同时配置：

- `limit.context`：总窗口
- `limit.input`：输入侧预算
- `limit.output`：输出上限

Chord 会用 `limit.input` 计算自动压缩阈值、oversize 恢复和其他输入预算相关逻辑；未配置时才回退到 `limit.context` 以兼容旧配置。若设置了 `context.compaction.reserved`，Chord 会先扣掉这部分预留，再应用 `compact_threshold`。

以 `gpt-5.5` 为例，当前公开示例统一使用保守基线：`context=400000`、`input=272000`、`output=32000`。这样自动压缩和 oversize 恢复会与常见的输入上限观测保持一致；如果你已经核实某个 provider / runtime 的专用限制，再在自己的配置里显式覆盖。

## 各类文件放哪里

| 文件类型 | 路径 |
| --- | --- |
| 全局配置 | `~/.config/chord/config.yaml` |
| 凭据 | `~/.config/chord/auth.yaml` |
| 项目级覆盖 | `<repo>/.chord/config.yaml` |
| 全局自定义 agent | `~/.config/chord/agents/<name>.md` |
| 项目级自定义 agent | `<repo>/.chord/agents/<name>.md` |
| 项目级 slash 命令 | `<repo>/.chord/commands/<name>.md` |

完整目录布局及 project-key 的解析方式见[目录与路径](../paths_CN.md)。
