# Chord

[![CI](https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/keakon/chord/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/keakon/chord?display_name=release)](https://github.com/keakon/chord/releases) [![Go Version](https://img.shields.io/github/go-mod/go-version/keakon/chord)](./go.mod) [![License](https://img.shields.io/github/license/keakon/chord)](./LICENSE)

📖 **Docs site:** <https://keakon.github.io/chord/> · **中文版：** [README_CN.md](./README_CN.md)

**Calm AI coding in your terminal.** A lightweight, local-first coding agent built for long sessions that do not fall over, model setups you can swap at runtime, and remote operation when you cannot be at the keyboard.

- Companion gateway: [keakon/chord-gateway](https://github.com/keakon/chord-gateway) — drive Chord from WeChat, Feishu, and other chat surfaces

## Why Chord

Start with the core experience you notice immediately:

- **Stable for the long haul.** Before a long conversation approaches the model's token limit, Chord can compact earlier turns in the background; once compaction finishes, it atomically replaces the old history and keeps running while preserving the context needed for follow-up work. Paired with `/loop`, complex tasks can run continuously for hours.
- **You see the network state.** While waiting for a model response, Chord shows precise request status and elapsed wait time. Never wonder if it is stuck again.
- **Keyboard-first, Vim-style.** Insert / Normal modes, message search, Vim-flavoured navigation, automatic input-method switching across modes. Quitting takes two taps so you do not lose work to a stray Ctrl+C.
- **Hot-swap model setups.** Group models into reusable pools (`fast`, `thinking`, `cheap`, …); switch the active pool at runtime via `/models` or `Ctrl+P`. Each agent picks its own pool; the runtime falls back through the ordered list automatically.
- **Extremely lightweight.** Low memory and CPU footprint. Power-aware on macOS: prevents idle sleep while work is active, lets the system sleep again when idle.
- **Drive it remotely.** `chord headless` exposes a stdio JSONL control plane; pair with [chord-gateway](https://github.com/keakon/chord-gateway) to operate Chord from any chat surface — even from your phone when you are away from the desk.

Out of the box, you also get these quality-of-life upgrades:

- **LSP-backed code awareness** — live diagnostics and definition / references / implementation lookups via your local language servers.
- **Multimodal input** — paste images from the clipboard, attach files, preview in supported terminals.
- **Codex quota visibility** — real-time remaining-quota and reset-time display for OpenAI Codex subscriptions.

Once you want to go further, Chord also supports these advanced workflows:

- **Multi-agent collaboration** — a main agent with focused SubAgents, each with its own context, switchable via `Shift+Tab`.
- **Parallel work via git worktrees** — `chord --worktree feat-auth` spins up an isolated working copy so several tasks on the same repo do not stomp on each other.

## Three-step setup

### 1. Install

If you already have Go 1.26.3+ installed:

```bash
go install github.com/keakon/chord/cmd/chord@latest
```

Source builds require Go 1.26.3 or newer because earlier Go 1.26 patch releases contain reachable standard-library vulnerabilities. With the default `GOTOOLCHAIN=auto`, Go downloads the required toolchain automatically when needed.

If you do not have Go 1.26.3+, download a prebuilt binary from [GitHub Releases](https://github.com/keakon/chord/releases). Pick the archive for your OS/architecture, extract it, put `chord` on your `PATH`, and run:

```bash
chord --version
```

### 2. Run the setup wizard once

Run `chord` in an interactive terminal:

```bash
chord
```

If `config.yaml` is missing, Chord launches a one-time setup wizard. The wizard creates the minimal `config.yaml` and, when needed, `auth.yaml`, then prints the exact paths it used.

If you prefer to write YAML manually or need a different provider/model setup, see [Quickstart](./docs/quickstart.md).

### 3. Run from your project

```bash
cd my-project && chord
```

For manual provider/model setup and model-limit guidance, see [Quickstart](./docs/quickstart.md). In short: `limit.context` is the total request window, `limit.output` is the model's output capacity, and `limit.input` is only needed when a provider publishes a separate input cap. See the [Glossary](./docs/glossary.md) for the exact rules and [example configs](./docs/examples/index.md) for ready-to-paste `config.yaml` files.

### Release download notes

GitHub Releases provide prebuilt binaries for supported platforms. On macOS, the downloaded binary may be blocked on first run because the file came from the internet and is not notarized. If that happens, remove the quarantine attribute and make the binary executable:

```bash
xattr -dr com.apple.quarantine /path/to/chord
chmod +x /path/to/chord
/path/to/chord --version
```

If macOS still blocks it, add a local ad-hoc signature:

```bash
codesign --force --sign - /path/to/chord
```

For example, if you installed Chord at `/usr/local/bin/chord`, replace `/path/to/chord` with `/usr/local/bin/chord`.

## Documentation

- [Docs home](./docs/index.md)
- Getting started: [Quickstart](./docs/quickstart.md) · [Usage](./docs/usage.md) · [Glossary](./docs/glossary.md)
- Reference: [CLI](./docs/cli.md) · [Configuration & Auth](./docs/configuration.md) · [Keybindings](./docs/keybindings.md) · [Paths](./docs/paths.md) · [Environment variables](./docs/environment.md) · [Platform support](./docs/platforms.md)
- Going further: [Customization](./docs/customization.md) · [Hooks](./docs/hooks.md) · [Examples](./docs/examples/index.md)
- Integration: [Headless](./docs/headless.md)
- Safety: [Permissions & Safety](./docs/permissions-and-safety.md)
- Troubleshooting: [Troubleshooting](./docs/troubleshooting.md)

## Project links

- Companion: [keakon/chord-gateway](https://github.com/keakon/chord-gateway)
- [Contributing](./CONTRIBUTING.md)
- [Changelog](./CHANGELOG.md)
- [Issues](https://github.com/keakon/chord/issues)

## Platform support

Chord is developed and tested primarily on macOS. Linux works well; Windows mostly works but may have undiscovered bugs. Some features (`prevent_sleep`) are macOS-only and silently no-op elsewhere. See [Platform support](./docs/platforms.md) for the per-feature matrix.

## License

MIT License. See [LICENSE](./LICENSE).
