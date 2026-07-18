# Configuration & Auth

Chord separates behavior configuration and credentials:

- `~/.config/chord/config.yaml`: providers, models, extensions, defaults
- `~/.config/chord/auth.yaml`: API keys / OAuth credentials
- `.chord/config.yaml`: project-level overrides
- `~/.config/chord/agents/` and `.chord/agents/`: agent role definitions

## How to use this page

You do not need to read this page from top to bottom:

- **First setup:** start with [Quickstart](./quickstart.md), then copy a provider from [Model configuration recipes](./model-configs.md).
- **Credentials and OAuth:** jump to [`auth.yaml`](#authyaml) or [OAuth](#oauth).
- **Routing and reliability:** use [Model pools](#model-pools-selecting-providermodel), [Provider timeouts](#provider-timeouts), and [Stream retry cap](#stream-retry-cap).
- **Long sessions:** use [Context management](./context-management.md).
- **Exact field names:** use the [Configuration cheatsheet](#configuration-cheatsheet).

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
- `mcp` merges by server name, with each same-name project server replacing the entire global server definition rather than inheriting individual connection or permission fields;
- append-style extension points keep global entries and add project entries: currently `skills.paths` and per-trigger hook arrays under `hooks.*` append rather than replace.

On the first run, if you launch `chord` in an interactive terminal and `config.yaml` is missing, Chord starts a one-time setup wizard. It writes a minimal `config.yaml` and, when needed, `auth.yaml`, reuses matching existing credentials from `auth.yaml` when possible, then prints the resolved file locations. Redirected stdin does not by itself make startup non-interactive; if Chord can still open the controlling TTY, the wizard uses that TTY. If no controlling TTY is available, it exits instead of waiting for input.

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

### BigModel Chat Completions (Coding Plan)

Chord's `api_url` is the complete request URL. For the BigModel Coding Plan
OpenAI-compatible endpoint, append `/chat/completions` to the Coding Plan base.

```yaml
providers:
  bigmodel:
    type: chat-completions
    api_url: https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
    models:
      glm-5.2:
        limit:
          context: 1000000
          output: 128000
        reasoning:
          effort: max
        compat:
          request_overrides:
            rename_body_fields:
              max_completion_tokens: max_tokens
            body:
              thinking:
                type: enabled
                clear_thinking: false
          reasoning_continuity:
            mode: openai_visible
```

### OpenAI Responses

For provider/model-specific copy-paste snippets (GPT-5.4/5.5/5.6, Claude,
Gemini, GLM, DeepSeek/OpenAI-compatible), see [Model configuration recipes](./model-configs.md).

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6:
        limit:
          context: 500000
          input: 372000
          output: 128000
        reasoning:
          effort: medium
          summary: auto
        variants:
          low:
            reasoning:
              effort: low
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
          max:
            reasoning:
              effort: max
        modalities:
          input: [text, image]

model_pools:
  default:
    - openai/gpt-5.6@high
```

Pair this provider with an API key in `~/.config/chord/auth.yaml`:

```yaml
openai:
  - "$OPENAI_API_KEY"
```

- Replace both `gpt-5.6` occurrences with `gpt-5.6-sol`, `gpt-5.6-terra`, or `gpt-5.6-luna` to pin an explicit model ID.
- Supported API reasoning efforts are `none`, `low`, `medium`, `high`, `xhigh`, and `max`; select a configured variant with a ref such as `openai/gpt-5.6@max`.
- `reasoning.summary: auto` is optional. Chord does not currently expose GPT-5.6 `reasoning.mode: pro`.
- `preset: codex` providers can also use `max` when the selected model/backend supports it. Whether a given effort level is accepted is model/provider-specific.

Limits and reasoning values were checked against the current Codex model
catalog and OpenAI's [GPT-5.6 model guidance](https://developers.openai.com/api/docs/guides/latest-model).

Read model limits in this order:

1. `limit.context` is the total window. For most models, input + requested output just needs to fit inside this number.
2. `limit.input` is only needed when the provider also lists a separate input cap. Some GPT models work this way; if you omit it, Chord derives the usable input budget from `limit.context` after reserving effective requested output.
3. `limit.output` is the model's own output capacity. Chord's default requested output cap (`max_output_tokens`) is still `32000`, so real requests use the smaller output limit unless you raise it.

The current GPT-5.6 Codex allocation is a 500K total window with a 372K input
cap and a 128K output cap, so configure all three fields explicitly.

`parallel_tool_calls` defaults to `true` for Responses and Chat Completions providers. Set it to `false` on a provider, model, or variant only when the backend or workflow requires serial tool calls. Provider-level `user_agent` is also available for gateways that require a specific client identifier.

Provider auth headers are inferred separately from `type`, but can be overridden with `auth_scheme` when a compatible endpoint expects a different credential header:

- `type: messages` → default `auth_scheme: anthropic-api-key` (`x-api-key`)
- `type: responses` → default `auth_scheme: bearer` (`Authorization: Bearer`)
- `type: chat-completions` → default `auth_scheme: bearer`
- `preset: azure` → default `auth_scheme: api-key` (`api-key`)

Supported `auth_scheme` values are:

- `anthropic-api-key`
- `bearer`
- `api-key`

Use an explicit override only when the endpoint's auth requirements differ from Chord's transport default. For example, a provider may expose an Anthropic-compatible `/messages` path but require `Authorization: Bearer` instead of `x-api-key`. In that case, keep the same `type` and set only `auth_scheme: bearer`.

For Anthropic's gated 1M context beta, Chord opts in only when the model declares a window of at least 1M tokens (`limit.input` when set, otherwise `limit.context`). Models with smaller declared windows do not receive the beta header. The provider may apply different access requirements and pricing above 200K tokens.

`store` controls whether a Responses backend retains requests and responses server-side. It defaults to `false`, except for `preset: azure`. Enable it only when the backend explicitly requires server-side retention and you accept the data-retention trade-off. Do not enable it for `preset: codex`; the official Codex OAuth endpoint rejects `store: true`.

### OpenAI Codex preset

Codex OAuth uses the same model limit blocks shown in the Responses-compatible
examples. Only the provider preset and authentication method change.

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
      gpt-5.4:
        limit:
          context: 1050000
          input: 950000
          output: 128000
      gpt-5.6-sol:
        limit:
          context: 500000
          input: 372000
          output: 128000
```

GPT-5.4 uses `1050000 / 950000 / 128000`. GPT-5.5 uses `400000 / 272000 / 128000`;
GPT-5.6 Sol, Terra, and Luna use `500000 / 372000 / 128000` (`context / input /
output`). See [Model configuration recipes](./model-configs.md#codex-oauth-preset)
for complete examples.

`preset: codex` can use OpenAI / ChatGPT OAuth credentials from `auth.yaml`. OAuth entries are mappings:

```yaml
codex:
  - refresh: rfr_...
    access: eyJ...
    expires: 1774009702606
    account_id: acc_...              # optional; only workspace/account tokens always have this
    account_user_id: u_...__acc_...  # optional; parsed/backfilled in the background when missing
    email: user@example.com          # optional
```

`account_id` is not present for every ChatGPT account. Personal Plus/Pro access tokens may carry only `user_id` and no `chatgpt_account_id`; Chord still uses those credentials as ordinary OAuth bearer tokens and omits the `ChatGPT-Account-ID` header. Features that require a workspace/account id, such as Codex usage / rate-limit polling, skip those credentials until an account id is provided or parsed later.

For large account pools, Chord does not synchronously parse every OAuth JWT at startup and does not block provider initialization when one access token lacks `account_id`. Startup reads only metadata already present in `auth.yaml`; missing `account_user_id`, `account_id`, `email`, and `expires` are parsed and backfilled in the background after the provider is available. When manually converting Codex / sub2api / other login exports, keep any available `account_id`, `account_user_id`, and `email`, but they are not startup requirements.

### Azure OpenAI Responses preset

Use `preset: azure` for Azure OpenAI Responses endpoints. The preset is explicit: Chord does not auto-detect Azure from the endpoint URL. It sets `type: responses`, defaults `store: true`, treats the endpoint as an official API for 400 handling, disables Codex WebSocket/OAuth behavior, sends the configured credential as the Azure `api-key` header instead of `Authorization: Bearer`, and omits Codex compatibility headers such as `OpenAI-Beta` and `originator`.

Azure's v1 Responses endpoint can use `/openai/v1/responses` directly; add `api-version` only when you need to pin or opt into a specific version such as `preview`:

```yaml
providers:
  azure:
    preset: azure
    api_url: https://YOUR-RESOURCE.openai.azure.com/openai/v1/responses
    models:
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
```

Store the Azure API key under the same provider name in `auth.yaml`:

```yaml
azure:
  - $AZURE_OPENAI_API_KEY
```

### Google Gemini

```yaml
providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-3.5-flash:
        limit:
          context: 1048576
          output: 65536
        modalities:
          input: [text, image, pdf]
```

For Gemini, set `api_url` to the `/models` base path. Chord detects `type: generate-content` from the URL path's `/models` suffix, so `type` can be omitted. Do not include the model name or `:streamGenerateContent?alt=sse`; Chord appends `/{model}:streamGenerateContent?alt=sse` automatically. The model map key, such as `gemini-3.5-flash`, is the model ID sent to Gemini.

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
        modalities:
          input: [text, image, pdf]
        thinking:
          budget: -1
          include_thoughts: true
      gemini-3-pro:
        limit:
          context: 1048576
          output: 65536
        modalities:
          input: [text, image, pdf]
        thinking:
          budget: -1
          level: high
```

If `type` is omitted, Chord auto-detects it from provider config:

- `preset: codex` → `responses`
- `preset: azure` → `responses`
- `api_url` path ending in `/responses` → `responses`
- `api_url` path ending in `/chat/completions` → `chat-completions`
- `api_url` path ending in `/messages` → `messages`
- `api_url` path ending in `/models` → `generate-content`

If none of these rules match, set `type` explicitly.

## Appended thinking translation

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

- Only **thinking / reasoning** output is translated — never the assistant final answer. The translation is appended under the corresponding thinking card with a neutral `Translated · <target_language>` header, rendered through the same Markdown / code-highlighting pipeline, and never written back into model context.
- `target_language` and `model_pool` are both required; if either is missing, the feature is disabled. `model_pool` must point to a top-level `model_pools` entry — prefer a separate low-cost translation pool. The pool can contain multiple `provider/model[@variant]` refs; translation runs a **single fallback round** across them in order, moving to the next candidate on failure (including network/5xx/timeout) or when a result is empty, clearly truncated, or in the wrong language.
- `max_chars` (default `1000`) limits the thinking preview sent for translation; only the leading `max_chars` runes are translated, and text past that prefix will not appear in the translated card. Set a smaller value such as `500` for lower latency/cost, or a larger one for more complete translations.
- A temporary failure only skips that one thinking block; it does not block later thinking translations or the main response. Per-provider transport timeouts (one-minute-class by default) still apply, so a stalled model or key can fail over while the rest of the pool gets a chance to run.
- Translations are persisted in the session directory (`thinking_translations.json`) and restored when the session is resumed. A given thinking block is translated at most once: changing `thinking_translation.target_language` later does not re-translate already-stored blocks.

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

For `preset: codex` OAuth providers, Chord now keeps frequently changing runtime status (quota snapshots, reset times, last warm-up timestamps, shared OAuth status cache) in `auth.state.json`, not in `auth.yaml`.

That split is intentional:

- `auth.yaml` remains the user-edited source of truth for credentials and stable OAuth fields such as `refresh`, `access`, `expires`, `account_id`, and `email`; empty OAuth fields are omitted when Chord rewrites the file, and OAuth `status` does not belong in `auth.yaml`;
- `auth.state.json` is machine-managed shared runtime state. Normal entries are keyed directly by `account_user_id` below each provider so quota / reset updates and account states such as `expired`, `deactivated`, and `invalidated` do not constantly rewrite `auth.yaml` while the user may also be editing it. Refresh-only credentials whose account is not known yet can temporarily use a `refresh_sha256:<digest>` state entry until the first successful refresh backfills `account_user_id`. State entries without a matching `auth.yaml` OAuth credential, and unrecognized legacy state-key formats, are removed by `chord auth state clean`.

For OAuth credentials with `access`, the access token must carry parseable account and user/account-user claims. If `auth.yaml` already has `account_id`, the token's account ID must match; otherwise the access token is rejected as a mismatched credential. Chord can also keep a refresh-only OAuth entry (`refresh` without `access`) and refresh it on first use; after a successful refresh, Chord extracts `account_id` and switches runtime state to the `account_user_id` key. If refresh fails unrecoverably before the account is known, Chord records the invalid state under `refresh_sha256:<digest>` so `chord auth state clean` can remove the unusable credential later. An OAuth entry with neither `access` nor `refresh` is unusable.

`expires` is the access-token expiry timestamp in Unix milliseconds. When `access` contains a JWT `exp` claim, Chord uses that value as the most accurate expiry metadata and can cache the resulting expiry in `auth.state.json` without storing the access token there. A missing or locally expired `expires` value does not by itself mark an OAuth slot `expired` or unhealthy. Chord still tries the existing access token first, and only after an authentication failure will it refresh the credential or mark it expired if recovery is impossible.

Typical `auth.state.json` content looks like:

```json
{
  "openai": {
    "user-1__acc-1": {
      "account_user_id": "user-1__acc-1",
      "account_id": "acc-1",
      "email": "user@example.com",
      "expires": 1774009702606,
      "status": "expired",
      "updated_at": 1774009702606,
      "last_warmup_at": 1774009702606,
      "codex_primary_used_pct": 12.5,
      "codex_primary_window_minutes": 60,
      "codex_primary_reset_at": 1774013302000,
      "codex_secondary_used_pct": 40,
      "codex_secondary_window_minutes": 10080,
      "codex_secondary_reset_at": 1774600000000
    }
  }
}
```

The `status` field is authoritative only in `auth.state.json`. Chord writes `expired` when an access token can no longer be used and the credential cannot be refreshed (including missing, invalid, expired, or reused refresh tokens), `deactivated` when the service reports a disabled/banned account, and `invalidated` when the account must be re-authenticated. Any non-empty status makes that OAuth slot unselectable until it is cleaned up or replaced.

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

For Codex providers, prefer configuring only `preset: codex` plus model settings. Do not manually override preset-managed fields such as `api_url`, `token_url`, `client_id`, `type`, `store`, `responses_websocket`, or `supported_service_tiers` unless you are deliberately testing transport internals. The preset selects the official OAuth transport, Responses endpoint, WebSocket/cache defaults, quota polling, smart key ordering, and service-tier capability. It does not define a separate HTTP request body or force a Codex User-Agent: non-Codex `type: responses` providers use the same Responses wire shape described above, and all providers default to `User-Agent: chord/<version>`. Use `supported_service_tiers` when you need an explicit tier matrix.

Codex OAuth account selection is controlled by `key_rotation` / `key_order` in [Provider key selection](#provider-key-selection). Codex defaults to `key_order: smart`, which considers quota snapshots, soft cooldown, and reset timing when choosing an account.

`smart` ranks selectable Codex OAuth accounts by preferring:

- accounts whose cached snapshot has no tracked window at `100%` used; a `100%` window is tried last but is not a hard block by itself;
- accounts with remaining quota in the shorter primary window (for example the 5h window), choosing the nearer primary reset first so soon-expiring quota is used before it is wasted;
- then accounts with remaining quota in the longer secondary window (for example the 1w window), again preferring the nearer secondary reset;
- then higher remaining headroom when the comparable windows reset at the same time;
- still falling back to unknown / stale candidates when no better option exists.

When a Codex client becomes active, Chord may also background-probe additional OAuth slots to refresh cached headroom snapshots. That warm-up is best-effort, low-concurrency, cancels when the active client is replaced, and only refreshes cached quota state; authentication failures from usage probes do not mark OAuth credentials unusable.

Warm-up priority is also state-aware:

- OAuth slots that have never been warmed up in shared state are probed first;
- older cached entries are refreshed before recently refreshed ones;
- after warm-up or polling returns a newer snapshot, Chord writes it to `auth.state.json` and other processes adopt it lazily when they next read key-selection or rate-limit state.

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
    - anthropic/claude-opus-4.8
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

## Reusing protocol templates with YAML anchors

Chord has no `model_templates` schema field. You can still use YAML anchors and
merge keys under that top-level container; Chord ignores the container itself
and reads the expanded model entries under `providers`.

Keep this page focused on protocol semantics. For current model limits, pricing,
and complete GPT / Claude / Gemini / GLM / DeepSeek snippets, use
[Model configuration recipes](./model-configs.md).

```yaml
model_templates:
  chat-thinking: &chat-thinking
    limit:
      context: 200000
      output: 64000
    reasoning:
      effort: high
    compat:
      # DeepSeek and GLM Chat APIs document max_tokens, not the OpenAI
      # reasoning-model default max_completion_tokens.
      request_overrides:
        rename_body_fields:
          max_completion_tokens: max_tokens
        body:
          thinking:
            type: enabled
      # Replays reasoning_content without injecting request fields.
      reasoning_continuity:
        mode: openai_visible

  responses-thinking: &responses-thinking
    limit:
      context: 200000
      output: 64000
    reasoning:
      effort: high
      summary: auto

  messages-thinking: &messages-thinking
    limit:
      context: 200000
      output: 64000
    thinking:
      type: adaptive
      effort: high
    compat:
      # Compatible endpoints should not receive Anthropic beta headers unless
      # their own documentation opts into them.
      request_overrides:
        headers:
          anthropic-beta: null

providers:
  chat:
    type: chat-completions
    api_url: https://example.com/v1/chat/completions
    models:
      chat-model: *chat-thinking

  responses:
    type: responses
    api_url: https://example.com/v1/responses
    models:
      responses-model: *responses-thinking

  messages:
    type: messages
    api_url: https://example.com/v1/messages
    models:
      messages-model: *messages-thinking
```

Model field semantics:

- `limit.context`: total request window when the provider publishes one.
- `limit.input`: independent input cap when published. If omitted, Chord derives
  the prompt budget from `limit.context` minus the effective requested output.
- `limit.output`: model output capacity. Runtime requests are also capped by the
  global `max_output_tokens` setting and remaining total-context space.
- `reasoning.effort`: reasoning depth/budget. Chord normalizes whitespace and
  casing, then forwards the value supported by the target provider.
  - Chat Completions sends top-level `reasoning_effort`.
  - Responses sends `reasoning.effort` and optional `reasoning.summary`.
- `reasoning.summary`: optional Responses reasoning summary request. Supported
  Chord values are `auto`, `concise`, and `detailed`; omit it to let the provider
  decide whether to return a summary.
- `thinking`: Messages-compatible extended thinking. `type: adaptive` combines
  with `thinking.effort`, which Chord sends as `output_config.effort`.
- `text.verbosity`: optional OpenAI-compatible visible-text verbosity hint.
- `variants`: named model parameter overrides selected with refs such as
  `provider/model@high`.
- `cost`: optional USD-per-million-token estimates. It can include input/output,
  cache prices, service-tier multipliers, and long-context input tiers.
- `modalities.input`: supported input kinds: `text`, `image`, and `pdf`.
- `supported_service_tiers`: accepted non-standard tiers such as `fast` or
  `slow`; price multipliers are configured separately under `cost`.

Compatibility fields:

- `compat.request_overrides.body`: recursively merges arbitrary JSON into the
  final protocol request. A `null` value deletes that field.
- `compat.request_overrides.rename_body_fields`: renames a final request field
  while preserving Chord's dynamically computed value. Use this for differences
  such as `max_completion_tokens: max_tokens`.
- `compat.request_overrides.headers`: sets arbitrary request headers. A `null`
  value removes a Chord default header, for example `anthropic-beta: null` on a
  compatible Messages endpoint.
- `compat.reasoning_continuity.mode`:
  - `none`: no provider-specific visible reasoning replay.
  - `openai_visible`: replays unchanged assistant `reasoning_content` during
    Chat Completions tool loops. It does not inject request fields; configure
    those with `request_overrides.body`.
  - Responses and Messages use their protocol-native continuity mechanisms; do
    not use visible-reasoning replay for those transports.
- `compat.thinking_toolcall`: enables a provider-specific parser for gateways
  that encode tool calls inside visible reasoning text. Leave disabled unless
  the gateway requires that format.

Provider-level `compat` values are defaults. A model-level `compat` block can
override them for one model.

Request overrides apply to HTTP transports after Chord has constructed the
protocol request. Configuring them on a Codex Responses provider disables its
WebSocket transport for that request so the final JSON patch can be honored.

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

Provider/model requests identify the client with `User-Agent: chord/<version>` by default. Set provider-level `user_agent` only when a provider or gateway requires a specific value:

```yaml
providers:
  gateway:
    user_agent: RequiredGatewayClient/1.0
```

This setting also applies to Responses HTTP requests, Codex OAuth requests, Codex usage polling, and Responses WebSocket handshakes for that provider. WebFetch uses its own `web_fetch.user_agent`.

To send a Codex-style User-Agent, set the captured Codex value explicitly and keep it updated when the Codex client version or terminal changes:

```yaml
providers:
  codex:
    preset: codex
    user_agent: "codex-tui/0.139.0 (Mac OS 15.3.2; arm64) ghostty/1.3.1 (codex-tui; 0.139.0)"
```

## Provider timeouts

Provider-level timeout settings are optional and use seconds. Unset or `0` keeps the built-in defaults.

```yaml
providers:
  codex:
    response_header_timeout: 180
    stream_idle_timeout: 90
    websocket_handshake_timeout: 45
```

- `response_header_timeout`: timeout from starting a streaming HTTP request until response headers arrive, including connection setup and request-body upload. It stops once headers arrive and does not cap the total duration of a healthy stream; use `stream_idle_timeout` to bound gaps between streamed chunks. `0` keeps the built-in default.
- `stream_idle_timeout`: maximum idle time between streamed model data. When set, it overrides both the normal SSE idle timeout and slow-phase idle timeout for that provider, and it also applies to Codex Responses WebSocket reads.
- `websocket_handshake_timeout`: Responses WebSocket handshake timeout for providers using that transport, mainly `preset: codex` with `responses_websocket` enabled.

These settings are provider-scoped, so project-level `.chord/config.yaml` can override them for one provider without changing other providers. They do not change fixed low-level connection defaults such as dial or TLS handshake timeouts.

## Output token cap

Use `max_output_tokens` to set a global cap on requested output tokens. The effective request limit is still clamped by each model's `limit.output` and available total context (`limit.context` when known), so runtime uses the smallest applicable value.

Responses providers keep the stable Responses wire shape and do not send a `max_output_tokens` field on the HTTP or WebSocket request. The value can still affect Chord-side budgeting and compatibility checks, but it is not serialized into Responses requests.

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
retry classes that would normally wait and continue, such as all-keys-cooling,
concurrent-request 429 responses, or retryable HTTP 400 responses from a
non-official compatible gateway.

Provider HTTP 400 handling is intentionally conservative:

- Official APIs treat 400 as a terminal invalid-request error.
- Non-official compatible gateways may return 400 for transient gateway states
  such as concurrency limits or upstream capacity. Those non request-shaped 400s
  can cool the current key, rotate to the next key, and continue after all keys
  are cooling.
- Request/parameter/model-incompatible 400s still stop instead of retrying
  forever, for example a structured `code`/`type` such as `invalid_request_error`,
  `invalid_request`, or `missing_required_parameter`, or message-only inputs like
  `missing required parameter`,
  `Store must be set to false`, or `Stream must be set to true`.

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

`web_fetch` uses a built-in browser-like `User-Agent` by default. You can override it in config when a site needs a different header:

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

`web_fetch` intentionally remains a lightweight static HTTP reader. It does not run a local browser; JS-heavy pages may be marked as `Content-Quality: suspect-shell` when the returned HTML looks like an application shell rather than readable content.

## Multi-agent orchestration resource limits

The top-level `orchestration` section bounds process-local resources used by MainAgent/SubAgent workflows. It does not grant tool permissions or change per-agent delegation limits such as `delegation.max_children`; it limits how many admitted runtimes and LLM requests can run at once, how much SubAgent input can queue, and how much mailbox data remains in memory.

Most users should keep the built-in defaults. Configure these limits when a provider has a strict concurrency quota, the host has limited memory, or orchestration metrics show sustained queueing or rejection.

```yaml
orchestration:
  max_live_runtimes: 10
  max_borrowed_runtimes: 1
  max_active_llm_requests: 10
  provider_max_active_requests:
    openai: 6
    anthropic: 4
  model_max_active_requests:
    openai/gpt-5.5: 3
  subagent_queue_messages: 256
  subagent_queue_bytes: 4194304       # 4 MiB
  mailbox_memory_messages: 512
  mailbox_memory_bytes: 8388608       # 8 MiB
  subagent_compact_usage: 0.8
```

| Field | Default | Description |
|-------|---------|-------------|
| `max_live_runtimes` | `10` | Maximum normally admitted Agent runtimes. Further normal runtime acquisition waits until a slot is released. This is a soft process-local limit: a wake reactivation may use the bounded borrowed pool or, only when both pools are exhausted, an uncounted emergency bypass to avoid deadlocking the event loop. |
| `max_borrowed_runtimes` | `1` | Additional temporary runtime admissions used to wake orchestration work that must make progress, such as a parent resuming after a child event. Borrowing is bounded separately; emergency bypasses are observable in `RuntimeBypassActive` / `RuntimeBypassPeak` orchestration stats. |
| `max_active_llm_requests` | `10` | Process-wide maximum concurrent LLM requests across orchestrated agents. Eligible requests wait when the limit is full. |
| `provider_max_active_requests` | none | Optional concurrent-request limits keyed by provider name, for example `openai`. A request must satisfy this limit and the process-wide limit. |
| `model_max_active_requests` | none | Optional concurrent-request limits keyed by `provider/model`. Inline variants such as `@high` are ignored for matching, so `openai/gpt-5.5` covers all variants of that model. |
| `subagent_queue_messages` | `256` | Maximum pending input messages for each SubAgent. A new enqueue is rejected when either this count or the byte limit is reached; existing queued messages are preserved. |
| `subagent_queue_bytes` | `4194304` | Maximum estimated bytes of pending input for each SubAgent. This is an in-memory admission bound, not a disk spool. |
| `mailbox_memory_messages` | `512` | Maximum SubAgent mailbox messages retained in memory across the MainAgent inbox and owner-specific mailboxes. |
| `mailbox_memory_bytes` | `8388608` | Maximum estimated bytes retained by those in-memory mailboxes. Durable non-progress messages that exceed the memory budget are referenced through the on-disk mailbox spool; progress updates may be coalesced or omitted from memory. |
| `subagent_compact_usage` | `0.8` | Proactively compress a SubAgent's context when estimated usage reaches this fraction of its usable input budget. The default matches `context.compaction.threshold`; this separate setting remains available because SubAgents use local token estimates and a lightweight sliding-window checkpoint rather than MainAgent's usage-driven compaction pipeline. Must be greater than `0` and less than `1`. |

### Precedence and value rules

- These settings may appear in the global config and in project `.chord/config.yaml`. Positive project scalar values override the corresponding global values.
- `provider_max_active_requests` and `model_max_active_requests` are merged by key. A project entry replaces the same global key while preserving unrelated global entries.
- Scalar values that are zero or negative do not mean “unlimited”: they retain the inherited or built-in default. `subagent_compact_usage` also falls back to `0.8` unless it is strictly between `0` and `1`; unlike `context.compaction.threshold: 0`, zero does not disable SubAgent context protection.
- Only positive provider/model map limits are enforced. Keep map keys explicit and use positive integers; do not rely on zero as a general unlimited-mode switch.
- Limits are process-local. They do not coordinate quotas across multiple Chord processes.

### Tuning guidance

- To comply with an API quota, set the provider or model limit first; keep `max_active_llm_requests` as the overall safety ceiling.
- On a memory-constrained host, reduce mailbox byte/message limits gradually. Overflow uses durable storage, so lower limits trade memory for additional disk I/O.
- Reduce SubAgent queue limits only when producers can handle enqueue rejection. These queues do not spill to disk, and overly small limits can interrupt parent/child coordination.
- Keep `max_borrowed_runtimes` small but positive. Borrowed slots exist to break orchestration progress stalls, not to increase ordinary throughput.
- Lowering `subagent_compact_usage` reduces context-overflow risk but causes earlier and more frequent compression. Raising it reduces compression work but leaves less recovery headroom.
- Increasing concurrency is not automatically faster: provider throttling, model latency, local memory pressure, and workspace lease contention can reduce effective throughput. Change limits using observed queue/rejection metrics and end-to-end latency rather than CPU count alone.

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
  write: ask
  edit: ask
---

You are an agent focused on backend development.
```

Equivalent YAML agent example:

```yaml
name: backend-coder
description: Backend developer
mode: subagent
permission:
  write: ask
  edit: ask
prompt: |
  You are an agent focused on backend development.
```

Common fields include:

- `name`: agent name. If omitted, Chord uses the filename without extension. If specified, it must match the filename without extension (for example, `builder.yaml` must declare `name: builder`). A single directory cannot contain duplicate agent names, including duplicates across `.md`, `.yaml`, and `.yml`. Project-level agents may still override same-named global agents by design.
- `description`: short description shown to the main agent when delegation is available.
- `mode`: `main` for a MainAgent role, or `subagent` for a SubAgent. Empty and unknown values behave as `main`; `sub_agent` and `sub` are accepted as SubAgent aliases.
- `model_pools`: optional ordered list of pool names this agent can use. Pool definitions live in `config.yaml` top-level `model_pools`; when omitted, the agent can use all top-level pools sorted by name.
  Inline variants such as `openai/gpt-5.5@high` are specified in the pool definitions.
- `variant`: default variant when a model ref does not include `@variant`.
- `permission`: per-tool permission policy for this agent. Permissions live directly in agent config files; when the confirmation popup remembers a rule, `project` updates the current project's `.chord/agents/<role>.yaml`, and `global` updates the user config directory's `agents/<role>.yaml` (default: `~/.config/chord/agents/<role>.yaml`). Chord no longer writes a separate permissions directory. Some orchestration tools have special semantics (`delegate` patterns match `agent_type` and also gate delegated-work controls such as `cancel`; `handoff` and `done` treat `allow` and `ask` as workflow-available states with Chord's own confirmation gates). See [Permissions & Safety](./permissions-and-safety.md#special-permission-semantics) before relying on fine-grained control-tool rules.
- `mcp`: additional auto-start MCP servers scoped to this agent. Agent MCP is additive: a server name already present in the effective global/project `mcp` config is a startup error. Agent-scoped servers cannot use `manual: true` because runtime MCP controls manage the top-level server surface; configure a manual server at the project/global level instead. Remove an agent entry to inherit a top-level server, rename it for a separate private server, or override the top-level server in `.chord/config.yaml` for the whole project. Different agents may reuse the same private server name without sharing the connection unless they are instances of the same agent definition.
- `delegation`: limits such as `max_children`, `max_depth`, and `child_join`.
- `prompt` / `system_prompt`: system prompt for plain YAML files.

Example:

```yaml
name: builder
mode: main
model_pools: [default]
permission:
  "*": deny
  read: allow
  view_image: allow
  grep: allow
  glob: allow
  web_fetch:
    "localhost:8000": ask
  shell: allow
  edit: ask
  write: ask
```

## Context management

Long-session context handling — **context compaction** (LLM-generated summaries
that rewrite session history) and **context reduction** (request-time trimming
of stale tool output) — is configured under the top-level `context:` key and
documented on its own page: [Context management](./context-management.md).

## Post-tool diagnostics

After `edit`, `patch`, or `write` modifies a file, Chord can append language diagnostics to the tool result so the model sees compile or lint problems immediately. This is controlled by the `diagnostics` config and is enabled by default for Python (an LSP semantic backend with a Ruff quick fallback). Set `diagnostics.enabled: false` to skip the whole pipeline.

Native file tools also send `workspace/didChangeWatchedFiles` events to matching LSP servers before syncing the `textDocument`: `write` sends Created for new files, `write` on existing files plus `edit` / `patch` send Changed, and successful `delete` sends Deleted. This helps Pyright, TypeScript, gopls, rust-analyzer, and similar servers refresh their project graph promptly, reducing transient unresolved-import/module diagnostics after new files are created. Diagnostics are still returned immediately in file-tool results so the model can attribute problems to the current edit; files created or removed by `shell` commands or external programs are not yet reported through a full filesystem watcher.

For Python, two backends are used:

- `diagnostics.python.semantic_backend` — the primary LSP server (default `pyright`). Its `server` field must match a server key under `lsp` so the language server is actually configured.
- `diagnostics.python.quick_backend` — a one-shot fallback (default `ruff check`) used for large files, or when the semantic backend is unavailable.

`diagnostics.python.large_file.{line_threshold, byte_threshold, strategy}` decides when a file is large enough to use the quick backend instead of the semantic one; `run_semantic_when_quick_unavailable: true` forces the semantic backend even on large files when the quick backend is missing. Ruff quick diagnostics do not update the LSP sidebar — they appear only in `edit`, `patch`, or `write` results and note that full semantic diagnostics were skipped.

Recommended Python skeleton:

```yaml
lsp:
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]

diagnostics:
  python:
    semantic_backend:
      server: pyright
    quick_backend:
      type: command
      command: ruff
```

`diagnostics.python.output.{max_near_diagnostics, max_outside_diagnostics, max_total_diagnostics, near_range_before_lines, near_range_after_lines}` shapes how much appended diagnostics text is shown, prioritizing errors and warnings before info and hints. See the [Configuration cheatsheet](#configuration-cheatsheet) for the full field list.

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
| `context`               | object                | see below                        | global / project         | `compaction` and `reduction` settings. See [Context compaction](./context-management.md#context-compaction) and [Context reduction](./context-management.md#context-reduction). |
| `diagnostics`           | object                | enabled (Python LSP + Ruff fallback) | global / project     | Post-tool diagnostics appended to `edit`, `patch`, or `write` results. `diagnostics.python.semantic_backend` is the primary LSP server (default `pyright`); `diagnostics.python.quick_backend` is a one-shot fallback (default `ruff check`). `diagnostics.python.large_file.{line_threshold, byte_threshold, strategy}` controls when large files use the quick backend, and `run_semantic_when_quick_unavailable: true` forces semantic diagnostics when the quick backend is missing. `diagnostics.python.output.{max_near_diagnostics, max_outside_diagnostics, max_total_diagnostics, near_range_before_lines, near_range_after_lines}` shapes the appended diagnostics text. Diagnostics are shown by severity priority (errors/warnings first, then info/hints if slots remain). Set `diagnostics.enabled: false` to skip the whole pipeline. |
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
by `cache_control` blocks, and Chord also sends JSON-formatted
`metadata.user_id` automatically with a stable anonymous `device_id` plus a
stable routing `session_id` derived from local/provider identity. These
Anthropic metadata fields are not user-configurable. In `explicit` mode (the
default for Anthropic models), Chord places up to four `cache_control`
breakpoints by priority: the last system block, the frozen reduced-prefix
boundary (when incremental reduction has frozen a stable prefix), the last
user message, and the last assistant message — so long agent loops reuse the
frozen historical surface instead of re-writing the moving tail each turn.
For Anthropic models, you can request an hourly cache TTL with
`prompt_cache.ttl: 1h`:

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
| `api_url`      | string | Endpoint URL. Chord detects provider type from the URL path, ignoring query strings and fragments. For Gemini, the `/models` base path; Chord appends `/{model}:streamGenerateContent?alt=sse`. For Azure Responses, `?api-version=...` is optional and can be used to pin a specific API version. |
| `preset`       | string | `codex` (OpenAI Codex / ChatGPT OAuth) or `azure` (Azure OpenAI Responses with `api-key` auth).                                                           |
| `official_api` | bool   | Treat this endpoint as an official provider API where HTTP 400 usually means an invalid request and should not be retried as a transient gateway error. `preset: codex` and `preset: azure` are official by default; omit or set `false` for aggregating/proxy gateways. |
| `key_rotation` | string | `on_failure` (default) / `per_request`. Controls when a credential / API key is reselected.                                                            |
| `key_order`    | string | `sequential` (non-Codex default) / `random` / `smart` (Codex only). Controls how Chord chooses among selectable keys.                                   |
| `compress`     | bool   | gzip request bodies when compression saves bytes. Off by default.                                                                                       |
| `response_header_timeout` | int | Timeout in seconds from starting a streaming HTTP request until response headers arrive, including connection setup and request-body upload. `0` / omitted uses the built-in default; healthy streams are bounded by `stream_idle_timeout`, not a total request timer. |
| `stream_idle_timeout` | int | Stream idle timeout in seconds for this provider. `0` / omitted uses built-in SSE/WebSocket idle defaults. |
| `websocket_handshake_timeout` | int | Responses WebSocket handshake timeout in seconds. `0` / omitted uses the built-in default. |
| `supported_service_tiers` | list | Provider-level default accepted non-standard tiers for its models, e.g. `[fast, slow]` or `[fast]`. Model entries can override it. |
| `models`       | map    | Map of model id → [model config](#model-field-reference).                                                                                               |

### Model field reference

| Field             | Type   | Description                                                                                                            |
| ----------------- | ------ | ---------------------------------------------------------------------------------------------------------------------- |
| `limit.context`   | int    | Total request window in tokens when known. If `limit.input` is omitted, Chord derives the input budget from this minus effective requested output. |
| `limit.input`     | int    | Separate input cap when a provider publishes one. Chord uses it to compact or retry before the prompt is too large.               |
| `limit.output`    | int    | Maximum output tokens; runtime is also clamped by `max_output_tokens`.                                                             |
| `context.compaction.reserved` | int | Optional input-budget headroom reserved before `compaction.threshold` is applied. Useful for tokenizer drift, tool overhead, and safer overflow recovery. |
| `reasoning`       | object | OpenAI reasoning options. `reasoning.effort` is normalized and passed through verbatim, so any provider-supported level (e.g. GLM `max` / `minimal` / `none`) reaches the upstream unchanged (unset = omit and use provider/model default). For Responses, `reasoning.summary` (`auto` / `concise` / `detailed`; unset = omit / no explicit summary request). Recommended summary value when you want readable summaries: `auto`. |
| `text.verbosity`  | string | Optional OpenAI text verbosity hint where supported; leave unset to use the provider/model default unless you intentionally want `low` / `medium` / `high`. |
| `thinking`        | object | Anthropic extended-thinking options. `type: adaptive` lets Chord derive a budget from `effort`; `thinking.effort` is sent as `output_config.effort` for Messages requests; `display: summarized` enables summarized thinking blocks (valid only with `type: enabled` or `adaptive`). |
| `compat.reasoning_continuity.mode` | string | Optional continuity override. Use `openai_visible` only for Chat Completions models that require unchanged assistant `reasoning_content` replay; it does not inject request fields. Use `none` on a model to opt out of a provider-level default. |
| `compat.request_overrides.body` | object | Recursive JSON patch applied after Chord constructs the protocol request. `null` deletes a field. |
| `compat.request_overrides.rename_body_fields` | map | Renames final JSON fields while preserving Chord's computed values. A `null` target deletes the source field. |
| `compat.request_overrides.headers` | map | Sets final request headers. A `null` value removes that header. |
| `variants`        | map    | Named parameter presets. Reference with `provider/model@variant`.                                                      |
| `modalities.input`| array  | Subset of `text` / `image` / `pdf`. Defaults to `[text]`; declare `image` / `pdf` explicitly when supported.          |
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
