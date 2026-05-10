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

## Split-limit models

When a provider publishes split limits, prefer setting all of:

- `limit.context`: total window
- `limit.input`: input-side budget
- `limit.output`: output cap

Chord uses `limit.input` for auto-compaction thresholds, oversize recovery, and other input-budget decisions. It only falls back to `limit.context` when `limit.input` is omitted for backward compatibility. If `context.compaction.reserved` is set, Chord subtracts that headroom before applying `compact_threshold`.

For `gpt-5.5`, Chord's public examples use the conservative baseline `context=400000`, `input=272000`, `output=32000`. That keeps auto-compaction and oversize recovery aligned with the commonly observed input-side cap. If you have verified provider-specific numbers for a different runtime, override them explicitly in your own config.

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
