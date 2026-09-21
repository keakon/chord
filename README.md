<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/logo/chord-wordmark-dark.svg">
    <img src="./assets/logo/chord-wordmark-light.svg" alt="Chord" width="360">
  </picture>
</p>

<p align="center"><strong>A faster, cheaper, lighter terminal coding agent.</strong></p>

<p align="center">
  <a href="https://keakon.github.io/chord/">Docs site</a> ·
  <a href="./README_CN.md">中文 README</a>
</p>

<p align="center">
  <a href="https://github.com/keakon/chord/actions/workflows/ci.yml"><img src="https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/keakon/chord/releases"><img src="https://img.shields.io/github/v/release/keakon/chord?display_name=release" alt="Release"></a>
  <a href="./go.mod"><img src="https://img.shields.io/github/go-mod/go-version/keakon/chord" alt="Go Version"></a>
  <a href="./LICENSE"><img src="https://img.shields.io/github/license/keakon/chord" alt="License"></a>
</p>

<p align="center">
  <img src="./docs/assets/screenshot.png" alt="Chord terminal UI: tool calls, patches, and diagnostics on the left; model, usage, todos, and changed files on the right" width="900">
</p>

## Feature highlights

- [Automatic model fallback](./docs/configuration.md#model-pools-selecting-providermodel)
- [Request trimming plus compaction](./docs/context-management.md)
- [Streaming tool early execution](./docs/performance.md#streaming-tool-early-execution)
- [Small memory footprint](./docs/performance.md#app-memory)
- [Import Claude Code, Codex, and OpenCode sessions](./docs/usage.md#importing-external-sessions)
- [Vim-style keyboard controls](./docs/keybindings.md)

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

### 2. Start in your project

Open your project in an interactive terminal:

```bash
cd my-project
chord
```

If `config.yaml` is missing, Chord launches a one-time setup wizard: it creates the minimal `config.yaml` and, when needed, `auth.yaml`, then prints the exact paths it used.

To write YAML manually or use a different provider/model setup, see [Quickstart](./docs/quickstart.md).

### 3. Send your first task

Describe what you want and press `Enter`. For example, have it read through your project first:

```text
Explain this project's main modules and how to run its tests. Do not change any files yet.
```

Inspect the response and tool results. Once you know your way around, ask Chord to make a specific change.

For manual provider/model setup and the `limit` fields, see [Quickstart](./docs/quickstart.md) and the [Glossary](./docs/glossary.md); ready-to-paste `config.yaml` files are in the [configuration examples](./docs/examples/index.md).

## Documentation

- [Quickstart](./docs/quickstart.md): install and complete your first task
- [Usage](./docs/usage.md): everyday controls, session recovery, and long tasks
- [Choosing models](./docs/model-choice.md) · [Model configuration recipes](./docs/model-configs.md) · [Configuration examples](./docs/examples/index.md): pick a channel, then connect it
- [Permissions & Safety](./docs/permissions-and-safety.md): choose which actions need approval
- [Long tasks](./docs/usage.md#loop-continuous-execution-mode): keep implementation, checks, and fixes moving
- [Customization](./docs/customization.md): configure roles, skills, code diagnostics, and external tools
- [Headless](./docs/headless.md): control Chord from another interface with `chord headless`
- [ACP agent mode](./docs/acp.md): drive Chord from Zed and other ACP clients with `chord acp`
- [Troubleshooting](./docs/troubleshooting.md) · [Full documentation index](./docs/index.md)

## Measured results

Six agent harnesses ran the same [DeepSWE v1.1 task](https://deepswe.datacurve.ai/data/v1.1/tasks/httpx-streaming-json-iteration): adding streaming JSON iteration to `httpx`. All six runs used deepseek-v4.1-flash. Chord 0.8.1 finished first and cheapest: 6m37s and $0.052, using 54.5K input tokens, 2.96M cache-read tokens, and 58.6K output tokens. The next-best run took 1.49× as long and cost 1.51× as much.

### Real-world coding task

| Harness | Time | Cost |
|---------|------|------|
| Chord 0.8.1 | **6m37s** | **$0.052** |
| deepseek-harness 0.1.5-rc.1 | 9m50s (1.49×) | $0.079 (1.51×) |
| pi 0.85.1 | 10m22s (1.57×) | $0.086 (1.65×) |
| codex 0.154.0 | 17m01s (2.57×) | $0.139 (2.67×) |
| mini-swe-agent 2.4.6 | 18m29s (2.79×) | $0.125 (2.40×) |
| claude code 2.1.272 | 22m25s (3.39×) | $0.166 (3.17×) |

Multipliers are relative to Chord.

### App memory

Measured on macOS 15.3.2 (arm64).

| Harness | Empty session | 200 messages | Growth |
|---------|---------------|--------------|--------|
| Chord 0.8.1 | 30MB | **39MB** | **+9MB** |
| codex 0.154.0 | **27MB** | 47MB | +20MB |
| claude code 2.1.273 | 143MB | 216MB | +73MB |

One measured task and one memory scenario; your numbers will differ. Full tables, methodology, and implementation notes: [Performance](./docs/performance.md#measured-results).

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
