# Quickstart

This page is for first-time Chord users. The goal is to complete a minimal working setup in a few minutes.

## 1. Install

Requires Go 1.26+.

```bash
# Install from GitHub
go install github.com/keakon/chord/cmd/chord@latest

# Or build from source
go build -o chord ./cmd/chord/
```

You can also download prebuilt binaries from [GitHub Releases](https://github.com/keakon/chord/releases). On macOS, a downloaded binary may be blocked on first run because it came from the internet and is not notarized. If that happens, run:

```bash
xattr -dr com.apple.quarantine /path/to/chord
chmod +x /path/to/chord
/path/to/chord --version
```

If macOS still blocks it, add a local ad-hoc signature:

```bash
codesign --force --sign - /path/to/chord
```

Replace `/path/to/chord` with the actual installed path, such as `/usr/local/bin/chord`.

> When running from source, use `go run ./cmd/chord/` (not `go run cmd/chord/main.go`).

## 2. Configure API keys

Create the config directory first:

```bash
mkdir -p ~/.config/chord
chmod 700 ~/.config/chord
```

Then edit `~/.config/chord/auth.yaml`. For the default OpenRouter setup below:

```yaml
openrouter:
  - "$OPENROUTER_API_KEY"
```

Other providers use the same provider-name key convention, for example `anthropic`, `openai`, or any custom OpenAI-compatible provider name.

For OpenAI ChatGPT / Codex OAuth, add a provider in `~/.config/chord/config.yaml` first:

```yaml
providers:
  openai:
    type: openai
    preset: codex
```

Then run:

```bash
chord auth openai
```

## 3. Create a minimal config

Edit `~/.config/chord/config.yaml`:

```yaml
providers:
  openrouter:
    type: chat-completions
    api_url: https://openrouter.ai/api/v1/chat/completions
    models:
      openai/gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
        modalities:
          input: [text, image]

model_pools:
  default:
    - openrouter/openai/gpt-5.5
```

`providers` defines the API endpoint and available models; `model_pools.default` defines the model pool used by the built-in `builder` / `planner` agents. Both are required. If you only configure a provider, startup will fail because the default model pool cannot be resolved. `builder` does not automatically use every global `model_pools` entry; the built-in config only references `default`. If you override the built-in `builder` agent with a custom agent config, that config must explicitly define `model_pools` or `models`.

If you use another OpenRouter model or any other OpenAI-compatible API, change `api_url`, the provider name, and the model name, then update the `provider/model` reference in `model_pools.default` to match. Read model limits in this order: `limit.context` is the total window; for most models, input + requested output just needs to fit there. If a provider also lists a separate input cap (some GPT models do), add `limit.input`; otherwise Chord falls back to `limit.context`. `limit.output` is the model's own output capacity. Chord's `gpt-5.5` examples use `context=400000`, `input=272000`, `output=128000`. The default requested output cap (`max_output_tokens`) is still `32000`, so real requests use the smaller output limit unless you raise it. See [Glossary](./glossary.md) for the related terms.

## 4. Run

Run Chord from your project directory:

```bash
cd my-project
chord
# or
go run ./cmd/chord/
```

On first run, Chord creates the project-level `.chord/` directory as needed.

For headless control-plane mode:

```bash
chord headless
# or
go run ./cmd/chord/ headless
```

Headless overview: [Headless](./headless.md).

## 5. First interaction

After startup:

1. Type your question directly
2. Press `Enter` to send
3. Press `Esc` to enter Normal mode
4. Press `q` to quit, or press `Ctrl+C` twice within 2 seconds

Try a simple first message, for example:

```text
Please read the current project structure first, then summarize its main modules.
```

## 6. Common startup commands

```bash
# Normal startup; the active model is the first pool in the agent's model_pools list.
# After startup, run /models to inspect pool status, or /models <pool> / Ctrl+P to switch.
# Full pool configuration: ./configuration.md#model-pools-selecting-providermodel
chord

# Resume the most recent session
chord --continue

# Resume a specific session
chord --resume 20260428064910975

# Create or enter a chord-managed git worktree so this task's sessions and
# cache stay isolated from the rest of the project. Combine with --continue
# or --resume to act on the worktree's own session history.
chord --worktree feat-auth
```

For full worktree workflow (list/remove, cross-worktree resume, headless integration), see [Worktrees](./usage.md#worktrees).

## 7. Next

- [Usage](./usage.md)
- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Customization](./customization.md)
- [Troubleshooting](./troubleshooting.md)
