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

## 2. First run

Run `chord` in an interactive terminal. If `config.yaml` is missing, Chord launches a one-time setup wizard.
The wizard creates the minimal `config.yaml` and, when needed, `auth.yaml`, reuses matching existing `auth.yaml` credentials when possible, and then prints the exact paths it used.
If stdin is redirected but Chord can still get a controlling TTY, the wizard uses that TTY. If no controlling TTY is available, Chord exits immediately with an initialization error instead of waiting for input.

If you prefer to write YAML manually instead of using the wizard, see [Configuration & Auth](./configuration.md) or the copy-paste-ready [Examples](./examples/index.md).

For API-key setup, the wizard provides one API-key provider path. It asks for an API URL ending in one of these suffixes, with examples in the prompt:

- `/responses` — OpenAI Responses API / compatible gateways
- `/messages` — Anthropic Messages API / compatible gateways
- `/chat/completions` — OpenAI Chat Completions compatible gateways
- `/models` — Gemini Generate Content base path

Based on that endpoint, Chord recommends a starter provider name and model such as `openai` / `gpt-5.5`, `anthropic` / `claude-opus-4.7`, or `gemini` / `gemini-3.1-pro-preview`.

If your provider requires a proxy, the wizard can also write a proxy URL into `config.yaml`. It shows examples such as `http://127.0.0.1:1080` and `socks5://127.0.0.1:1080`.

If you chose the API-key provider path, verify the configured models with:

```bash
chord doctor models
```

If you choose the Codex OAuth path, the wizard completes OAuth sign-in before setup finishes. It creates a `preset: codex` provider and configures these starter models automatically: `gpt-5.2`, `gpt-5.3-codex`, `gpt-5.4`, and `gpt-5.5`.

For later continuous-execution workflows, remember that loop mode exits only through the `Done` tool, and `Done` now requires a final completion report in its `report` argument. Note that loop mode automatically disables context compaction and reduction so long-running tasks retain their full working state.

## 3. Run

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

## 4. First interaction

After startup:

1. Type your question directly
2. Press `Enter` to send
3. Press `Esc` to enter Normal mode
4. Press `q` to quit, or press `Ctrl+C` twice within 2 seconds

Try a simple first message, for example:

```text
Please read the current project structure first, then summarize its main modules.
```

## 5. Common startup commands

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

## 6. Next

- [Usage](./usage.md)
- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Customization](./customization.md)
- [Troubleshooting](./troubleshooting.md)
