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

Read model limit fields in this order:

- `limit.context`: the total window. For most models, input + requested output just needs to fit inside this number.
- `limit.input`: a separate input cap. Only set this when the provider publishes one; otherwise Chord derives the input budget from `limit.context` minus effective requested output.
- `limit.output`: the model's own output capacity. Actual requests are also capped by `max_output_tokens` and by any remaining room in `limit.context`.

Chord uses `limit.input` when present, or the derived input budget otherwise, to decide when to compact before the prompt is too large and how to recover after a provider rejects a request as too large. If `context.compaction.reserved` is set, Chord subtracts that headroom before applying `compact_threshold`.

For `gpt-5.5`, Chord's public examples use `context=400000`, `input=272000`, `output=128000`. Chord still defaults `max_output_tokens` to `32000`, so actual requests use the smaller output limit unless you raise it. Provider docs sometimes call this setup split limits; see [Glossary](../glossary.md).

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
