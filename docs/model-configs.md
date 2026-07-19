# Model configuration recipes

Use this page when you already know which provider/model family you want and just need a copy-paste-ready starting point. Keep [Configuration & Auth](./configuration.md) for field semantics and full schema details; use [Examples](./examples/index.md) for full multi-file workstation/team layouts.

## OpenAI Responses-compatible: GPT-5.4 / GPT-5.5 / GPT-5.6

Use these same model limits with Codex OAuth, Responses relays, and other
compatible providers. Pair API-key providers with the matching entry in
`~/.config/chord/auth.yaml`:

```yaml
openai:
  - "$OPENAI_API_KEY"
```

### GPT-5.4

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.4:
        limit:
          context: 1050000
          input: 950000
          output: 128000
        cost:
          input: 2.5
          output: 15
          cache_read: 0.25
        reasoning:
          summary: auto
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
        modalities:
          input: [text, image, pdf]

model_pools:
  default:
    - openai/gpt-5.4@high
```

Verify:

```bash
chord doctor models --model openai/gpt-5.4@high
```

### GPT-5.5

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
        cost:
          input: 5
          output: 30
          cache_read: 0.5
        reasoning:
          summary: auto
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
        modalities:
          input: [text, image, pdf]

model_pools:
  default:
    - openai/gpt-5.5@high
```

Verify:

```bash
chord doctor models --model openai/gpt-5.5@high
```

### GPT-5.6 alias (`gpt-5.6` → Sol)

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
        cost:
          input: 5
          output: 30
          cache_read: 0.5
          cache_write: 6.25
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

Use explicit model IDs when you want fixed pricing/behavior:

### GPT-5.6 Sol

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-sol:
        limit:
          context: 500000
          input: 372000
          output: 128000
        cost:
          input: 5
          output: 30
          cache_read: 0.5
          cache_write: 6.25
        reasoning:
          effort: medium
          summary: auto
        variants:
          max:
            reasoning:
              effort: max
        modalities:
          input: [text, image]
```

### GPT-5.6 Terra

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-terra:
        limit:
          context: 500000
          input: 372000
          output: 128000
        cost:
          input: 2.5
          output: 15
          cache_read: 0.25
          cache_write: 3.125
        reasoning:
          effort: medium
          summary: auto
        variants:
          max:
            reasoning:
              effort: max
        modalities:
          input: [text, image]
```

### GPT-5.6 Luna

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-luna:
        limit:
          context: 500000
          input: 372000
          output: 128000
        cost:
          input: 1
          output: 6
          cache_read: 0.1
          cache_write: 1.25
        reasoning:
          effort: medium
          summary: auto
        variants:
          max:
            reasoning:
              effort: max
        modalities:
          input: [text, image]
```

Notes:

- `gpt-5.6` currently resolves to Sol, so its `cost` block should match Sol pricing.
- GPT-5.6 API reasoning efforts can include `none`, `low`, `medium`, `high`, `xhigh`, and `max`.
- `reasoning.summary: auto` is optional. Leave it unset when you do not want Chord to explicitly request a readable reasoning summary.
- Chord does not currently expose GPT-5.6 `reasoning.mode: pro`.

Verify:

```bash
chord doctor models --model openai/gpt-5.6@max
```

## Codex OAuth preset

Use this when you want ChatGPT/Codex OAuth instead of API keys. Model limits are
the same limits used by the GPT-5.x recipes above; only the provider preset and
authentication method change.

The GPT-5.x limits used throughout this page are:

| Model | `limit.context` | `limit.input` | `limit.output` |
| --- | ---: | ---: | ---: |
| GPT-5.4 | 1,050,000 | 950,000 | 128,000 |
| GPT-5.5 | 400,000 | 272,000 | 128,000 |
| GPT-5.6 Sol / Terra / Luna | 500,000 | 372,000 | 128,000 |

Keep all three fields: `context` is the total input-plus-output window exposed
by Codex, while `input` and `output` are the separate hard allocations within
that window. The separate maxima do not need to add up to `context`: near the
input cap, less space remains for output.

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
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
          max:
            reasoning:
              effort: max
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

model_pools:
  default:
    - codex/gpt-5.5@high
```

Authenticate with:

```bash
chord auth codex
```

Notes:

- Keep API-key and Codex OAuth providers separate when you use both because their credentials differ, but reuse the same model limit blocks.
- GPT-5.4 uses `1050000 / 950000 / 128000`: the 1.05M total window, Codex's 95% effective input budget, and the model's maximum output.
- Use `500000 / 372000 / 128000` for `gpt-5.6-sol`, `gpt-5.6-terra`, and `gpt-5.6-luna`.
- These values track the current Codex model catalog and may change with a future Codex release. Update all three fields together when the backend allocation changes.

## Anthropic Claude

Pair with `~/.config/chord/auth.yaml`:

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
```

```yaml
providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.8:
        limit:
          context: 1000000
          output: 128000
        cost:
          input: 5
          output: 25
          cache_read: 0.5
          cache_write: 6.25
          cache_write_1h: 10
        thinking:
          type: adaptive
          display: summarized
        variants:
          high:
            thinking:
              effort: high
          xhigh:
            thinking:
              effort: xhigh
        modalities:
          input: [text, image, pdf]

model_pools:
  default:
    - anthropic/claude-opus-4.8@high
```

For a lower-cost Claude family config, use the same shape with `claude-sonnet-4.6`, `output: 64000`, and Sonnet pricing from your account/provider docs.

## Google Gemini

Pair with `~/.config/chord/auth.yaml`:

```yaml
gemini:
  - "$GEMINI_API_KEY"
```

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
        thinking:
          budget: -1
          level: high

model_pools:
  default:
    - gemini/gemini-3.5-flash
```

Notes:

- Keep `api_url` at the `/models` base path. Chord appends `/{model}:streamGenerateContent?alt=sse` automatically.
- `type` can be omitted; Chord auto-detects Gemini from the `/models` path.

## GLM-5.2 / BigModel Coding Plan

Pair with `~/.config/chord/auth.yaml`:

```yaml
bigmodel:
  - "$BIGMODEL_API_KEY"
```

```yaml
model_templates:
  glm-5.2-chat: &glm-5-2-chat
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

  glm-5.2-messages: &glm-5-2-messages
    limit:
      context: 1000000
      output: 128000
    thinking:
      type: adaptive
      effort: max
    compat:
      request_overrides:
        headers:
          anthropic-beta: null

  glm-5.2-responses: &glm-5-2-responses
    limit:
      context: 1000000
      output: 128000
    reasoning:
      effort: max

providers:
  bigmodel:
    type: chat-completions
    api_url: https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
    models:
      glm-5.2: *glm-5-2-chat

  bigmodel-messages:
    type: messages
    api_url: https://open.bigmodel.cn/api/anthropic/v1/messages
    models:
      glm-5.2: *glm-5-2-messages

  glm-responses:
    type: responses
    api_url: https://example.com/v1/responses
    models:
      glm-5.2: *glm-5-2-responses

model_pools:
  default:
    - bigmodel/glm-5.2
```

Notes:

- Chat Completions requires `thinking.type: enabled`, `reasoning_effort`, and
  `max_tokens`. `request_overrides` adds GLM's thinking flags and renames the
  dynamically calculated output-limit field; `openai_visible` only replays
  unmodified `reasoning_content`.
- Messages-compatible endpoints use `thinking` plus `output_config.effort`.
  Disable Anthropic beta headers unless that endpoint documents support.
- A GLM `/responses` endpoint is gateway-specific. Use a separate template with
  `reasoning.effort` only when the gateway documents OpenAI Responses mapping.

## DeepSeek-V4-Pro

Pair with `~/.config/chord/auth.yaml`:

```yaml
deepseek:
  - "$DEEPSEEK_API_KEY"
```

```yaml
model_templates:
  deepseek-v4-pro-chat: &deepseek-v4-pro-chat
    limit:
      context: 1000000
      output: 64000
    reasoning:
      effort: max
    compat:
      request_overrides:
        rename_body_fields:
          max_completion_tokens: max_tokens
        body:
          thinking:
            type: enabled
      reasoning_continuity:
        mode: openai_visible

  deepseek-v4-pro-messages: &deepseek-v4-pro-messages
    limit:
      context: 1000000
      output: 64000
    thinking:
      type: adaptive
      effort: max
    compat:
      request_overrides:
        headers:
          anthropic-beta: null

  deepseek-v4-pro-responses: &deepseek-v4-pro-responses
    limit:
      context: 1000000
      output: 64000
    reasoning:
      effort: max

providers:
  deepseek:
    type: chat-completions
    api_url: https://api.deepseek.com/v1/chat/completions
    models:
      deepseek-v4-pro: *deepseek-v4-pro-chat

  deepseek-messages:
    type: messages
    api_url: https://api.deepseek.com/anthropic/v1/messages
    models:
      deepseek-v4-pro: *deepseek-v4-pro-messages

  deepseek-responses:
    type: responses
    api_url: https://example.com/v1/responses
    models:
      deepseek-v4-pro: *deepseek-v4-pro-responses

model_pools:
  default:
    - deepseek/deepseek-v4-pro
```

Notes:

- DeepSeek Chat thinking uses `thinking.type`, top-level `reasoning_effort`, and
  `max_tokens`. `request_overrides` supplies the request-shape differences;
  during thinking + tool-call loops, `openai_visible` returns the assistant's
  `reasoning_content` unchanged.
- DeepSeek Messages supports `output_config.effort`; Chord derives it from
  `thinking.effort`. Disable Anthropic beta headers for the compatible endpoint.
- Treat third-party `/responses` endpoints as gateway-specific; use
  `reasoning.effort` only when the gateway documents its mapping.
- For compatible gateways, use the exact model ID and limits published by that
  gateway/account. See [Troubleshooting — DeepSeek / OpenAI-compatible thinking-mode 400s](./troubleshooting.md#deepseek--openai-compatible-thinking-mode-400s).

## Qwen preserved thinking

Qwen returns visible reasoning through `reasoning_content`, but most models
ignore that field in history by default. Only enable replay on a model that
documents `preserve_thinking` support (currently Qwen 3.6/3.7 Max and Plus
families); older Qwen 3/3.5 models may still emit reasoning but should leave
continuity disabled.

```yaml
model_templates:
  qwen-preserved: &qwen-preserved
    limit:
      context: 1000000
      output: 65536
    compat:
      request_overrides:
        body:
          enable_thinking: true
          preserve_thinking: true
      reasoning_continuity:
        mode: openai_visible

providers:
  qwen:
    type: chat-completions
    api_url: https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions
    models:
      qwen3.7-plus: *qwen-preserved

model_pools:
  default:
    - qwen/qwen3.7-plus
```

Use the limits and regional endpoint published for your account. Historical
reasoning counts as input tokens and billing when `preserve_thinking` is true.

## Kimi K3

Kimi K3 is the current flagship thinking model. It has a 1M-token context,
always reasons, currently accepts only `reasoning_effort: max`, and requires
the complete assistant message (including `reasoning_content`) in multi-turn
conversations and tool-call loops. Do not pass the K2.x `thinking` parameter or
fixed sampling fields such as `temperature`.

```yaml
model_templates:
  kimi-k3: &kimi-k3
    limit:
      context: 1048576
      output: 131072
    reasoning:
      effort: max
    compat:
      reasoning_continuity:
        mode: openai_visible

  kimi-k2.7-code: &kimi-k2-7-code
    limit:
      context: 262144
      output: 32768
    compat:
      reasoning_continuity:
        mode: openai_visible

  kimi-k2.6-thinking: &kimi-k2-6-thinking
    limit:
      context: 262144
      output: 32768
    compat:
      request_overrides:
        body:
          thinking:
            type: enabled
            keep: all
      reasoning_continuity:
        mode: openai_visible

providers:
  kimi:
    type: chat-completions
    api_url: https://api.moonshot.ai/v1/chat/completions
    models:
      kimi-k3: *kimi-k3
      kimi-k2.7-code: *kimi-k2-7-code
      kimi-k2.6: *kimi-k2-6-thinking

model_pools:
  default:
    - kimi/kimi-k3
```

K2.7 Code is the 256K coding-specialized, thinking-only option; its thinking
mode and `keep: all` behavior are fixed, so the template does not send a
`thinking` object. K2.6 is the 256K general-purpose hybrid option and therefore
sets both fields explicitly. K2.5 does not support preserved thinking and is
being retired for new users; prefer K3 for new configurations.

For all `openai_visible` recipes (DeepSeek, GLM, supported Qwen, and Kimi),
Chord only replays native reasoning within the producing provider. This keeps
documented in-provider upgrades such as Kimi K2.6/K2.7 to K3 working, while a
cross-provider fallback drops the incompatible reasoning/tool trajectory so
the target can plan afresh instead of receiving another provider's chain of
thought or an invalid partial trajectory.

## Grok 4.5 (xAI Responses)

xAI recommends the Responses API for Grok. Grok 4.5 supports text and image
input, function calling, structured output, reasoning, and a 500K context
window. It emits raw reasoning through `response.reasoning_text.*` stream
events; Chord maps those events to the normal thinking stream while preserving
the ordered Responses output items for tool-loop continuity.

```yaml
model_templates:
  grok-4.5: &grok-4-5
    limit:
      context: 500000
      output: 64000 # conservative local allocation; xAI publishes the total context
    reasoning:
      effort: high
    modalities:
      input: [text, image]
    cost:
      input: 2
      output: 6
      cache_read: 0.3
      input_tiers:
        - above_input_tokens: 199999
          input: 4
          output: 12
          cache_read: 0.6


providers:
  xai:
    type: responses
    api_url: https://api.x.ai/v1/responses
    models:
      grok-4.5: *grok-4-5

model_pools:
  default:
    - xai/grok-4.5
```

Use `grok-4.5` or the rolling `grok-4.5-latest` alias. Do not configure
`openai_visible`: xAI Responses uses native ordered output/reasoning state, not
Chat Completions `reasoning_content`. `reasoning.effort` accepts `low`,
`medium`, or `high`; high is the default and reasoning cannot be disabled.
`grok-4.20-fast` is not an official xAI model ID. The official
`grok-4.20-multi-agent` model has a 1M context window and should be configured
from its own current xAI model page rather than copied from Grok 4.5.

## Verify any recipe

After copying a recipe, run one targeted check first:

```bash
chord doctor models --model provider/model
```

Then verify the exact variant you plan to use, for example:

```bash
chord doctor models --model openai/gpt-5.6@max
chord doctor models --model codex/gpt-5.5@max
chord doctor models --model anthropic/claude-opus-4.8@high
```
