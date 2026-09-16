# Quickstart

## 1. Install

Prebuilt binaries do not require Go. Installing with `go install` or building from source requires Go 1.27.0+.

```bash
# Install from source with Go
go install github.com/keakon/chord/cmd/chord@latest

# Or build the local checkout
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

Open your project in an interactive terminal and start Chord:

```bash
cd my-project
chord
```

If `config.yaml` is missing, the setup wizard offers two ways to connect:

- **API key**: have your provider's full API URL, model name, and key ready. You can also enter a proxy URL if needed.
- **Codex OAuth**: follow the sign-in prompts without entering an API key manually.

The wizard creates a minimal `config.yaml` and, when needed, `auth.yaml`, then shows where it saved them. It reuses matching credentials when possible. Chord also creates the project's `.chord/` directory as needed.

Prefer to write configuration yourself? Start with an [example](./examples/index.md). See [Configuration & Auth](./configuration.md) for endpoint formats, credentials, and model pools. For setup without an interactive terminal, see [Troubleshooting](./troubleshooting.md).

## 3. Check the connection

After setup, you can send your first message. If an API-key configuration cannot reach the model, exit Chord and run:

```bash
chord doctor models
```

Resolve authentication or connection errors before running `chord` again. See [Troubleshooting](./troubleshooting.md) for help.

> `go run ./cmd/chord/` only works in the Chord source checkout. Use the installed `chord` command in your own project instead.

## 4. First interaction

Describe what you want and press `Enter`. For example, have it read through your project first:

```text
Explain this project's main modules and how to run its tests. Do not change any files yet.
```

Read the response and tool results. If an action needs approval, inspect it before deciding whether to allow it. Once you know your way around, ask for a specific change and review the resulting diff.

To quit, press `Esc` to enter Normal mode, then `q`; alternatively, press `Ctrl+C` twice within 2 seconds.

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

Read in this order:

1. [Permissions & Safety](./permissions-and-safety.md): set approval rules before the first edit.
2. [Usage](./usage.md): daily controls, sessions, and long tasks.
3. [Configuration & Auth](./configuration.md): providers, credentials, and model pools.
4. [Customization](./customization.md): roles, skills, and project setup.
5. [Troubleshooting](./troubleshooting.md): when something fails.
