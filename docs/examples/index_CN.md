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
如果你已经知道自己要用哪个 provider / model，只想找一段最小可复制配置，优先看[模型配置速查](../model-configs_CN.md)。

## Agent 文件格式

Agent 定义可以写成 `.md`、`.yaml` 或 `.yml`。示例里使用 `.md`，是为了用 front matter 放结构化设置，再把角色 prompt 写成 Markdown 正文；更喜欢纯 YAML 的话，也可以直接写 YAML 文件。两种格式都放在同一套全局或项目级 `agents/` 目录下。

## 上下文和输出限制

示例配置会为每个模型设置 `limit.context` 和 `limit.output`；只有 provider 单独公布输入上限时，示例才会写 `limit.input`。各字段的含义，以及 provider 未单独公布输入上限时 Chord 如何推导输入预算，见[术语表](../glossary_CN.md)。这些限制如何与压缩配合，见[上下文管理 — 上下文压缩](../context-management_CN.md#上下文压缩compaction)。

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
