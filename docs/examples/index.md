# Configuration examples

These examples are organized by **real file layout**, not by stuffing multiple files into comments inside one YAML blob. Pick a scenario and create the files at the paths shown on that page.

## Choose a starting point

| Scenario | Best for | Page |
| --- | --- | --- |
| Minimal | Smallest working setup: one provider, one key, one model pool | [Minimal](./examples-minimal.md) |
| Codex + LSP | Codex OAuth, Go / Python LSP, plus a reviewer agent | [Codex + LSP](./examples-codex-workstation.md) |
| OpenAI-compatible gateway | Multi-key rotation and backup-endpoint failover | [OpenAI-compatible gateway](./examples-openai-compat.md) |
| Team repository | Project-level `.chord/`, hooks, shared commands, multi-agent roles | [Team setup](./examples-team.md) |

These examples are starting points, not rigid templates. For field semantics and the full config surface, see the [Configuration cheatsheet](../configuration.md#configuration-cheatsheet).

## Context and output limits

The bundled example configs set `limit.context`, `limit.input`, and `limit.output` per model. For what each field means — and how Chord derives the input budget when a provider does not publish a separate input cap — see the [Glossary](../glossary.md). For how those limits interact with compaction, see [Configuration — Compaction](../configuration.md#context-compaction).

## Where things go

| File | Path |
| --- | --- |
| Global config | `~/.config/chord/config.yaml` |
| Credentials | `~/.config/chord/auth.yaml` |
| Project-level overrides | `<repo>/.chord/config.yaml` |
| Global custom agents | `~/.config/chord/agents/<name>.md` |
| Project-level custom agents | `<repo>/.chord/agents/<name>.md` |
| Project-level slash commands | `<repo>/.chord/commands/<name>.md` |

For the full layout and project-key resolution rules, see [Paths](../paths.md).
