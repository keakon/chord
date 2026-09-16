# Chord

[![CI](https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/keakon/chord/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/keakon/chord?display_name=release)](https://github.com/keakon/chord/releases) [![Go Version](https://img.shields.io/github/go-mod/go-version/keakon/chord)](./go.mod) [![License](https://img.shields.io/github/license/keakon/chord)](./LICENSE)

📖 **Docs site:** <https://keakon.github.io/chord/>

🌐 [中文介绍](./README_CN.md)

**Finish coding tasks faster, spend less on each one, and keep memory small.** A lightweight terminal coding agent for long sessions: it keeps context clean and switches models automatically when one is unavailable.

<p align="center">
  <img src="./docs/assets/screenshot.png" alt="Chord terminal UI screenshot" width="900">
</p>

## What else Chord does

- Keyboard-first, Vim-style keybindings
- Notifies you only when you're needed
- Hot-swap model pools without restarting
- Drive it remotely through `chord headless`
- Import sessions from Claude Code, Codex, and OpenCode
- LSP integration
- Preview images in the terminal
- Codex subscription quota and reset time
- Customizable agent teams
- Parallel work via git worktrees

## Three-step setup

### 1. Install

If you already have Go 1.27.0+ installed:

```bash
go install github.com/keakon/chord/cmd/chord@latest
```

Source builds require Go 1.27.0 or newer; the default `GOTOOLCHAIN=auto` downloads the required toolchain when needed.

If you do not have Go 1.27.0+, download the archive for your OS/architecture from [GitHub Releases](https://github.com/keakon/chord/releases), extract it, put `chord` on your `PATH`, and run:

```bash
chord --version
```

On macOS, the downloaded binary may be blocked on first run because it came from the internet and is not notarized; see [Quickstart](./docs/quickstart.md#1-install) for the `xattr` / `codesign` commands that unblock it.

### 2. Run the setup wizard

Run `chord` in an interactive terminal:

```bash
chord
```

If `config.yaml` is missing, Chord launches a one-time setup wizard: it creates the minimal `config.yaml` and, when needed, `auth.yaml`, then prints the exact paths it used.

To write YAML manually or use a different provider/model setup, see [Quickstart](./docs/quickstart.md).

### 3. Run from your project

```bash
cd my-project && chord
```

For manual provider/model setup and the `limit` fields, see [Quickstart](./docs/quickstart.md) and the [Glossary](./docs/glossary.md); ready-to-paste `config.yaml` files are in [example configs](./docs/examples/index.md).

## Documentation

- [Docs home](./docs/index.md)
- Getting started: [Quickstart](./docs/quickstart.md) · [Usage](./docs/usage.md) · [Glossary](./docs/glossary.md)
- Reference: [CLI](./docs/cli.md) · [Configuration & Auth](./docs/configuration.md) · [Context management](./docs/context-management.md) · [Model configuration recipes](./docs/model-configs.md) · [Built-in tools](./docs/tools.md) · [Edit tools](./docs/edit-tools.md) · [Keybindings](./docs/keybindings.md) · [Paths](./docs/paths.md) · [Environment variables](./docs/environment.md) · [Platform support](./docs/platforms.md) · [Performance](./docs/performance.md)
- Going further: [Customization](./docs/customization.md) · [Hooks](./docs/hooks.md) · [Examples](./docs/examples/index.md)
- Integration: [Headless](./docs/headless.md) (`chord headless`)
- Safety: [Permissions & Safety](./docs/permissions-and-safety.md)
- Troubleshooting: [Troubleshooting](./docs/troubleshooting.md)

## Measured results

On a [DeepSWE v1.1 task](https://deepswe.datacurve.ai/data/v1.1/tasks/httpx-streaming-json-iteration) that adds streaming JSON iteration to `httpx`, Chord v0.8.1 finished in 6m37s, using 54.5K input tokens, 2.96M cache-read tokens, and 58.6K output tokens, at an estimated $0.052. Among six agent harnesses run on the same task with deepseek-v4.1-flash, Chord finished first and cost the least: 33% faster and 34% cheaper than the next-best run. Memory stays small too: 30MB with an empty session and 39MB after 200 messages.

These measurements come from one task and one memory scenario; your numbers will differ. Full tables (including app memory) and methodology: [Performance](./docs/performance.md).

## Project links

- Companion: [keakon/chord-gateway](https://github.com/keakon/chord-gateway)
- [Contributing](./CONTRIBUTING.md)
- [Changelog](./CHANGELOG.md)
- [Issues](https://github.com/keakon/chord/issues)

## Platform support

Chord is developed and tested primarily on macOS. Linux works well; Windows mostly works but may have undiscovered bugs. Some features (`prevent_sleep`) are macOS-only and silently no-op elsewhere. See [Platform support](./docs/platforms.md) for the per-feature matrix.

## Acknowledgements

Chord is built on [Bubble Tea](https://github.com/charmbracelet/bubbletea), with design and feature inspiration from Claude Code, Codex, OpenCode, and Crush. Most of its development was assisted by GPT-5.4/5.5. Thanks to the many community-run API proxies on [linux.do](https://linux.do/) for providing token access.

## License

MIT License. See [LICENSE](./LICENSE).
