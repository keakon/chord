# 配置示例

本目录提供端到端、可直接复制粘贴的 `config.yaml` 示例。

| 文件                                                           | 适用场景                                                                                       |
| -------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| [`anthropic-minimal.yaml`](./anthropic-minimal.yaml)           | 最小可用配置——一个 provider、一个 key、一个模型池                                              |
| [`codex-oauth-with-lsp.yaml`](./codex-oauth-with-lsp.yaml)     | Codex OAuth + Go/Python LSP + "reviewer" SubAgent                                            |
| [`openai-compat-load-balance.yaml`](./openai-compat-load-balance.yaml) | OpenAI 兼容网关多 key 轮询 + 备用 endpoint 故障转移                                  |
| [`team-ready.yaml`](./team-ready.yaml)                         | 严格的按 agent 权限、用 hooks 做审计/lint、配 `.chord/` 项目级覆盖                              |

这些是起点，不是真理。多数字段都可选——具体含义见 [配置字段速查表](../configuration_CN.md#配置字段速查表)。

## 各类文件放哪里

| 文件类型                       | 路径                                                                  |
| ------------------------------ | --------------------------------------------------------------------- |
| 全局配置                       | `~/.config/chord/config.yaml`                                         |
| 凭据                           | `~/.config/chord/auth.yaml`                                           |
| 项目级覆盖                     | `<repo>/.chord/config.yaml`                                           |
| 全局自定义 agent               | `~/.config/chord/agents/<name>.md`                                    |
| 项目级自定义 agent             | `<repo>/.chord/agents/<name>.md`                                      |
| 项目级 slash 命令              | `<repo>/.chord/commands/<name>.md`                                    |

完整目录布局及 project-key 的解析方式见 [目录与路径](../paths_CN.md)。
