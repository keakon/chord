# Chord

[![CI](https://github.com/keakon/chord/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/keakon/chord/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/keakon/chord?display_name=release)](https://github.com/keakon/chord/releases) [![Go Version](https://img.shields.io/github/go-mod/go-version/keakon/chord)](./go.mod) [![License](https://img.shields.io/github/license/keakon/chord)](./LICENSE)

📖 **Docs site:** <https://keakon.github.io/chord/> · **中文版：** [README_CN.md](./README_CN.md)

**Calm AI coding in your terminal.** A lightweight, local-first coding agent built for long sessions that do not fall over, model setups you can swap at runtime, and remote operation when you cannot be at the keyboard.

- Companion gateway: [keakon/chord-gateway](https://github.com/keakon/chord-gateway) — drive Chord from WeChat, Feishu, and other chat surfaces

## Three-step setup

Requires Go 1.26+.

```bash
# 1. Install
go install github.com/keakon/chord/cmd/chord@latest

# 2. Configure provider, model pool, and credentials
mkdir -p ~/.config/chord && chmod 700 ~/.config/chord
cat > ~/.config/chord/config.yaml <<'YAML'
providers:
  openrouter:
    type: chat-completions
    api_url: https://openrouter.ai/api/v1/chat/completions
    models:
      openai/gpt-5.5:
        limit:
          context: 1000000
          output: 128000
        modalities:
          input: [text, image]
model_pools:
  default:
    - openrouter/openai/gpt-5.5
YAML
cat > ~/.config/chord/auth.yaml <<'YAML'
openrouter:
  - "$OPENROUTER_API_KEY"
YAML

# 3. Run from your project
cd my-project && chord
```

For other OpenRouter models or different OpenAI-compatible providers, see [Quickstart](./docs/quickstart.md). For ready-to-copy config files, see [Examples](./docs/examples/index.md).

### macOS release downloads

If you download a macOS binary from [GitHub Releases](https://github.com/keakon/chord/releases), macOS may block the first run because the file came from the internet and is not notarized. Remove the quarantine attribute and make the binary executable:

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

## Why Chord

- **Long sessions do not crash.** Auto-compaction keeps a long conversation usable past the model's context window — earlier turns are summarized into a context summary while what is needed to continue is preserved. No more "wait, did it forget?".
- **You see the network state.** While waiting for a model response, Chord shows precise request status and elapsed wait time. Never wonder if it is stuck again.
- **Keyboard-first, Vim-style.** Insert / Normal modes, message search, Vim-flavoured navigation, automatic input-method switching across modes. Quitting takes two taps so you do not lose work to a stray Ctrl+C.
- **Hot-swap model setups.** Group models into reusable pools (`fast`, `thinking`, `cheap`, …); switch the active pool at runtime via `/models` or `Ctrl+P`. Each agent picks its own pool; the runtime falls back through the ordered list automatically.
- **Runs for days on a small VPS.** Low memory and CPU footprint. Power-aware on macOS: prevents idle sleep while work is active, lets the system sleep again when idle.
- **Drive it remotely.** `chord headless` exposes a stdio JSONL control plane; pair with [chord-gateway](https://github.com/keakon/chord-gateway) to operate Chord from any chat surface.

A few extras you may appreciate later:

- **Multi-agent collaboration** — a main agent with focused SubAgents, each with its own context, switchable via `Shift+Tab`.
- **Parallel work via git worktrees** — `chord --worktree feat-auth` spins up an isolated working copy so several tasks on the same repo do not stomp on each other.
- **LSP-backed code awareness** — live diagnostics and definition / references / implementation lookups via your local language servers.
- **Multimodal input** — paste images from the clipboard, attach files, preview in supported terminals.
- **Codex quota visibility** — real-time remaining-quota and reset-time display for OpenAI Codex subscriptions.

## When Chord shines

- **Always-on assistant on a small VPS.** Low resource budget plus reliable long sessions means you can leave it running and check in throughout the day.
- **Mixed-model setups.** Use a strong model for thinking, a fast one for navigation, a cheap one for compaction — switch with one keystroke when the wind changes.
- **Operating from your phone.** Expose `chord headless` through chord-gateway and drive a real coding agent from a chat app while you are away from the desk.

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
