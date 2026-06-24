# Environment Variables

This page lists every environment variable Chord reads, grouped by purpose, plus the precedence rules that decide which value wins.

## Precedence

For paths and the API base, Chord resolves a value with this order:

1. **CLI flag** (e.g. `--state-dir /tmp/chord-state`)
2. **Chord-specific env var** (e.g. `CHORD_STATE_DIR`)
3. **`config.yaml` `paths:` field** (e.g. `paths.state_dir`)
4. **XDG-standard env var** (e.g. `XDG_STATE_HOME`)
5. **Built-in default** (e.g. `~/.local/state/chord`)

For credentials referenced by `auth.yaml`, the variable is expanded only when the YAML scalar starts with `$` or `${...}`. See [Configuration & Auth — Environment variables in auth.yaml](./configuration.md#environment-variables-in-authyaml).

## Path overrides

| Variable               | What it sets                                                                | Default if unset                                                  |
| ---------------------- | --------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| `CHORD_CONFIG_HOME`    | Config home (provider config, agents, skills, custom commands, `auth.yaml`) | `$XDG_CONFIG_HOME/chord` if set, else `~/.config/chord`           |
| `CHORD_STATE_DIR`      | Durable runtime state root (sessions, exports, logs, projects, worktrees)   | `$XDG_STATE_HOME/chord` if set, else `~/.local/state/chord`       |
| `CHORD_CACHE_DIR`      | Rebuildable cache                                                           | `$XDG_CACHE_HOME/chord` if set, else `~/.cache/chord`             |
| `CHORD_SESSIONS_DIR`   | Sessions root only (overrides only the sessions location)                   | `<state-dir>/sessions`                                            |
| `CHORD_LOGS_DIR`       | Logs directory only                                                         | `<state-dir>/logs`                                                |
| `XDG_CONFIG_HOME`      | XDG-standard config root                                                    | `~/.config`                                                       |
| `XDG_STATE_HOME`       | XDG-standard state root                                                     | `~/.local/state`                                                  |
| `XDG_CACHE_HOME`       | XDG-standard cache root                                                     | `~/.cache`                                                        |

For the directory layout these variables affect, see [Paths](./paths.md).

## Credentials referenced by `auth.yaml`

Chord does not read provider keys from the environment directly — it reads `auth.yaml` and expands `$VAR` / `${VAR}` placeholders inside it. Convention is to use `<PROVIDER>_API_KEY` style names, but you can pick any variable name.

```yaml
# ~/.config/chord/auth.yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
openai:
  - "${OPENAI_API_KEY}"
gemini:
  - "$GEMINI_API_KEY"
my-gateway:
  - "$MY_GATEWAY_KEY"        # any variable name works
```

| Common name             | Where it ends up                                          |
| ----------------------- | --------------------------------------------------------- |
| `ANTHROPIC_API_KEY`     | `anthropic` provider, when referenced from `auth.yaml`     |
| `OPENAI_API_KEY`        | `openai` (or `openai-compatible`) provider                |
| `GEMINI_API_KEY`        | Google Gemini provider                                    |
| Any custom `*_API_KEY`  | Whatever provider name you reference it under             |

Notes:

- Unset variables expand to an empty string and are filtered out, unless the YAML value is a literal empty string.
- This expansion is specific to `auth.yaml`. It does not generally apply to every field in `config.yaml`.

## Network proxy

If your network cannot directly reach official Anthropic, OpenAI, Google, or similar APIs, configure a proxy for Chord; otherwise provider requests may time out or fail to connect. You can use standard environment variables, or configure a global/provider-level `proxy` in `config.yaml`.

Chord uses Go's standard proxy resolution (`http.ProxyFromEnvironment`) for outbound HTTP. The standard proxy variables apply directly:

| Variable           | Purpose                                                                                                            |
| ------------------ | ------------------------------------------------------------------------------------------------------------------ |
| `HTTP_PROXY`       | Proxy for `http://` requests                                                                                       |
| `HTTPS_PROXY`      | Proxy for `https://` requests                                                                                      |
| `NO_PROXY`         | Comma-separated host patterns that bypass the proxy                                                                |
| `http_proxy` / `https_proxy` / `no_proxy` | Lowercase variants are also recognized                                                                  |

For per-tool proxy override (e.g. routing only `web_fetch` through a SOCKS5), see [Configuration & Auth — WebFetch](./configuration.md#webfetch).

## Terminal detection (read-only)

These are standard variables Chord inspects; you typically never set them yourself.

| Variable                | Purpose                                                                                       |
| ----------------------- | --------------------------------------------------------------------------------------------- |
| `TERM`                  | Identify the terminal type for capability negotiation                                         |
| `TERM_PROGRAM`          | Identify the terminal emulator (iTerm2, WezTerm, Ghostty, …) for image and notification protocol selection |
| `TERM_PROGRAM_VERSION`  | Used together with `TERM_PROGRAM`                                                             |
| `TMUX`                  | Detect that Chord is running inside tmux                                                      |
| `CMUX_SOCKET` / `CMUX_SOCKET_PATH` | Detect that Chord is running inside cmux; influences the image protocol pipeline and TUI refresh cadence |
| `NO_COLOR`              | When set to any non-empty value, disables ANSI color in startup log output to stderr           |
| `USER` / `USERNAME`     | Used in some diagnostic output                                                                |

Chord disables terminal hard-scroll optimizations for all terminal hosts because those scroll sequences can leave stale rows in Chord's sticky transcript layout. When cmux is detected, Chord also uses a more conservative TUI profile because cmux/libghostty embedding can spend significantly more CPU processing frequent small terminal updates than Ghostty.app. This is automatic and has no user-facing setting: cmux gets lower stream/scroll refresh cadence and no foreground-only visual animation ticks, while terminal title animation remains enabled so background tabs still show activity.

## Terminal capability overrides (images)

Use these only when diagnosing or intentionally overriding auto-detection:

| Variable                 | Purpose                                                                  |
| ------------------------ | ------------------------------------------------------------------------ |
| `CHORD_IMAGE_BACKEND`    | Force image backend: `kitty` / `iterm2` / `none` / `auto` (default auto) |
| `CHORD_IMAGE_INLINE`     | Force inline image support on/off: `1` / `0`                             |
| `CHORD_IMAGE_FULLSCREEN` | Force fullscreen image viewer support on/off: `1` / `0`                  |

Notes:

- Inside `tmux` / `zellij`, Chord conservatively disables image preview by default; these overrides are mainly for debugging or known-good setups.
- WezTerm currently auto-selects the iTerm2 image protocol; Ghostty / kitty auto-select Kitty graphics.

## Development and debugging

| Variable             | Purpose                                                                                                                        |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `CHORD_HOOK_DEBUG`   | Set to `1` to log every hook invocation (input/output/exit code/duration). Verbose; use only when diagnosing hook misbehavior.  |
| `CHORD_PPROF_PORT`   | Set to a port number (e.g. `6060`) to expose Go pprof on `127.0.0.1`. Off by default.                                          |

These are intended for development, troubleshooting, and bug reports — not for daily use.

## A note on `CHORD_API_BASE`

`CHORD_API_BASE` overrides the provider base URL for the whole invocation, the same as passing `--api-base`; the CLI flag wins when both are set. It applies to every provider in that run, so for a persistent per-provider override prefer setting `api_url` on each provider in `config.yaml`.

## Related

- [Paths](./paths.md)
- [CLI — global flags](./cli.md#global-flags)
- [Configuration & Auth](./configuration.md)
- [Troubleshooting](./troubleshooting.md)
