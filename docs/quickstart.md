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

Then edit `~/.config/chord/auth.yaml` and choose one credential setup.

### Option A: Anthropic

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
```

### Option B: OpenAI-compatible API

```yaml
openai-compatible:
  - "$OPENAI_API_KEY"
```

### Option C: OpenAI ChatGPT / Codex OAuth

Add a provider in `~/.config/chord/config.yaml` first:

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
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.7:
        limit:
          context: 1000000
          output: 128000
```

If you use an OpenAI-compatible API, change `type` and `api_url` accordingly.

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
