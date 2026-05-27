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

Project config is loaded from `.chord/config.yaml` without injecting built-in
defaults first, then merged onto the already-loaded global config. Runtime
commands treat the current working directory as the project root, so the
project-layer config is read from `./.chord/config.yaml` under the startup cwd
rather than by searching parent directories. That means:

- omitted project fields stay truly unset instead of silently shadowing global defaults;
- malformed project config is treated as a startup error, not ignored;
- global-only keys such as `paths.*` and `maintenance.*` are ignored in project config;
- most scalar and object values override the global value at the same key;
- `model_pools` merge by pool name, with same-name project pools overriding the global definition;
- append-style extension points keep global entries and add project entries: currently `skills.paths` and per-trigger hook arrays under `hooks.*` append rather than replace.

On the first run, if you launch `chord` in an interactive terminal and `config.yaml` is missing, Chord starts a one-time setup wizard. It writes a minimal `config.yaml` and, when needed, `auth.yaml`, reuses matching existing credentials from `auth.yaml` when possible, then prints the resolved file locations. Redirected stdin does not by itself make startup non-interactive; if Chord can still open the controlling TTY, the wizard uses that TTY. If no controlling TTY is available, it exits instead of waiting for input.

## Streaming tool execution (early execution)

Chord executes a small safe subset of tools *speculatively* while the model response
is still streaming (as soon as tool arguments are complete), instead of waiting
for the provider to fully finalize the response. This reduces the "finalize gap".

- Always enabled; there is no `early_tool_execution` toggle.
- Eligible tools: `Read`, `Grep`, `Glob`, rollback-safe file mutation tools
  (`Write`, `Edit`, `Delete`), plus a conservative read-only subset of
  `Shell` (single command only; no pipes/redirects/`&&`/`;`):
  - `pwd`, `ls`, `cat`, `which`
  - `git status|log|diff|show|branch|rev-parse`
- Not eligible: non-read-only `Shell`, interactive/control tools, or any call that
  requires permission action `ask`.
- Speculative file mutations are real on-disk writes/deletes, but the runtime
  captures pre-state first and rolls them back if finalize discards the call.
  Within a turn, conflicting speculative file mutations for the same path are
  skipped and left to the finalized execution path. Read-like speculative calls
  are also skipped while *any* unpromoted speculative file mutation exists in
  the same turn (regardless of path), so they never observe uncommitted file
  state — at the cost of serializing read speculation behind in-flight writes.
- Speculative results may be shown early in the UI, but they are only appended to
  the conversation context after finalize validation.

## Minimal provider config

### OpenRouter

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
          context: 400000
          input: 272000
          output: 128000
```

Read model limits in this order:

1. `limit.context` is the total window. For most models, input + requested output just needs to fit inside this number.
2. `limit.input` is only needed when the provider also lists a separate input cap. Some GPT models work this way; if you omit it, Chord derives the usable input budget from `limit.context` after reserving effective requested output.
3. `limit.output` is the model's own output capacity. Chord's default requested output cap (`max_output_tokens`) is still `32000`, so real requests use the smaller output limit unless you raise it.

Chord's `gpt-5.5` examples use `context=400000`, `input=272000`, `output=128000`. Provider docs sometimes call this setup split limits; see [Glossary](./glossary.md).

For `type: responses`, Chord follows the Responses request shape used by Codex: the stable system prompt is sent in the top-level `instructions` field, while conversation messages remain typed `input` items such as `{"type":"message","role":"user",...}`. This keeps compatible gateways from receiving a system-role message in `input`.

### OpenAI Codex preset

```yaml
providers:
  codex:
    preset: codex
    type: responses
    models:
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
```

### Google Gemini

```yaml
providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-3.1-pro-preview:
        limit:
          context: 1048576
          output: 65536
```

For Gemini, set `api_url` to the `/models` base path. Chord detects `type: generate-content` from the `/models` suffix, so `type` can be omitted. Do not include the model name or `:streamGenerateContent?alt=sse`; Chord appends `/{model}:streamGenerateContent?alt=sse` automatically. The model map key, such as `gemini-3.1-pro-preview`, is the model ID sent to Gemini.

Gemini thinking options use the same unified `thinking` object as other providers (no separate `gemini_thinking` key):

- `thinking.budget` → `generationConfig.thinkingConfig.thinkingBudget`
  - Gemini: ✅ used
  - Anthropic: ⚠️ only when `thinking.type: enabled` (mapped to Anthropic budget mode)
  - OpenAI: ❌ ignored
- `thinking.include_thoughts` → `generationConfig.thinkingConfig.includeThoughts`
  - Gemini: ✅ used
  - Anthropic / OpenAI: ❌ ignored
- `thinking.level` → `generationConfig.thinkingConfig.thinkingLevel` (`minimal|low|medium|high`, Gemini 3+; not all models support `minimal`)
  - Gemini (3+): ✅ used
  - Gemini 2.x / Anthropic / OpenAI: ❌ ignored

Example:

```yaml
providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-2.5-flash:
        limit:
          context: 1048576
          output: 65536
        thinking:
          budget: -1
          include_thoughts: true
      gemini-3-pro:
        limit:
          context: 1048576
          output: 65536
        thinking:
          budget: -1
          level: high
```

If `type` is omitted, Chord auto-detects it from provider config:

- `preset: codex` → `responses`
- `api_url` ending in `/responses` → `responses`
- `api_url` ending in `/chat/completions` → `chat-completions`
- `api_url` ending in `/messages` → `messages`
- `api_url` ending in `/models` → `generate-content`

If none of these rules match, set `type` explicitly.

## Thinking bilingual appended translation

If your model outputs English thinking / reasoning and you want an appended translation (for example, Chinese) in the TUI, you can enable `thinking_translation`:

```yaml
model_pools:
  translation:
    - openai/gpt-5.4-mini

thinking_translation:
  target_language: zh-Hans
  model_pool: translation
  max_chars: 1000
```

Notes:

- This feature only translates **thinking / reasoning**. It does not translate the assistant final answer.
- Translation uses an LLM provider you already configured. `thinking_translation.model_pool` must point to a top-level `model_pools` entry.
- `target_language` and `model_pool` are both required. If either is missing, the feature is disabled.
- Use a separate low-cost translation pool when possible. The pool can contain multiple `provider/model[@variant]` refs; translation runs a **single fallback round** across pool entries in order: if one candidate fails (including network/5xx/timeout), it moves to the next candidate, and empty, clearly truncated, or wrong-target-language translation results also trigger trying the next candidate.
- `max_chars` limits the thinking preview sent for translation. The default is `1000`; set a smaller value such as `500` for lower latency/cost, or a larger value when you prefer more complete translated thinking. Only the leading `max_chars` runes are translated — any thinking text past this prefix is intentionally not sent to the translation model and will not appear in the translated card.
- The thinking-translation layer does not impose its own whole-translation timeout and does not use a circuit breaker. A temporary failure for one thinking block only skips that block; it does not block later thinking translations or the main response.
- Per-provider request/header/stream idle timeouts still apply at the LLM transport layer. With the default auxiliary client settings, these are one-minute-class timeouts, so a stalled model/key can fail over while the pool still gets a chance to run.
- The translated content is appended under the corresponding thinking card, separated by a neutral header like `Translated · <target_language>`. The translation is rendered with the same Markdown / code-highlighting pipeline and is not written back into model context.
- If a translation model accidentally echoes the internal `<TRANSLATION>` envelope marker, Chord strips that marker before persistence/restoration/rendering so it cannot be interpreted as a Markdown HTML block and break formatting.
- Translations are persisted in the session directory (`thinking_translations.json`) keyed by `(message, block)` and bound to a content hash, and restored when the same session is resumed. A given thinking block is translated at most once: changing `thinking_translation.target_language` later does not re-translate already-stored blocks. Translations are not written back into model context.

More detailed fields are described in the config reference below.

## auth.yaml

Provider keys must match the provider name in `config.yaml`:

The first-run wizard can create this file for you. It supports either literal API keys or `$ENV_VAR` placeholders.

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"

openai:
  - "$OPENAI_API_KEY"
```

You can list multiple keys for rotation or backup.

For `preset: codex` OAuth providers, Chord now keeps frequently changing runtime status (quota snapshots, reset times, last warm-up timestamps, shared OAuth status cache) in `auth.state.yaml`, not in `auth.yaml`.

That split is intentional:

- `auth.yaml` remains the user-edited source of truth for credentials and stable OAuth fields such as `refresh`, `access`, `expires`, `account_id`, and `email`; empty OAuth fields are omitted when Chord rewrites the file, and OAuth `status` does not belong in `auth.yaml`;
- `auth.state.yaml` is machine-managed shared runtime state keyed by `account_id`, so quota / reset updates and account states such as `expired`, `deactivated`, and `invalidated` do not constantly rewrite `auth.yaml` while the user may also be editing it.

For OAuth credentials, `access` must be a token from which Chord can parse the account id; if `auth.yaml` already has `account_id`, it must match the account id inside `access`. Chord can also keep a refresh-only OAuth entry (`refresh` without `access`) and refresh it on first use. An OAuth entry with neither `access` nor `refresh` is unusable.

`expires` is the access-token expiry timestamp in Unix milliseconds. When `access` contains a JWT `exp` claim, Chord uses that value as the most accurate expiry metadata and can cache it in `auth.state.yaml`. A missing or locally expired `expires` value does not by itself mark an OAuth slot `expired`; it only makes that slot less preferred. Chord still tries the existing access token first, and only after an authentication failure will it refresh the credential or mark it expired if recovery is impossible.

A typical `auth.state.yaml` entry looks like:

```yaml
openai:
  openai:account_id:acc-1:
    account_id: acc-1
    expires: 1774009702606
    status: normal
    updated_at: 1774009702606
    last_warmup_at: 1774009702606
    codex_primary_used_pct: 12.5
    codex_primary_window_minutes: 60
    codex_primary_reset_at: 1774013302000
    codex_secondary_used_pct: 40
    codex_secondary_window_minutes: 10080
    codex_secondary_reset_at: 1774600000000
```

The `status` field is authoritative only in `auth.state.yaml`. Chord writes `expired` when an access token can no longer be used and the credential cannot be refreshed (including missing, invalid, expired, or reused refresh tokens), `deactivated` when the service reports a disabled/banned account, and `invalidated` when the account must be re-authenticated. Any non-empty status makes that OAuth slot unselectable until it is cleaned up or replaced.

These cached Codex quota/reset fields are restart-stable scheduling and display hints, not hard blocks by themselves:

- they help startup / first-pick ordering choose accounts that are more likely to still have quota;
- they let key switches immediately show the last cached snapshot before a fresh warm-up completes;
- they do **not** by themselves make the account absolutely unselectable;
- real hard blocking still comes from confirmed request failures and runtime cooldown state.

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

## Provider key selection

When a provider has multiple API keys / OAuth accounts, Chord uses two settings: `key_rotation` controls when Chord reselects a key, and `key_order` controls how Chord chooses among selectable keys.

- `key_rotation: on_failure` (default): keep using the current key until it fails, cools down, or becomes unusable.
- `key_rotation: per_request`: reselect a key before every request; useful for load balancing across independent keys.
- `key_order: sequential` (default for non-Codex providers): choose in stable key order, generally preferring the least-recently-used selectable key.
- `key_order: random`: choose randomly among selectable keys.
- `key_order: smart`: Codex providers only. Prefer healthy OAuth accounts with better quota headroom and reset timing.

`key_rotation` only rotates credentials / API keys. It does not rotate models; model selection still follows the model pool sticky cursor and fallback logic.

Loop mode still follows the configured `key_rotation` / `key_order`. For Codex long-running loops, keep the default `key_rotation: on_failure` if you prefer stable transport/cache continuity; explicitly use `per_request` only when you want to distribute quota across multiple accounts.

## OAuth

Only providers with `preset: codex` are treated as OAuth providers.

For Codex providers, prefer configuring only `preset: codex` plus model settings. Do not manually override preset-managed fields such as `api_url`, `token_url`, `client_id`, `type`, `store`, `responses_websocket`, or `supported_service_tiers` unless you are deliberately testing transport internals. The preset selects the official OAuth transport, Responses endpoint, WebSocket/cache defaults, and service-tier capability. Use `supported_service_tiers` when you need an explicit tier matrix.

Codex OAuth account selection is controlled by `key_rotation` / `key_order` in [Provider key selection](#provider-key-selection). Codex defaults to `key_order: smart`, which considers quota snapshots, soft cooldown, and reset timing when choosing an account.

`smart` ranks selectable Codex OAuth accounts by preferring:

- accounts whose shared cached snapshot shows **both** tracked windows still have remaining quota;
- then accounts where at least one tracked window still has remaining quota;
- then higher remaining headroom when rate-limit snapshots are available;
- then the nearer known reset time when candidates are otherwise similar;
- still falling back to unknown / stale candidates when no better option exists.

When a Codex client becomes active, Chord may also background-probe additional OAuth slots to refresh cached headroom snapshots. That warm-up is best-effort, low-concurrency, cancels when the active client is replaced, and may update persisted OAuth credential status when an account becomes unusable.

Warm-up priority is also state-aware:

- OAuth slots that have never been warmed up in shared state are probed first;
- older cached entries are refreshed before recently refreshed ones;
- after warm-up or polling returns a newer snapshot, Chord writes it to `auth.state.yaml` and other processes adopt it lazily when they next read key-selection or rate-limit state.

```bash
# auto-select a configured codex provider
chord auth

# explicitly choose a provider
chord auth codex

# headless / SSH environments
chord auth codex --device-code
```

## Model pools (selecting provider/model)

Chord selects the active model via **named model pools**. Each pool entry should be a full `provider/model[@variant]` reference so the provider endpoint, auth, protocol, and variant tuning are unambiguous.

Pool definitions live in `config.yaml` (global or project-level). Agent configs
may reference pool names to restrict access; they cannot define inline pools.

### Define model pools in config.yaml

```yaml
# ~/.config/chord/config.yaml or .chord/config.yaml
model_pools:
  thinking:
    - anthropic/claude-opus-4.7
    - openai/gpt-5.5
  non-thinking:
    - anthropic/claude-sonnet-4
```

Project-level `.chord/config.yaml` `model_pools` are merged into the global config
(same-name pools override).

### Reference pools from agents (optional)

Agents do not need to set `model_pools`. If omitted, the agent can use every
pool defined in merged `config.yaml` `model_pools`, sorted by pool name. Add
`model_pools: [...]` only when you want to restrict that agent to a subset or
customize its fallback order.

```yaml
# ~/.config/chord/agents/builder.yaml or .chord/agents/builder.yaml
name: builder
mode: main
model_pools: [thinking, non-thinking]
```

```yaml
# .chord/agents/reviewer.yaml
name: reviewer
mode: subagent
model_pools: [thinking]
```

When no pool is explicitly selected, Chord falls back to the agent's first
allowed pool: the first entry in `model_pools: [...]` when configured, otherwise
the alphabetically first top-level pool.

At runtime, use `/models` to switch the pool for the **current view** (per project,
persisted across restarts). In the main view this means the current main role; in a
SubAgent view it means that SubAgent's agent pool selection. Switching pools updates
the full fallback chain for subsequent LLM calls, even if the currently selected
`provider/model` exists in both pools (in-flight requests keep using their starting
snapshot). You can also set a named
agent directly with `/models --agent <name> <pool>`. For SubAgents, the default behavior
is to use the first allowed pool; switching back to that pool restores the default
behavior.

## Reusing model templates with YAML anchors

Chord does not have a special `model_templates` schema field. You can still use
YAML anchors and merge keys to avoid repeating model limits and variants. The
top-level `model_templates` key below is just an ignored anchor container.

```yaml
model_templates:
  gpt-400k: &gpt-400k
    limit:
      context: 400000
      input: 272000
      output: 128000
    reasoning:
      # Recommended example when you want readable reasoning summaries from
      # OpenAI Responses reasoning models. This is not Chord's implicit default:
      # leave `summary` unset to omit the field and use provider/model behavior.
      summary: auto
    # Optional for OpenAI GPT-5 / Responses API models. Leave unset to use
    # the provider/model default; set low for shorter visible text output or high
    # when you explicitly want more detailed visible output.
    # text:
    #   verbosity: low
    variants:
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

  # Anchor names cannot contain dots in Chord's YAML parser: use _ in the
  # anchor/alias name even when the model key contains dots.
  gpt-5.2: &gpt-5_2
    <<: *gpt-400k
    cost:
      input: 1.75
      output: 14
      cache_read: 0.175
      service_tier_multipliers:
        fast: 2
        slow: 0.5
      input_tiers:
        - above_input_tokens: 272000
          input: 3.5
          output: 28
          cache_read: 0.35

  gpt-5.5: &gpt-5_5
    <<: *gpt-400k
    cost:
      input: 5
      output: 30
      cache_read: 0.5
      service_tier_multipliers:
        fast: 2.5
        slow: 0.5
      input_tiers:
        - above_input_tokens: 272000
          input: 10
          output: 60
          cache_read: 1

  gpt-1m: &gpt-1m
    limit:
      context: 1050000
      input: 922000
      output: 128000
    reasoning:
      # Recommended example when you want readable reasoning summaries from
      # OpenAI Responses reasoning models. This is not Chord's implicit default:
      # leave `summary` unset to omit the field and use provider/model behavior.
      summary: auto
    # Optional for OpenAI GPT-5 / Responses API models. Leave unset to use
    # the provider/model default; set low for shorter visible text output or high
    # when you explicitly want more detailed visible output.
    # text:
    #   verbosity: low
    variants:
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

  gpt-5.4: &gpt-5_4
    <<: *gpt-1m
    cost:
      input: 2.5
      output: 15
      cache_read: 0.25
      service_tier_multipliers:
        fast: 2
        slow: 0.5
      input_tiers:
        - above_input_tokens: 272000
          input: 5
          output: 30
          cache_read: 0.5

  claude-1m: &claude-1m
    limit:
      context: 1000000
      output: 65536
    cost:
      input: 5
      output: 25
      cache_read: 0.5
      cache_write: 6.25
      cache_write_1h: 10
      service_tier_multipliers:
        fast: 6
    thinking:
      type: adaptive
      effort: medium
      display: summarized
    variants:
      high:
        thinking:
          type: adaptive
          effort: high
          display: summarized
      xhigh:
        thinking:
          type: adaptive
          effort: xhigh
          display: summarized
    modalities:
      input: [text, image]

providers:
  codex:
    preset: codex
    # Optional: model entries inherit this provider-level default.
    # Codex preset already defaults to [fast, slow]; set it explicitly when
    # documenting or overriding a non-preset provider.
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.2: *gpt-5_2
      gpt-5.5: *gpt-5_5

  openai:
    api_url: https://api.openai.com/v1/responses
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.4: *gpt-5_4
      gpt-5.5: *gpt-5_5

  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    supported_service_tiers: [fast]
    models:
      claude-opus-4.7: *claude-1m
```

Model fields used in the example:

- `limit.context`: total request window in tokens when the provider exposes it.
- `limit.input`: use this only when the provider also publishes a separate input cap. Chord uses it to decide when to compact before the prompt is too large and how to retry after a provider rejects a too-large request. If omitted, Chord derives the input budget from `limit.context` minus the effective requested output (`max_output_tokens`, capped by `limit.output`). It does not by itself reduce requested output tokens; output clamping follows `limit.output`, `max_output_tokens`, and any total-context (`limit.context`) remainder.
- `limit.output`: model maximum output token capacity. Runtime requests are also
  capped by `max_output_tokens`, so the effective request uses the smaller value.
- `reasoning`: OpenAI / OpenAI-compatible reasoning options.
  - `reasoning.effort` controls how much reasoning depth/budget Chord asks for. Chord currently passes `low`, `medium`, `high`, or `xhigh`; leave it unset to omit the field and use the provider/model default. `medium` is a good starting point for everyday coding, while `high` / `xhigh` trade more latency and token use for harder planning, debugging, or synthesis tasks.
  - For `type: chat-completions`, Chord sends `reasoning.effort` as top-level `reasoning_effort` and uses `max_completion_tokens`.
  - For `type: responses`, Chord sends `reasoning.effort` and `reasoning.summary` inside the `reasoning` object.
  - `reasoning.summary` controls whether Chord explicitly asks for a readable reasoning summary in Responses output. Chord currently accepts `auto`, `concise`, or `detailed`; leave it unset to omit the field, which means Chord does not explicitly request a summary and the provider/model decides its default behavior.
  - Recommended `reasoning.summary` value: `auto` when you want summaries. It lets the provider choose the best supported detail level, avoids pinning templates to a fixed summarizer, and matches current OpenAI guidance. Use `concise` to reduce UI noise or `detailed` when you deliberately want fuller debugging/evaluation output.
  - `reasoning.summary` is only meaningful for models that support Responses reasoning summaries. Some upstream clients/catalogs model “summary off” as `none`; in Chord, the equivalent is leaving `reasoning.summary` unset.
- `text.verbosity`: optional OpenAI text verbosity hint, where supported. Leave it unset in reusable templates unless you intentionally want to override the provider/model default; use `low` for shorter visible text output and `high` for deliberately detailed visible output.
- `thinking`: Anthropic extended-thinking options. `type: adaptive` lets Chord
  derive an appropriate thinking budget from `effort`; `display: summarized`
  asks Claude to return summarized thinking blocks for Chord to show (only
  valid for `type: enabled` or `adaptive`; rejected under `disabled`); variants
  can override `thinking.effort` and `thinking.display`.
- `variants`: named model parameter presets. Use a model ref like
  `openai/gpt-5.5@high` or `anthropic/claude-opus-4.7@xhigh` to select one.
- `cost`: estimated pricing in USD per 1M tokens. `input`, `output`, `cache_read`, `cache_write`, and `cache_write_1h` are all optional, but supplying them lets Chord estimate usage cost in the UI and `/usage` output. `cache_write` is the default prompt-cache write price, typically the 5-minute TTL price for Anthropic; `cache_write_1h` is used when a provider reports or is configured to request 1-hour cache writes. When a matching cache-write price is omitted, Chord estimates cache-write tokens at the effective `input` price.
  - `cost.service_tier_multipliers`: optional per-tier pricing multipliers applied after the base price or matching `input_tiers` price is selected. Use it for provider service tiers such as OpenAI priority (`fast`) or flex (`slow`).
  - `cost.input_tiers`: optional long-context pricing overrides. Each entry uses `above_input_tokens` as a strict threshold; when billable input is greater than that value, Chord uses the highest matching tier's `input`, `output`, `cache_read`, and optional `cache_write` prices before applying any service-tier multiplier.
- `modalities.input`: supported input modalities. Supported values are `text`, `image`, and `pdf`. When omitted, Chord defaults to `text` and `image` for
  backward compatibility.
- `supported_service_tiers`: explicit non-standard service tiers accepted by a provider or model, for example `[fast, slow]` for OpenAI service tiers or `[fast]` for Anthropic speed. Provider-level values act as defaults for all models in that provider; model-level values override provider defaults. If both are omitted, Chord uses preset defaults. Pricing is configured separately with `cost.service_tier_multipliers` for service-tier rates and `cost.input_tiers` for long-context thresholds. `standard` is always available and is not listed in `supported_service_tiers`. Manual switching only selects tiers supported by the current provider/model: `Ctrl+R` cycles through the available set (`standard` plus supported non-standard tiers), and `/tier fast` or `/tier slow` shows an error when the current provider/model does not support that tier. When the active provider/model changes and a previously requested `fast` or `slow` is no longer supported, the info panel still shows the requested `tier: fast` or `tier: slow` in dim strikethrough text so the requested mode remains visible while indicating it is not effective.

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
It is different from context management (compaction / reduction): it only changes HTTP request transfer
encoding and does not summarize or remove conversation history.

```yaml
providers:
  openai:
    compress: true
```

When enabled, Chord gzip-compresses the request body only if compression reduces
the payload size; otherwise it sends the request uncompressed. Leave this unset
unless your provider or gateway benefits from compressed request bodies.

Provider/model HTTP requests identify the client with `User-Agent: chord/<version>` by default. Set provider-level `user_agent` only when a provider or gateway requires a specific value. This setting affects only normal model HTTP requests for that provider:

```yaml
providers:
  gateway:
    user_agent: RequiredGatewayClient/1.0
```

This setting affects only normal model HTTP requests for that provider. WebFetch uses its own `web_fetch.user_agent`; Codex OAuth, usage polling, and WebSocket requests keep Chord's protocol-specific User-Agent.

## Output token cap

Use `max_output_tokens` to set a global cap on requested output tokens. The effective request limit is still clamped by each model's `limit.output` and available total context (`limit.context` when known), so runtime uses the smallest applicable value.

`limit.input` is separate: use it only for models whose providers publish an extra input cap beyond the total context window. Lowering `max_output_tokens` can reduce cost and long-response failure risk, but it does **not** increase a provider's input allowance or replace `limit.input`.

```yaml
max_output_tokens: 32000
```

## Stream retry cap

Use `stream_retry_rounds` to put a hard ceiling on public LLM retry rounds.
Each round can still walk the current model pool and each provider key in the
normal order; this setting limits how many full rounds `CompleteStream` will
make before giving up.

A "round" here means the whole public retry pass, not a single provider/model
attempt. For example, `stream_retry_rounds: 2` allows at most two full passes
through the active routing chain. Once the cap is reached, Chord stops even for
retry classes that would normally wait and continue, such as all-keys-cooling
or concurrent-request 429 responses.

- `0` keeps the default behavior: retry until success, cancellation, or a terminal failure.
- Positive values stop after that many rounds, even for cooling / concurrent-request retry classes.
- This is mainly useful for automation or headless environments that prefer bounded latency over maximum persistence.

```yaml
stream_retry_rounds: 3
```

## Local TUI options

These options affect the local TUI. They can be set in the global config and
can also be overridden by project-level `.chord/config.yaml` when appropriate.

```yaml
desktop_notification: true
ime_switch_target: com.apple.keylayout.ABC
prevent_sleep: true
```

- `desktop_notification`: enables terminal notifications in local TUI mode,
  mainly when the terminal is unfocused. Chord auto-selects a notification
  escape sequence by terminal (OSC 9 or OSC 777) and sends notifications for
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

### Manual (on-demand) MCP servers

By default, configured MCP servers auto-start and become part of the default LLM tool context. For an MCP server you do not need in every conversation, set `manual: true`: it stays disabled at startup, Chord normally does not connect to it, and its tool descriptions are not added to the default context, reducing context overhead. Enable it manually only when you need it:

```yaml
mcp:
  exa:
    url: https://mcp.exa.ai/mcp
    manual: true
```

- When `manual: true`, the server starts in a disabled (gray) state and does not connect until you enable it.
- Only servers configured with `manual: true` can be changed at runtime with `/mcp`. Auto-start servers are read-only in the MCP selector and are not affected by `/mcp enable|disable`.
- Enable/disable at runtime with `/mcp` (menu in TUI) or with explicit commands:
  - `/mcp enable <server>`
  - `/mcp disable <server>`
  - `/mcp status`
- Runtime `/mcp enable|disable` changes are allowed while a turn is running. The current in-flight request keeps the tool surface it started with; the new MCP tools and the MCP system-prompt block are applied at the next LLM request (including automatic retry/recovery requests).
- In Codex loop mode, when a tracked Codex quota window has less than 10% remaining, Chord preserves the existing LLM-facing context surface instead of rewriting tool descriptions or system-prompt text. Runtime permissions and MCP tool execution state still change, but the model keeps seeing the previous tool list/prompt so a low-quota loop can continue on the same Codex session without a context-shape change.
  This is an intentional trade-off. Until quota recovers or a new session/context surface is built, the model's view can temporarily disagree with runtime state: newly enabled MCP tools may not be discoverable by the model; disabled MCP tools may still appear callable and then fail at execution; permission changes from `deny`/`ask` to `allow` may not be reflected in the prompt/tool descriptions; and changes from `allow` to `deny`/`ask` may be enforced only when the tool call is attempted. Chord accepts that short-term mismatch to avoid exhausting a Codex loop session by changing its context shape near the end of the quota window.

### Startup consistency

Auto-start MCP servers still connect asynchronously after the TUI starts, but **the first LLM request waits** until each auto-start server has either connected successfully or reached a terminal failure state. This avoids tool-surface inconsistency between the agent and the model.

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
permission:
  Write: ask
  Edit: ask
prompt: |
  You are an agent focused on backend development.
```

Common fields include:

- `name`: agent name. If omitted, Chord uses the filename without extension.
- `description`: short description shown to the main agent when delegation is available.
- `mode`: `main` for a MainAgent role, or `subagent` for a SubAgent. Empty and unknown values behave as `main`; `sub_agent` and `sub` are accepted as SubAgent aliases.
- `model_pools`: optional ordered list of pool names this agent can use. Pool definitions live in `config.yaml` top-level `model_pools`; when omitted, the agent can use all top-level pools sorted by name.
  Inline variants such as `openai/gpt-5.5@high` are specified in the pool definitions.
- `variant`: default variant when a model ref does not include `@variant`.
- `permission`: per-tool permission policy for this agent. Permissions live directly in agent config files; when the confirmation popup remembers a rule, `project` updates the current project's `.chord/agents/<role>.yaml`, and `global` updates the user config directory's `agents/<role>.yaml` (default: `~/.config/chord/agents/<role>.yaml`). Chord no longer writes a separate permissions directory. Some orchestration tools have special semantics (`Delegate` also gates delegated-work controls such as `Cancel`; `Handoff` and `Done` treat `allow` and `ask` as workflow-available states with Chord's own confirmation gates). See [Permissions & Safety](./permissions-and-safety.md#special-permission-semantics) before relying on fine-grained control-tool rules.
- `mcp`: MCP config scoped to this agent.
- `delegation`: limits such as `max_children`, `max_depth`, and `child_join`.
- `prompt` / `system_prompt`: system prompt for plain YAML files.

Example:

```yaml
name: builder
mode: main
model_pools: [default]
permission:
  "*": deny
  Read: allow
  Grep: allow
  Glob: allow
  WebFetch:
    "http://localhost:8000/*": ask
  Shell: allow
  Edit: ask
  Write: ask
```

## Context management: Compaction vs. Reduction

Chord provides two complementary context management layers:
**context compaction** rewrites the session history with an LLM-generated
summary, while **context reduction** trims stale tool output from each
individual request prompt. They operate at different levels and serve different
purposes.

### Quick comparison

| Aspect | Context compaction | Context reduction |
|--------|-----------|-----------|
| What it does | Calls an LLM to generate a structured summary and replaces old history | Applies deterministic rules to trim stale tool output from the current request |
| Writes to disk | ✅ Rewrites session files | ❌ Session files unchanged |
| Uses an LLM | ✅ (configurable model pool) | ❌ (heuristic rules only) |
| When it fires | Context exceeds threshold / manual `/compact` / error recovery | Before every LLM request |
| Typical latency | Seconds to tens of seconds (waits for LLM) | Milliseconds (in-memory rule matching) |
| User visibility | TUI shows "Compacting context..." progress | Silent (invisible) |
| Loop mode | Enabled; automatic and manual compaction can still run so long sessions can continue after the context budget is spent | Disabled for new messages; if `/loop on` is enabled while a request is in flight, Chord reuses that request's already-reduced prefix for cache stability. For Codex loop sessions with less than 10% quota remaining, Chord also keeps the existing LLM-facing tool descriptions and system prompt stable; permission/MCP execution state can still change. |

**How they work together**: Reduction is the lightweight first line of defense —
it trims stale tool output before every request, slowing down context growth.
When reduction alone is not enough and the context keeps growing past the
compaction threshold, compaction steps in for a deep compression pass. Most
users only need to care about compaction settings; reduction defaults are
already tuned for common usage patterns.

### Context compaction

When the main conversation approaches the model context limit, Chord
automatically triggers context compaction. The compaction process calls an LLM
to analyze the current conversation, generates a structured summary (covering
goals, progress, key decisions, file evidence, etc.), archives old messages,
and replaces the conversation history with the summary. The compacted session
is persisted to disk.

**Minimal config** (enable automatic compaction):

```yaml
context:
  compaction:
    threshold: 0.8
    model_pool: compact
```

**Configuration fields**:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `threshold` | float | `0.8` | Context usage ratio that triggers automatic compaction. Range `0`–`1`, e.g. `0.8` means trigger when usage reaches 80% of the usable input budget. Set to `0` to disable automatic compaction. |
| `model_pool` | string | clone current agent pool | Name of a dedicated model pool for compaction. Use a low-cost/fast model to minimize overhead. |
| `reserved` | int | `0` | Token headroom reserved for tokenizer drift, tool schema overhead, and compaction/recovery safety margin. Subtracted from the input budget before applying `threshold`. |
| `preset` | string | auto-detected | Force a specific compaction implementation. Usually unnecessary. |
| `profile` | string | `auto` | Compaction strategy. Usually unnecessary. |

**How the threshold is calculated**: Chord uses the **usable input budget** as
the baseline. If the model config sets `limit.input`, that value is used;
otherwise Chord derives it from `limit.context - effective requested output`
(where effective output is `max_output_tokens` capped by the model's
`limit.output`). If `reserved` is set, it is subtracted first. The TUI
`Context` indicator in the info panel and footer uses the same input-budget
baseline, so its percentage matches automatic compaction thresholds. For
providers that report prompt-cache writes separately, Chord counts the current
prompt-side usage as `input_tokens + cache_write_tokens` so newly cached prompt
segments are included in the displayed context burden.

**Reserved headroom example**:

```yaml
context:
  compaction:
    threshold: 0.8
    reserved: 16000
```

With a model configured as `input: 272000`, the usable budget after reserving
is `256000`, and automatic compaction triggers when context reaches
`256000 × 0.8 = 204800` tokens. A sensible `reserved` value prevents compaction
from triggering too late due to tokenizer drift or tool-description overhead.

Beyond automatic triggering, you can manually compact at any time with the
`/compact` command in the TUI. Manual compaction uses the same background
worker as automatic compaction: it can be started while the agent is already
working, shows progress in the background compaction status slot, and applies
at the next safe continuation/idle barrier rather than interrupting the active
turn immediately. You can also use `/compact --no` to temporarily disable
subsequent automatic compaction for the current session.

When a provider publishes both a total context window and a separate input cap,
use all three fields when you know them:

```yaml
providers:
  openai:
    models:
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
```

This matters because reducing `output` does not increase a provider's hard
input allowance. Keeping automatic compaction enabled is recommended when your
selected models have smaller input budgets or split input/output limits.

### Context reduction

Before each LLM request, Chord applies a set of deterministic rules to inspect
tool results in the conversation and trim large, stale output. **This only
affects the prompt sent for the current request — it never rewrites session
files on disk.**

In loop mode, reduction is not applied to newly added messages. If you enable
`/loop on` while an LLM request is already in flight, Chord freezes and reuses
that request's already-prepared prefix for subsequent loop requests. This avoids
flipping old history from a reduced form back to full raw tool output, preserving
prompt-cache prefix stability; messages produced during the loop remain
unreduced until loop mode is turned off.

> **Most users do not need to configure this section.** The built-in defaults
> are conservative and work well for common scenarios. In empirical local-session
> analysis, reduction produced meaningful savings without systematically
> breaking prompt-cache reuse; if you want a cache-friendlier balance, use the
> recommended profile below.

**Recommended default profile** (keeps recent medium-sized reads/logs intact while
still trimming large stale output):

```yaml
context:
  reduction:
    confirm_age_turns: 2
    error_age_turns: 3
    shell_success_age_turns: 2
    shell_success_bytes: 8000
    read_like_age_turns: 1
    read_like_output_bytes: 4000
    stale_age_turns: 4
    stale_output_bytes: 1500
    min_tool_results_prune: 8
```

Use the defaults when prompt-cache stability matters and your sessions commonly
reuse the same active files across several turns. If your main problem is
hitting context limits quickly in tool-heavy sessions, you can lower the byte
thresholds, for example `shell_success_bytes: 4000` and
`read_like_output_bytes: 2500`.

**Reduction categories**: Tool results are classified into five categories,
each with its own age and size thresholds.

| Category | Typical examples | Age threshold | Size threshold | Rationale |
|----------|-----------------|---------------|----------------|-----------|
| Confirm / permission | Tool permission confirmations, user authorizations | `confirm_age_turns` (default 2) | — | Permission decisions become stale quickly |
| Errors | Failed tool results | `error_age_turns` (default 3) | — | Failure reasons may still be relevant, kept a bit longer |
| Shell success | `git`, `go test`, `npm run` output | `shell_success_age_turns` (default 2) | `shell_success_bytes` (default 8000) | Build/test output can be important context but is usually reproducible |
| Read / search | `Read`, `Grep`, `Glob` output | `read_like_age_turns` (default 1) | `read_like_output_bytes` (default 4000) | File contents can always be re-read; most aggressive trimming |
| Other stale results | Tool output not covered above | `stale_age_turns` (default 4) | `stale_output_bytes` (default 1500) | Catch-all fallback; most conservative to avoid losing hard-to-reconstruct data |

How to read the age and size parameters:

- `*_age_turns` counts the number of **user turns** (times you send a message)
  after the tool result. For example, `read_like_age_turns: 1` means a large
  read result may be trimmed starting with your next message.
- `*_bytes` is the **minimum output size in bytes** for that category to be
  eligible for trimming. Smaller outputs stay intact — short output doesn't
  need reduction.
- `min_tool_results_prune` (default 8) is a **safety gate**: Chord won't start
  trimming until the conversation has at least this many tool-result messages,
  preventing small conversations from being touched prematurely.

**Tuning guidance**:

| If you see this... | Try this... |
|--------------------|-------------|
| Prompt-cache reuse is good but medium reads/logs still change the request prefix too often | Raise `read_like_output_bytes` and `shell_success_bytes` further |
| Short conversations with many tool results hitting limits | Lower `min_tool_results_prune` (e.g. `4`) |
| Permission confirmations dominating the prompt | Lower `confirm_age_turns` (e.g. `1`) |
| Build/test logs are important context to keep | Raise `shell_success_bytes` further (e.g. `16000`) |
| File contents often need to be revisited | Raise `read_like_age_turns` (e.g. `3`) and `read_like_output_bytes` (e.g. `8000`) |
| All tool output is important, nothing should be dropped | Raise all `*_age_turns` and `*_bytes` globally |

Full configuration example (showing all defaults):

```yaml
context:
  reduction:
    confirm_age_turns: 2
    error_age_turns: 3
    shell_success_age_turns: 2
    shell_success_bytes: 8000
    read_like_age_turns: 1
    read_like_output_bytes: 4000
    stale_age_turns: 4
    stale_output_bytes: 1500
    min_tool_results_prune: 8
```

Unset or non-positive threshold fields use the defaults above. Project-level
`.chord/config.yaml` can override global config field by field.

## Provider/model diagnostics

```bash
# smoke-test all providers with representative models
chord doctor models

# test one provider's representative model
chord doctor models --provider openai

# test an exact model or variant
chord doctor models --model openai/gpt-5.5@high
chord doctor models --provider openai --model gpt-5.5@high

# audit each entry in a model pool independently
chord doctor models --pool thinking
```

Use this command as an auth, endpoint, transport, model, and variant tuning smoke test. It uses the same merged global + project config view as normal runtime startup, so project-level provider/proxy/model overrides are included. Pool diagnostics request each pool entry independently rather than following the normal fallback chain.

## Configuration cheatsheet

The full top-level keys of `config.yaml` (both global `~/.config/chord/config.yaml` and project-level `.chord/config.yaml`). All keys are optional unless noted.

| Key                     | Type                  | Default                          | Scope                    | Summary                                                                                                                  |
| ----------------------- | --------------------- | -------------------------------- | ------------------------ | ------------------------------------------------------------------------------------------------------------------------ |
| `providers`             | `map[name]Provider`   | —                                | global / project         | Per-provider config (`type`, `api_url`, `preset`, `key_rotation`, `key_order`, `models`, `compress`). See [Minimal provider config](#minimal-provider-config). |
| `model_pools`           | `map[name][]ref`      | —                                | global / project         | Reusable named pools of full `provider/model[@variant]` refs. See [Model pools](#model-pools-selecting-providermodel). |
| `thinking_translation`  | object                | disabled (`max_chars: 1000`)     | global / project         | Optional appended translation preview for thinking / reasoning cards. Requires `target_language` and `model_pool`; failures only skip the affected thinking block. |
| `context`               | object                | see below                        | global / project         | `compaction` and `reduction` settings. See [Context compaction](#context-compaction) and [Context reduction](#context-reduction). |
| `diagnostics`           | object                | enabled (Python LSP + Ruff fallback) | global / project     | Post-tool diagnostics appended to `Edit` / `Write` results. `diagnostics.python.semantic_backend` is the primary LSP server (default `pyright`); `diagnostics.python.quick_backend` is a one-shot fallback (default `ruff check`). `diagnostics.python.large_file.{line_threshold, byte_threshold, strategy}` controls when large files use the quick backend, and `run_semantic_when_quick_unavailable: true` forces semantic diagnostics when the quick backend is missing. `diagnostics.python.output.{max_near_diagnostics, max_outside_diagnostics, max_total_diagnostics, near_range_before_lines, near_range_after_lines}` shapes the appended diagnostics text. Diagnostics are shown by severity priority (errors/warnings first, then info/hints if slots remain). Set `diagnostics.enabled: false` to skip the whole pipeline. |
| `skills`                | object                | empty                            | global / project         | `paths: [...]` — additional skill directories beyond the defaults.                                                       |
| `confirm_timeout`       | int (seconds)         | `0` (no timeout)                 | global / project         | Timeout for confirmation dialogs in TUI; `0` means wait forever.                                                         |
| `diff`                  | object                | `{inline_max_columns: 200}`      | global / project         | TUI diff rendering. `inline_max_columns` caps one-line inline diff width.                                                |
| `desktop_notification`  | bool                  | `false`                          | global / project         | Enable local-TUI terminal notifications when the terminal is unfocused; Chord auto-selects OSC 9 or OSC 777 by terminal. (Unsupported terminals ignore the sequence.) |
| `prevent_sleep`         | bool                  | `false`                          | global / project         | Prevent macOS idle sleep while any agent is active. macOS-only; no-op elsewhere.                                         |
| `keymap`                | `map[action][]key`    | see [Keybindings](./keybindings.md#action-name-reference) | global / project | Override key bindings. Action names use lower snake_case.                                                                |
| `commands`              | `map[/cmd]text`       | empty                            | global / project         | Custom slash commands; `"/cmd"` → text inserted as a user message. See [Customization — Custom slash commands](./customization.md#custom-slash-commands). |
| `ime_switch_target`     | string                | empty                            | global / project         | IM identifier passed to `im-select` / `im-select.exe` when entering Normal mode. Linux/macOS/Windows.                    |
| `log_level`             | string                | `info`                           | global / project         | `debug` / `info` / `warn` / `error`. `debug` is verbose.                                                                |
| `paths`                 | object                | XDG defaults                     | global only              | `state_dir`, `cache_dir`, `sessions_dir`, `logs_dir`. CLI flags and `CHORD_*` env vars override.                         |
| `maintenance`           | object                | disabled                         | global only              | `size_check_on_startup`, `warn_state_bytes`, `warn_cache_bytes`.                            |
| `lsp`                   | `map[name]Server`     | empty                            | global / project         | Per-language-server config. See [Customization — LSP](./customization.md#lsp).                                          |
| `mcp`                   | `map[name]MCP`        | empty                            | global / project / agent | Per-MCP-server config. See [MCP](#mcp).                                                                                  |
| `hooks`                 | object                | empty                            | global / project / agent | Hooks per trigger point. See [Hooks](./hooks.md).                                                                        |
| `max_output_tokens`     | int                   | model-default                    | global / project         | Global cap on requested output tokens. Effective limit is also clamped by each model's `limit.output`; reasoning requests also respect it. |
| `stream_retry_rounds`   | int                   | `0` (retry until success/cancel) | global / project         | Hard cap on public LLM full-round retries. `0` keeps retrying until success, cancellation, or terminal failure. |
| `proxy`                 | string                | empty (use env / direct)         | global / project         | Global proxy URL. Per-tool override via `web_fetch.proxy`.                                                              |
| `web_fetch`             | object                | empty                            | global / project         | `user_agent`, `proxy` (inherits global if nil; empty string = direct). See [WebFetch](#webfetch).                       |
| `worktree`              | object                | empty                            | global / project         | Defaults for `chord --worktree` and `chord worktree …` subcommands.                                                     |

### Provider field reference

Chord automatically propagates the current Chord session id to OpenAI-family
providers as cache/routing affinity metadata: OpenAI Responses requests include
`prompt_cache_key`, and OpenAI Chat Completions / Responses HTTP requests include
`X-Session-Id` and `session-id` headers when a session id is available. These
fields are not user-configurable; they follow the active Chord session and are
cleared or changed on session switch/resume. Anthropic prompt caching is driven
by `cache_control` blocks, and its optional `metadata.user_id` remains a stable
anonymous user/provider id rather than a per-session id. For Anthropic models,
you can request an hourly cache TTL with `prompt_cache.ttl: 1h`:

```yaml
providers:
  anthropic:
    models:
      claude-sonnet-4-5:
        prompt_cache:
          mode: auto
          ttl: 1h
```

Gemini does not have a simple per-request session-id cache key in Chord's
`generateContent` transport; its cache signals come from provider-specific
cached-content APIs/usage fields, not from a Chord session id header.

| Field          | Type   | Description                                                                                                                                              |
| -------------- | ------ | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `type`         | string | `messages` / `chat-completions` / `responses` / `generate-content`. Auto-detected from `api_url` or `preset` when omitted.                              |
| `api_url`      | string | Endpoint URL. For Gemini, the `/models` base path; Chord appends `/{model}:streamGenerateContent?alt=sse`.                                              |
| `preset`       | string | Currently `codex` (OpenAI Codex / ChatGPT OAuth).                                                                                                        |
| `official_api` | bool   | Treat this endpoint as an official provider API where HTTP 400 usually means an invalid request and should not be retried as a transient gateway error. `preset: codex` is official by default; omit or set `false` for aggregating/proxy gateways. |
| `key_rotation` | string | `on_failure` (default) / `per_request`. Controls when a credential / API key is reselected.                                                            |
| `key_order`    | string | `sequential` (non-Codex default) / `random` / `smart` (Codex only). Controls how Chord chooses among selectable keys.                                   |
| `compress`     | bool   | gzip request bodies when compression saves bytes. Off by default.                                                                                       |
| `supported_service_tiers` | list | Provider-level default accepted non-standard tiers for its models, e.g. `[fast, slow]` or `[fast]`. Model entries can override it. |
| `models`       | map    | Map of model id → [model config](#model-field-reference).                                                                                               |

### Model field reference

| Field             | Type   | Description                                                                                                            |
| ----------------- | ------ | ---------------------------------------------------------------------------------------------------------------------- |
| `limit.context`   | int    | Total request window in tokens when known. If `limit.input` is omitted, Chord derives the input budget from this minus effective requested output. |
| `limit.input`     | int    | Separate input cap when a provider publishes one. Chord uses it to compact or retry before the prompt is too large.               |
| `limit.output`    | int    | Maximum output tokens; runtime is also clamped by `max_output_tokens`.                                                             |
| `context.compaction.reserved` | int | Optional input-budget headroom reserved before `compaction.threshold` is applied. Useful for tokenizer drift, tool overhead, and safer overflow recovery. |
| `reasoning`       | object | OpenAI reasoning options. Chord-supported subfields are `reasoning.effort` (`low` / `medium` / `high` / `xhigh`; unset = omit and use provider/model default) and, for Responses, `reasoning.summary` (`auto` / `concise` / `detailed`; unset = omit / no explicit summary request). Recommended summary value when you want readable summaries: `auto`. |
| `text.verbosity`  | string | Optional OpenAI text verbosity hint where supported; leave unset to use the provider/model default unless you intentionally want `low` / `medium` / `high`. |
| `thinking`        | object | Anthropic extended-thinking options. `type: adaptive` lets Chord derive a budget from `effort`; `display: summarized` enables summarized thinking blocks (valid only with `type: enabled` or `adaptive`). |
| `variants`        | map    | Named parameter presets. Reference with `provider/model@variant`.                                                      |
| `modalities.input`| array  | Subset of `text` / `image` / `pdf`. Defaults to `[text, image]`.                                                       |
| `supported_service_tiers` | list | Provider-level default or model-level override for accepted non-standard tiers, e.g. `[fast, slow]` or `[fast]`. Omit to use preset defaults. |

Service tiers and prompt caching are provider-specific. OpenAI supports priority/flex-style tiering plus `prompt_cache_key` / `prompt_cache_retention`; Anthropic supports `cache_control` with 5m and 1h TTLs and service-tier controls; Gemini uses its own routing / thinking / cached-content mechanisms when available. Chord maps the user-facing tier to the closest supported provider behavior instead of forcing one wire format across all backends.

OpenAI reasoning items can also be returned as `reasoning.encrypted_content` when you need stateless continuation. Treat that field as opaque continuation data: it is not meant to be rendered directly in the UI. When a readable summary is available, that is the user-facing form to show.

## Related

- [Quickstart](./quickstart.md)
- [CLI](./cli.md)
- [Keybindings](./keybindings.md)
- [Paths](./paths.md)
- [Environment variables](./environment.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Customization](./customization.md)
- [Hooks](./hooks.md)
- [Troubleshooting](./troubleshooting.md)
