# Configuration examples

This directory contains end-to-end, copy-paste-ready `config.yaml` examples.

| File                                                   | Use case                                                                                       |
| ------------------------------------------------------ | ---------------------------------------------------------------------------------------------- |
| [`anthropic-minimal.yaml`](./anthropic-minimal.yaml)   | Smallest working config — one provider, one key, one model pool                                |
| [`codex-oauth-with-lsp.yaml`](./codex-oauth-with-lsp.yaml) | Codex OAuth + Go and Python LSP + a "reviewer" SubAgent                                    |
| [`openai-compat-load-balance.yaml`](./openai-compat-load-balance.yaml) | Multi-key rotation against an OpenAI-compatible gateway, with backup-endpoint failover |
| [`team-ready.yaml`](./team-ready.yaml)                 | Strict per-agent permissions, hooks for auditing/lint, project-level `.chord/` overrides       |

These are starting points, not opinionated truth. Most fields are optional — see the [Configuration cheatsheet](../configuration.md#configuration-cheatsheet) for what each one does.

## Where things go

| File                            | Path                                                                  |
| ------------------------------- | --------------------------------------------------------------------- |
| Global config                   | `~/.config/chord/config.yaml`                                         |
| Credentials                     | `~/.config/chord/auth.yaml`                                           |
| Project-level overrides         | `<repo>/.chord/config.yaml`                                           |
| Global custom agents            | `~/.config/chord/agents/<name>.md`                                    |
| Project-level custom agents     | `<repo>/.chord/agents/<name>.md`                                      |
| Project-level slash commands    | `<repo>/.chord/commands/<name>.md`                                    |

For the full layout and how project-key resolution works, see [Paths](../paths.md).
