# Chord

[![CI](https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/keakon/chord/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/keakon/chord?display_name=release)](https://github.com/keakon/chord/releases) [![Go Version](https://img.shields.io/github/go-mod/go-version/keakon/chord)](./go.mod) [![License](https://img.shields.io/github/license/keakon/chord)](./LICENSE)

📖 **Docs site:** <https://keakon.github.io/chord/>

🌐 [中文介绍](./README_CN.md)

**Calm AI coding in your terminal.** A lightweight, local-first coding agent built for long sessions that do not fall over, model setups you can swap at runtime, and remote operation when you cannot be at the keyboard.

- Companion gateway: [keakon/chord-gateway](https://github.com/keakon/chord-gateway) — drive Chord from WeChat, Feishu, and other chat surfaces

<p align="center">
  <img src="./docs/assets/screenshot.png" alt="Chord terminal UI screenshot" width="900">
</p>

## Performance

### Real-world coding task

We benchmarked Chord v0.6.3 against Codex-CLI v0.136.0 on a [real-world database system task](https://github.com/datacurve-ai/deep-swe/tree/main/tasks/pebble-durability-wait-apis): implementing durability wait APIs in Pebble. Far from simple CRUD, the task requires understanding commit/WAL sync and concurrency semantics, reasoning across write paths, event listeners, and DB lifecycle subsystems.

| Metric | Chord v0.6.3 | Codex-CLI v0.136.0 | Improvement |
|--------|--------------|---------------------|-------------|
| **Time** | **46m21s** | 61m18s | **24% faster** |
| **LLM calls** | **93** | 118 | **21% fewer** |
| **Input tokens** | **6.86M** | 18.47M | **63% fewer** |
| **Output tokens** | **25K** | 74K | **66% fewer** |
| **Cache read tokens** | **6.55M** | 17.64M | **63% fewer** |
| **Cost** | **$5.58** | $15.15 | **63% cheaper** |

Notes:

- Both runs used GPT-5.5 (xhigh).
- Time excludes environment setup and final wrap-up, but includes model interaction, code changes, and test execution.
- The task's reference solution spans 8 files and 670 changed lines; actual model output may be larger or smaller depending on tests, comments, and implementation choices.

### App startup and memory

We also measured the interactive app shell: time from launch to accepting input, normal exit time, and memory with an empty session and after loading 200 messages.

| App | Startup to input | Normal exit | Empty session memory | 200-message memory |
|-----|------------------|-------------|----------------------|--------------------|
| Chord v0.6.3 | **<1s** | **<1s** | **31.6MB** | **~40MB** |
| Codex-CLI v0.136.0 | **<1s** | ~20s | 35.8MB | ~80MB |
| Claude Code v2.1.163 | 32s | ~2s | 156.3MB | >300MB |

Notes:

- Codex-CLI waits for shutdown wrap-up and exits after about a 20-second timeout.
- Claude Code waits on startup and becomes ready for input after about a 30-second timeout.
- Memory use varies by session content and environment, so these numbers are only estimates for this measured scenario.

## Why Chord

Start with the core experience you notice immediately:

- **Long sessions use less context.** Chord trims stale tool output at request time and keeps typed summaries for large search results, JSON blobs, build/test logs, and file reads. Before a conversation approaches the model's token limit, it can compact earlier turns in the background. Paired with `/loop`, complex tasks can run continuously for hours while wasting fewer tokens.
- **Stays out of the way.** Chord can load sessions with thousands of messages almost instantly, exits without a shutdown wait, keeps memory usage low, and unloads idle LSP/MCP resources until they are needed again.
- **You see the network state.** While waiting for a model response, Chord shows precise request status and elapsed wait time. Never wonder if it is stuck again.
- **Keyboard-first, Vim-style.** Built for keyboard-heavy workflows: Insert / Normal modes, Vim-flavoured navigation, and message search. Quitting takes two taps so you do not lose work to a stray Ctrl+C.
- **Hot-swap model setups.** Group models into reusable pools (`fast`, `thinking`, `cheap`, …); switch the active pool at runtime via `/models` or `Ctrl+P`. Each agent picks its own pool; the runtime falls back through the ordered list automatically.
- **Drive it remotely.** `chord headless` exposes a stdio JSONL control plane; pair with [chord-gateway](https://github.com/keakon/chord-gateway) to operate Chord from chat surfaces.
- **Bring old sessions in.** `chord import` migrates Claude Code, Codex, and OpenCode sessions into resumable Chord sessions.

Out of the box, you also get these quality-of-life upgrades:

- **Project context** — live LSP diagnostics, definition / reference / implementation lookups, Git status, and `@` file completion.
- **Images and PDFs** — paste images, attach image/PDF files when the active model supports them, preview images in supported terminals, and let image-capable models inspect local PNG/JPEG files with `view_image`.
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
- Reference: [CLI](./docs/cli.md) · [Configuration & Auth](./docs/configuration.md) · [Keybindings](./docs/keybindings.md) · [Paths](./docs/paths.md) · [Environment variables](./docs/environment.md) · [Platform support](./docs/platforms.md) · [Performance](./docs/performance.md)
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

## Acknowledgements

Chord is built on [Bubble Tea](https://github.com/charmbracelet/bubbletea), with design and feature inspiration from Claude Code, Codex, OpenCode, and Crush. Most of its development was assisted by GPT-5.4/5.5. Thanks to the many community-run API proxies on [linux.do](https://linux.do/) for providing token access.

## License

MIT License. See [LICENSE](./LICENSE).
