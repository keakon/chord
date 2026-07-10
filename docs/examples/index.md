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
If you already know the provider/model you want and only need a minimal copy-paste snippet, start with [Model configuration recipes](../model-configs.md).

## Agent file formats

Agent definitions can be written as `.md`, `.yaml`, or `.yml` files. The examples use `.md` because front matter keeps structured settings near a Markdown prompt body; use YAML files when you prefer a single plain YAML document. Both formats are loaded from the same global and project-level `agents/` directories.

## Context and output limits

The bundled example configs set `limit.context` and `limit.output` per model; examples include `limit.input` only when the provider publishes a separate input cap. For what each field means — and how Chord derives the input budget when a provider does not publish a separate input cap — see the [Glossary](../glossary.md). For how those limits interact with compaction, see [Configuration — Compaction](../configuration.md#context-compaction).

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
