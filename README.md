# Chord

A lightweight, local-first terminal coding agent. Low resource usage, reliable long sessions, flexible model orchestration, Vim-like navigation, multi-agent collaboration, and a headless mode for remote control — designed to make AI coding feel calm and predictable.

- 中文版：see [README_CN.md](./README_CN.md)
- User docs: [docs/index.md](./docs/index.md) (start with [docs/quickstart.md](./docs/quickstart.md))
- Gateway: [keakon/chord-gateway](https://github.com/keakon/chord-gateway)

## Why try Chord?

- **Lightweight and always available** — low memory and low CPU overhead make Chord comfortable on small VPS machines and personal always-on environments.
- **No more “is it stuck?”** — while waiting for a model response, Chord shows precise network/request status and elapsed wait time.
- **Keyboard-first terminal UI** — Vim-like normal/input modes, searchable message history, quick model switching, and automatic input-method switching across modes.
- **Image input** — paste or attach images, preview them in supported terminals, and send to multimodal models.
- **LSP-backed coding context** — connect local language servers for static diagnostics and semantic navigation such as definition/references/implementation.
- **Reliable long sessions** — compaction algorithm compresses long sessions into context summaries while preserving the information needed to continue work.
- **Provider/model/key routing** — multiple provider, model, and API key configuration with automatic retry, failover, and load balancing.
- **Codex quota visibility** — display remaining Codex subscription quota and reset time in real time.
- **Multi-agent collaboration** — main agent with focused subagents, inspect their contexts and switch between views.
- **Remote control** — `chord headless` exposes a stdio JSONL control plane; with `chord-gateway`, control Chord from WeChat, Feishu, and other chat surfaces.
- **Power-aware runtime** — prevents system sleep while work is active and allows sleep again when Chord becomes idle.

## Quickstart

Requires Go 1.26+.

```bash
go install github.com/keakon/chord/cmd/chord@latest
chord
```

For remote gateways, bots, or automation:

```bash
chord headless
```

For credentials, provider setup, and first-run details, follow the [Quickstart](./docs/quickstart.md).

## Documentation

- [Docs home](./docs/index.md)
- [Quickstart](./docs/quickstart.md)
- [Usage](./docs/usage.md)
- [Configuration & Auth](./docs/configuration.md)
- [Permissions & Safety](./docs/permissions-and-safety.md)
- [Customization](./docs/customization.md)
- [Troubleshooting](./docs/troubleshooting.md)
- [Headless](./docs/headless.md)

## Project links

- Contributing: [CONTRIBUTING.md](./CONTRIBUTING.md)
- Changelog: [CHANGELOG.md](./CHANGELOG.md)
- Gateway: [keakon/chord-gateway](https://github.com/keakon/chord-gateway)

## License

MIT License.
