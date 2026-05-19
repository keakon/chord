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

## 上下文和输出限制

按这个顺序理解模型限制字段：

- `limit.context`：总窗口。对大多数模型，只要“输入 + 请求输出”放得进这个数字即可。
- `limit.input`：单独的输入上限。只有 provider 明确公布时才需要写；否则 Chord 会从 `limit.context` 中扣除有效请求输出后推导输入预算。
- `limit.output`：模型的最大输出能力。实际请求还会受 `max_output_tokens` 和 `limit.context` 剩余空间限制。

Chord 会优先使用 `limit.input`，未配置时使用推导出的输入预算，来判断何时在 prompt 过大前压缩，以及 provider 因请求过大而拒绝后如何恢复。若设置了 `context.compaction.reserved`，Chord 会先扣掉这部分预留，再应用 `compaction.threshold`。

以 `gpt-5.5` 为例，公开示例使用 `context=400000`、`input=272000`、`output=128000`。Chord 默认的 `max_output_tokens` 仍是 `32000`，所以实际发送请求时会取更小的输出上限，除非你主动调大。provider 文档里有时会把这类配置叫作 split limits；见 [术语表](../glossary_CN.md)。

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
