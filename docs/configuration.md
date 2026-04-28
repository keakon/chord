# Configuration & Auth

Chord separates behavior configuration and credentials:

- `~/.config/chord/config.yaml`: providers, models, extensions, defaults
- `~/.config/chord/auth.yaml`: API keys / OAuth credentials
- `.chord/config.yaml`: project-level overrides
- `~/.config/chord/agents/` and `.chord/agents/`: agent role definitions

## Configuration layers

A practical precedence model is:

1. Built-in defaults
2. Global config
3. Project config
4. Agent-level config

This lets you keep personal defaults, project-specific behavior, and per-agent
capabilities separate.

## Minimal provider config

### Anthropic

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

### OpenAI Chat Completions

```yaml
providers:
  bigmodel:
    type: chat-completions
    api_url: https://open.bigmodel.cn/api/paas/v4/chat/completions
    models:
      glm-5.1:
        limit:
          context: 200000
          output: 131072
```

### OpenAI Responses

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.5:
        limit:
          context: 1000000
          output: 128000
```

### OpenAI Codex preset

```yaml
providers:
  codex:
    preset: codex
    type: responses
    models:
      gpt-5.4:
        limit:
          context: 1000000
          output: 128000
```

If `type` is omitted, Chord auto-detects it from provider config:

- `preset: codex` → `responses`
- `api_url` ending in `/responses` → `responses`
- `api_url` ending in `/chat/completions` → `chat-completions`
- `api_url` ending in `/messages` → `messages`

If none of these rules match, set `type` explicitly.

## auth.yaml

Provider keys must match the provider name in `config.yaml`:

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"

openai:
  - "$OPENAI_API_KEY"
```

You can list multiple keys for rotation or backup.

## Environment variables in auth.yaml

Provider credentials in `auth.yaml` support environment-variable expansion for scalar API-key values:

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"

openai:
  - "${OPENAI_API_KEY}"
```

Expansion is applied when the scalar starts with `$`. Unset variables expand to an empty string and are filtered out, unless the YAML value is a literal empty string. This expansion applies to `auth.yaml` credentials, not generally to every field in `config.yaml`.

If you intentionally need an empty API key, write a literal empty string:

```yaml
local-provider:
  - ""
```

Do not rely on an unset environment variable for this case. An unset `$ENV_VAR` is treated as a missing credential and is filtered out.

## OAuth

Only providers with `preset: codex` are treated as OAuth providers.

```bash
# auto-select a configured codex provider
chord auth

# explicitly choose a provider
chord auth codex

# headless / SSH environments
chord auth codex --device-code
```

## Selecting provider/model

The main agent's initial provider/model comes from the active `builder` agent
configuration when it defines `models`. If no `builder` model is configured,
Chord falls back to the first configured provider/model in alphabetical order.

```yaml
# ~/.config/chord/agents/builder.yaml or .chord/agents/builder.yaml
models:
  - anthropic/claude-opus-4.7
  - openai/gpt-5.5
```

The ordered `models` list is also the runtime fallback pool for that agent.

## Reusing model templates with YAML anchors

Chord does not have a special `model_templates` schema field. You can still use
YAML anchors and merge keys to avoid repeating model limits and variants. The
top-level `model_templates` key below is just an ignored anchor container.

```yaml
model_templates:
  gpt-400k: &gpt-400k
    limit:
      context: 400000
      output: 128000
    reasoning:
      summary: auto
    text:
      verbosity: medium
    variants:
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

  gpt-1m: &gpt-1m
    limit:
      context: 1000000
      output: 128000
    reasoning:
      summary: auto
    text:
      verbosity: medium
    variants:
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

  claude-1m: &claude-1m
    limit:
      context: 1000000
      output: 65536
    thinking:
      type: adaptive
      effort: medium
    variants:
      high:
        thinking:
          type: adaptive
          effort: high
      xhigh:
        thinking:
          type: adaptive
          effort: xhigh
    modalities:
      input: [text, image]

providers:
  codex:
    preset: codex
    models:
      gpt-5.2: *gpt-400k
      gpt-5.4: *gpt-1m

  openai:
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.2: *gpt-400k
      gpt-5.4: *gpt-1m
      gpt-5.5: *gpt-1m

  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.7: *claude-1m
```

Model fields used in the example:

- `limit.context`: model context window size.
- `limit.output`: model maximum output token capacity. Runtime requests are also
  capped by `max_output_tokens`.
- `reasoning`: OpenAI reasoning options, mainly for Responses-style reasoning
  models. `summary` controls reasoning summary output; variants commonly
  override `reasoning.effort`.
- `text.verbosity`: OpenAI text verbosity hint, where supported.
- `thinking`: Anthropic extended-thinking options. `type: adaptive` lets Chord
  derive an appropriate thinking budget from `effort`; variants can override
  `thinking.effort`.
- `variants`: named model parameter presets. Use a model ref like
  `openai/gpt-5.5@high` or `anthropic/claude-opus-4.7@xhigh` to select one.
- `modalities.input`: supported input modalities. Supported values are `text`,
  `image`, and `pdf`. When omitted, Chord defaults to `text` and `image` for
  backward compatibility.

Only fields defined by Chord's model schema are used. `modalities.output` is
not currently interpreted, so it is intentionally omitted from the example.

## Project-level config

If a project needs local defaults, create this file at the project root:

```text
.chord/config.yaml
```

Common uses include:

- Project-specific permission rules
- Project-specific LSP / MCP / Hooks / Skills settings

## Provider request compression

Provider-level `compress` controls gzip compression for upstream request bodies.
It is different from context compaction: it only changes HTTP request transfer
encoding and does not summarize or remove conversation history.

```yaml
providers:
  openai:
    compress: true
```

When enabled, Chord gzip-compresses the request body only if compression reduces
the payload size; otherwise it sends the request uncompressed. Leave this unset
unless your provider or gateway benefits from compressed request bodies.

## Output token cap

Use `max_output_tokens` to set a global cap on requested output tokens. The effective request limit is still clamped by each model's `limit.output` and available context.

```yaml
max_output_tokens: 32000
```

## Local TUI options

These options affect the local TUI. They can be set in the global config and
can also be overridden by project-level `.chord/config.yaml` when appropriate.

```yaml
desktop_notification: true
ime_switch_target: com.apple.keylayout.ABC
prevent_sleep: true
```

- `desktop_notification`: enables OSC 9 terminal notifications in local TUI
  mode, mainly when the terminal is unfocused. Chord sends notifications for
  events such as permission confirmations, questions waiting for input, and
  agents returning to idle.
- `ime_switch_target`: uses `im-select` (`im-select.exe` on Windows) to switch
  to the specified input method when entering Normal mode, and restore the
  previous input method when returning to Insert mode. This is useful when you
  want command keys to use an English keyboard layout.
- `prevent_sleep`: prevents macOS idle sleep while any agent is active. It is
  only effective in local TUI mode.

## WebFetch

`WebFetch` uses a built-in browser-like `User-Agent` by default. You can override it in config when a site needs a different header:

```yaml
web_fetch:
  user_agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36
```

This setting works in both global config and project-level `.chord/config.yaml`; project config overrides the global value.

You can also configure a proxy for WebFetch requests:

```yaml
web_fetch:
  proxy: socks5://127.0.0.1:1080  # http, https, socks5 supported
```

- `proxy: nil` (default) — inherits the global `proxy` setting
- `proxy: ""` (empty string) — explicitly disables proxy ("direct" mode)
- `proxy: "http://..."`, `"https://..."`, `"socks5://..."` — uses specified proxy

`WebFetch` intentionally remains a lightweight static HTTP reader. It does not run a local browser; JS-heavy pages may be marked as `Content-Quality: suspect-shell` when the returned HTML looks like an application shell rather than readable content.

## MCP

MCP servers can expose many tools. Use `allowed_tools` to expose only selected remote tool names and avoid sending unused tool schemas to the model:

```yaml
mcp:
  search:
    url: https://mcp.exa.ai/mcp
    allowed_tools:
      - web_search_exa
      - web_fetch_exa
```

The server name (`search` above) is user-defined. With this example, Chord registers only `mcp_search_web_search_exa` and `mcp_search_web_fetch_exa`. Filtered tools are not registered and do not enter the LLM tool surface.

## Agent config

Built-in roles include `builder` and `planner`. You can also add custom agents
or override built-ins. Agent files can live in:

- `~/.config/chord/agents/`
- `.chord/agents/`

Supported file formats:

- `.md`: YAML frontmatter plus a Markdown body. The body becomes the system prompt.
- `.yaml` / `.yml`: plain YAML. Use `prompt` or `system_prompt` for the system prompt.

Markdown agent example:

```markdown
---
name: backend-coder
description: Backend developer
mode: subagent
models:
  - anthropic/claude-opus-4.7
permission:
  Write: ask
  Edit: ask
---

You are an agent focused on backend development.
```

Equivalent YAML agent example:

```yaml
name: backend-coder
description: Backend developer
mode: subagent
models:
  - anthropic/claude-opus-4.7
permission:
  Write: ask
  Edit: ask
prompt: |
  You are an agent focused on backend development.
```

Common fields include:

- `name`: agent name. If omitted, Chord uses the filename without extension.
- `description`: short description shown to the main agent when delegation is available.
- `mode`: `subagent` or another role mode; empty defaults to subagent behavior.
- `models`: ordered model pool for this agent, including fallback order. Inline variants such as `openai/gpt-5.5@high` are supported.
- `variant`: default variant when a model ref does not include `@variant`.
- `permission`: per-tool permission policy for this agent.
- `mcp`: MCP config scoped to this agent.
- `delegation`: limits such as `max_children`, `max_depth`, and `child_join`.
- `prompt` / `system_prompt`: system prompt for plain YAML files.

## Context compaction

When the main conversation approaches the model context limit, Chord can run
durable compaction automatically. Common settings:

```yaml
context:
  auto_compact: true
  compact_threshold: 0.8
  compact_model: openai/gpt-5.4-mini
```

Keeping automatic compaction enabled is recommended when your selected models
have smaller context windows.

## Provider connectivity check

```bash
# test all providers
chord test-providers

# test one provider
chord test-providers --provider openai
```

This command is useful as an auth and basic connectivity smoke test.

## Related

- [Quickstart](./quickstart.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Customization](./customization.md)
- [Troubleshooting](./troubleshooting.md)
