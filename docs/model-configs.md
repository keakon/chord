# Model configuration recipes

<!-- description: Ready-to-copy provider and model pool recipes: OpenAI, Anthropic, Codex OAuth, and OpenAI-compatible gateways. -->

Use this page when you already know which provider/model family you want and just need a copy-paste-ready starting point. If you have not chosen a channel yet, start with [Choosing models](./model-choice.md). Field semantics and the full schema live in [Configuration & Auth](./configuration.md); full multi-file workstation/team layouts live in [Examples](./examples/index.md).

## How to use this page

Choose a connection type, then copy its recipe. Keep the default context settings until the model connects successfully; tune them later if needed.

| Connection | Recipe |
| --- | --- |
| OpenAI API / Responses-compatible endpoint | [OpenAI GPT](#openai-gpt-responses) |
| Codex OAuth | [Codex sign-in](#codex-oauth-preset) |
| Anthropic API | [Claude](#anthropic-claude) |
| Google API | [Gemini](#google-gemini) |
| Other models | [GLM](#glm--bigmodel-coding-plan) · [DeepSeek](#deepseek) · [Qwen](#qwen-preserved-thinking) · [Kimi](#kimi) · [Grok](#grok-xai) · [MiniMax](#minimax-openai-compatible) · [MiMo](#xiaomi-mimo-openai-compatible) · [Muse Spark](#meta-muse-spark) |
| Thinking through a Chat Completions gateway | [Gateway settings](#thinking-behind-a-chat-completions-gateway) |

After copying a recipe, [verify the configuration and connection](#verify-any-recipe). For long sessions or context-cost tuning, see [Per-model compaction tuning](#per-model-compaction-tuning) at the end of this page.

## OpenAI GPT (Responses)

The GPT-5.4 / GPT-5.5 / GPT-5.6 / GPT-6 Astra snippets use the limits published on the OpenAI model pages: GPT-5.4 / 5.6 / 6 run a `1050000 / 922000 / 128000` allocation (1.05M total window; the 922K input budget derives as `context` minus `output`, since these models publish no separate input cap) on both the API and the current Codex catalog, while GPT-5.5 stays on `400000 / 272000 / 128000`. If your account or relay still serves an older profile, fall back to `400000 / 272000 / 128000` for the affected models.

The cost blocks use OpenAI API pricing; override them when your relay charges different rates. Codex OAuth has a separate preset block below. Pair API-key providers with the matching entry in `~/.config/chord/auth.yaml`:

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
          input: 922000
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
    - openai/gpt-5.4@xhigh
```

Verify:

```bash
chord doctor models --model openai/gpt-5.4@xhigh
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
    - openai/gpt-5.5@xhigh
```

Verify:

```bash
chord doctor models --model openai/gpt-5.5@xhigh
```

### GPT-5.6 (Sol / Terra / Luna)

The 5.6 family has three models: `gpt-5.6-sol`, `gpt-5.6-terra`, and
`gpt-5.6-luna`. They share the same window, reasoning, variants, and
modalities, so a common `&gpt-5-6-base` anchor carries those and each model
entry only adds its own `cost` block.

```yaml
model_templates:
  gpt-5.6-base: &gpt-5-6-base
    # 1.05M model-page limits, shared by the API and the current Codex
    # catalog: 1050000 total window / 922000 input budget (context minus
    # output; these models publish no separate input cap) / 128000 output.
    # Fall back to 400000/272000/128000 when your account or relay still
    # serves the older profile.
    limit:
      context: 1050000
      input: 922000
      output: 128000
    reasoning:
      effort: medium
      summary: auto
    variants:
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
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
      input: [text, image, pdf]
```

### GPT-5.6 Sol

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-sol:
        <<: *gpt-5-6-base
        cost:
          input: 5
          output: 30
          cache_read: 0.5
          cache_write: 6.25
          input_tiers:
            - above_input_tokens: 272000
              input: 10
              output: 45
              cache_read: 1
              cache_write: 12.5

model_pools:
  default:
    - openai/gpt-5.6-sol@xhigh
```

### GPT-5.6 Terra

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-terra:
        <<: *gpt-5-6-base
        cost:
          input: 2
          output: 12
          cache_read: 0.2
          cache_write: 2.5
          input_tiers:
            - above_input_tokens: 272000
              input: 4
              output: 18
              cache_read: 0.4
              cache_write: 5

model_pools:
  default:
    - openai/gpt-5.6-terra@max
```

### GPT-5.6 Luna

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-luna:
        <<: *gpt-5-6-base
        cost:
          input: 0.2
          output: 1.2
          cache_read: 0.02
          cache_write: 0.25
          input_tiers:
            - above_input_tokens: 272000
              input: 0.4
              output: 1.8
              cache_read: 0.04
              cache_write: 0.5

model_pools:
  default:
    - openai/gpt-5.6-luna@max
```

Notes:

- The 5.6 examples declare the model-page window directly as
  `1050000 / 922000 / 128000`: the 922K input budget derives as `context`
  minus `output` (these models publish no separate input cap) and needs no
  explicit `input`. Only the 400K-allocation models (GPT-5.5 / 5.2 above)
  keep `input: 272000`. If your account or relay still serves the older
  Codex profile, fall back to `400000 / 272000 / 128000` for the 5.6 tiers.
- GPT-5.6 API reasoning efforts can include `none`, `low`, `medium`, `high`, `xhigh`, and `max`.
- Responses defaults `reasoning.summary` to `auto` while reasoning is active; set `reasoning.summary: none` when you do not want Chord to request a readable summary.
- Chord does not currently expose GPT-5.6 `reasoning.mode: pro`.

Verify:

```bash
chord doctor models --model openai/gpt-5.6-sol@xhigh
```

#### Compaction tuning for GPT-5.6

Start from two questions: **which budget the model runs against**, the
1.05M / 922K allocation used by the examples above or a
`400000 / 272000` fallback profile when your account/relay still serves the
older Codex catalog, and **what the threshold is for**: keeping quality up,
staying under the 272K long-context pricing tier, or using the window for raw
capacity. The trigger is `threshold × usable input budget`, so the same ratio
fires at very different token counts under the two budgets; a recipe tuned
for one does not transfer to the other.

**Long-context quality** (MRCR v2 8-needle results as reported by OpenAI):
Sol/Terra stay strong in the 256K–512K band (91.5% / 89.6%) and drop to ~73%
(73.8% / 72.5%) in the 512K–1M band, while Luna sits at 41.3% in both, a
cliff rather than a slope. The bands are averages, so treat them as a broad guide
for where quality starts slipping, not as an exact cliff location.

**Pricing** (official OpenAI API): a prompt that exceeds 272K input tokens
(exactly 272000 does not) bills the **entire request** at the long-context rates (2x input / cache-read / cache-write and 1.5x output), not just the portion above 272K. Relays and Codex OAuth set their own prices, so this tier does not necessarily apply there. Chord's cost accounting selects the tier from the full prompt, but automatic compaction does not know about price tiers: it fires on a usage ratio, so keeping requests under 272K is a tuning goal, not a guarantee.

The trigger compares the last provider-reported usage with the budget, a single large tool result can push the next prompt past the line, and while `model_driven` is enabled the grace period lets crossing requests run before compaction starts. Leave headroom below the line. Every compaction also costs a summarization call and loses raw context, so compressing too eagerly can cost more than the tier it avoids.

Cost-first (Sol/Terra/Luna share this: it keeps usage under the 272K tier
and below Luna's 256K+ collapse zone):

```yaml
model_templates:
  gpt-5.6-cost-first: &gpt-5-6-cost-first
    <<: *gpt-5-6-base
    compaction:
      threshold: 0.25       # 0.25 × 922K ≈ 231K, under the 272K pricing tier
      reminder: 0.2
```

Quality-first (Sol/Terra; Luna has no strong long-context band to aim for):

```yaml
model_templates:
  gpt-5.6-quality-first: &gpt-5-6-quality-first
    <<: *gpt-5-6-base
    compaction:
      threshold: 0.55       # ≈ 507K; 0.5–0.65 are reasonable
```

The `reminder` above is optional: omit it to accept the value derived from
`threshold` (0.50 for the quality-first template; derivation in
[Per-model compaction tuning](#per-model-compaction-tuning)). Going above ~0.65 moves
the trigger past ~600K–640K, already inside the band where Sol/Terra measure
~73%; 0.7 (~645K–690K) is a capacity-first choice that deliberately accepts
the long-context rate and some quality loss, and 0.8 (~738K–789K) even more
so. Do not reuse the old 0.3 Luna recipe under this window: it fires at
~277K–296K, already past the pricing line.

##### Codex subscription windows are server-controlled

On a Codex subscription endpoint (`preset: codex`, or a `/codex/responses`
relay), the window a ChatGPT account actually gets comes from the
server-delivered model catalog (`context_window` / `max_context_window`),
not from the model page: those catalog values have changed repeatedly and
differed between accounts (input-side caps as low as 272K have shipped while
the model page advertised 1.05M). `/status` may show the configured value
before the first request and the real cap only after it. So:

- Before relying on the 1.05M allocation for long sessions, measure what the
  endpoint actually accepts: configure the candidate `limit`, run a long
  session, and watch the logs for `context_length_exceeded` / oversize
  rejections.
- If your account or relay still serves the older profile, fall back to
  `400000 / 272000 / 128000` for that provider.
- `threshold` is decoupled from the window: it is the fraction of the usable
  budget at which to compact, chosen by your quality/cost tradeoff, but the
  API's >272K-input whole-request 2× pricing cliff applies regardless of the
  window, so keep the trigger inside it if that pricing applies to your
  route. Leave headroom: the trigger compares the last provider-reported
  usage against the budget, and a single large tool result can push the next
  prompt past the line.

As everywhere on this page, the `compaction` block lives on the model
template so every provider referencing it inherits it, and the fields tune
the usage-driven automatic-compaction path regardless of `model_driven`. A
block on a shared template such as `&gpt-5-6-base` applies to every model that
merges it; for per-tier tuning, give that tier its own template.

### GPT-6 Astra

GPT-6 Astra is OpenAI's current flagship (`gpt-6-astra`): a 1,050,000-token context window with 128,000 max output and 922,000 usable input (derived as `context` minus `output` when `input` is unset). Reasoning supports `low`, `medium`, `high`, `xhigh`, and `max`; there is no `none` effort. Standard pricing is $10 input / $50 output per 1M with $1 cached input and $12.50 cache writes; prompts above 272K input bill the whole request at 2× input/cache and 1.5× output.

Unlike GPT-5.6 there are no Sol/Terra/Luna tiers: `gpt-6-astra` is a single model ID, so its recipe carries no tier variants. Pair the API-key provider with an entry in `~/.config/chord/auth.yaml`:

```yaml
openai:
  - "$OPENAI_API_KEY"
```

The base template carries a cost-first `compaction` block by default: 272K is
a pricing cliff (the whole request reprices, not just the tokens above the
line), so keeping usage under it is the largest cost lever, and Astra stays at
full long-context quality well below it (OpenAI reports 100% on MRCR v2
8-needle at 256K–512K). Raise `threshold` past 0.29 only when you accept the
2× long-context rate.

```yaml
model_templates:
  gpt-6-astra-base: &gpt-6-astra-base
    limit:
      context: 1050000
      output: 128000        # full API window: no `input`; usable input derives as 922K
    cost:
      input: 10
      output: 50
      cache_read: 1
      cache_write: 12.5
      input_tiers:
        - above_input_tokens: 272000
          input: 20
          output: 75
          cache_read: 2
          cache_write: 25
    reasoning:
      effort: medium
      summary: auto
    variants:
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
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
      input: [text, image, pdf]
    compaction:
      threshold: 0.25       # fires at ~231K, under the 272K pricing cliff
      reminder: 0.2

providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-6-astra: *gpt-6-astra-base

model_pools:
  default:
    - openai/gpt-6-astra@medium
```

Verify:

```bash
chord doctor models --model openai/gpt-6-astra@medium
```

Notes:

- This snippet targets the **official OpenAI API**, so it declares the full
  `1050000` window with no `input`: Chord derives the usable input budget as
  `context` minus the model's own `output` cap (`1050000 − 128000 = 922000`),
  and reserves the default `64000` output cap only for models that declare no
  `limit.output`. Above 272K is a pricing threshold here, not an input cap, so
  do not add `input: 272000`.
- A Codex-backed provider is a different allocation: see
  [Codex OAuth preset](#codex-oauth-preset) for the Codex-profile examples.
  Do not copy this API snippet's window onto a Codex provider.
- Supported API reasoning efforts are `low`, `medium`, `high`, `xhigh`, and
  `max`; select a configured variant with a ref such as `openai/gpt-6-astra@medium`.
  GPT-6 Astra has no `none` effort.
- Responses defaults `reasoning.summary` to `auto` while reasoning is active;
  set `reasoning.summary: none` when you do not want Chord to request a
  readable summary.

#### Compaction tuning for GPT-6 Astra

The base template above already carries the cost-first `compaction`
(0.25/0.2). The 272K pricing cliff is the hard constraint; the quality
ceiling is not: OpenAI reports GPT-6 Astra at 100% on MRCR v2 8-needle at
256K–512K and 96.3% at 512K–1M, a gentle slope rather than the cliff GPT-5.6
Sol hits (73.8% at 512K–1M). So Astra's threshold choice beyond cost-first is
a price/capacity tradeoff, not a quality-preservation one.

**Cost-first** (the base template, 0.25/0.2): fires at ~231K, under the 272K
cliff. Recommended default: the 2× repricing dwarfs any other lever, and
the trigger sits well inside the full-quality band.

**Quality-first / capacity-first** (accept the 2× long-context rate): because
Astra has no quality cliff before ~512K, you can push the threshold well past
where GPT-5.6 Sol would, while staying in a high-quality band. 0.6–0.7
(~553K–645K) buys a large window while keeping MRCR above 96%; 0.7–0.8
(~645K–738K) leans further into capacity at some quality cost. Override the
base template's `compaction`:

```yaml
model_templates:
  gpt-6-astra-quality: &gpt-6-astra-quality
    <<: *gpt-6-astra-base
    compaction:
      threshold: 0.65      # fires at ~600K; accepts the 2× long-context rate
```

Omit `reminder` to accept the value derived from `threshold`. On a
Codex-backed provider (its window is server-controlled and unverified for
Astra), put the `compaction` on that provider's model entry and tune the
threshold to the actual measured window, not the API full window.

## Codex OAuth preset

Use this when you want ChatGPT/Codex OAuth instead of API keys. Codex OAuth
differs from the API-key examples only in the provider preset and the
authentication method: the model windows match the API allocation.

The model allocations used in this section are:

| Model | `limit.context` | `limit.input` | `limit.output` |
| --- | ---: | ---: | ---: |
| GPT-6 Astra | 1,050,000 | 922,000 | 128,000 |
| GPT-5.4 | 1,050,000 | 922,000 | 128,000 |
| GPT-5.5 | 400,000 | 272,000 | 128,000 |
| GPT-5.6 Sol / Terra / Luna | 1,050,000 | 922,000 | 128,000 |

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
      gpt-6-astra:
        limit:
          context: 1050000
          input: 922000
          output: 128000
        variants:
          medium:
            reasoning:
              effort: medium
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
          max:
            reasoning:
              effort: max
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
          input: 922000
          output: 128000
      gpt-5.6-sol:
        limit:
          context: 1050000
          input: 922000
          output: 128000

model_pools:
  default:
    - codex/gpt-6-astra@medium
    - codex/gpt-5.5@xhigh
```

Authenticate with:

```bash
chord auth codex
```

Notes:

- Keep API-key and Codex OAuth providers separate when you use both because their credentials and model allocations differ.
- GPT-6 Astra is rolling out to Codex over the first weeks after launch (it
  requires Codex CLI 0.153.0 or newer) and its Codex subscription window is
  not published. The recipe uses the same `1050000 / 922000 / 128000`
  allocation as GPT-5.6 Sol as a conservative starting point; verify against
  your account's server catalog and adjust all three fields to the measured
  window before relying on it for long sessions.
- GPT-5.4, GPT-5.6 Sol / Terra / Luna, and GPT-6 Astra use the model-page
  allocation `1050000 / 922000 / 128000` (the 922K input budget derives as
  `context` minus `output`; these models publish no separate input cap). The
  API's >272K whole-request 2× pricing cliff still applies if your route bills
  that way. If the server catalog for your account/relay still serves the
  older profile, fall back to `400000 / 272000 / 128000`.
- These values track the current Codex model catalog and may change with a future Codex release. Update all three fields together when the backend allocation changes.

## Anthropic Claude

Pair with `~/.config/chord/auth.yaml`:

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
```

```yaml
model_templates:
  claude-opus: &claude-opus
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

providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-5: *claude-opus
      claude-opus-4.8: *claude-opus
      claude-opus-4.7: *claude-opus

model_pools:
  default:
    - anthropic/claude-opus-5@high
```

Claude Opus 5 / 4.8 / 4.7 share the same context window (1M), max output (128K), pricing, adaptive thinking, and input modalities, so all three reuse the single `&claude-opus` template; only the model ID differs. Remove the entries you don't use, and point `model_pools` at your preferred model (e.g. `anthropic/claude-opus-5@high`).

For a lower-cost Claude family config, use the same shape with `claude-sonnet-5`, `cost: {input: 2, output: 10}`, and `output: 64000` for a conservative local allocation. Sonnet 5's $2 / $10 per-1M pricing became permanent in August 2026.

For `claude-fable-5-1` (released September 2026), reuse the same shape: it shares the 1M context, 128K max output, adaptive thinking, and PDF support, and only the cost block differs. It keeps Fable 5's $10 / $50 per-1M input/output rates but cuts cache reads to $0.25 per 1M tokens (0.025x of base input instead of the standard 0.1x multiplier), so set `cache_read: 0.25`, not 1.0. `claude-fable-5` remains available with the same rates except cache reads at $1.0.

```yaml
# Requires the `&claude-opus` template above in the same file.
model_templates:
  claude-fable-5.1: &claude-fable-5-1
    <<: *claude-opus
    cost:
      input: 10
      output: 50
      cache_read: 0.25
      cache_write: 12.5
      cache_write_1h: 20

providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-fable-5-1: *claude-fable-5-1

model_pools:
  default:
    - anthropic/claude-fable-5-1@high
```

### Compaction tuning for Claude 5

The whole Claude 5 line (Fable 5.1, Opus 5, Sonnet 5) advertises 1M tokens with 128K output and flat per-token pricing across the window. MRCR v2 8-needle shows Opus-class models holding ~76% even at 1M (the flattest curve of any current family), so the reliable window is genuinely large. Opus 4.7-era models trade retrieval accuracy for refusal honesty; Opus 5 and Fable 5.1 restore strong long-context retrieval.

For everyday work, omit the `compaction` block and stay on the global default (`threshold` 0.8, about 698K on the ~872K usable budget); for many-hour agentic sessions, set `threshold: 0.7` (about 610K) to limit time spent deep in the mild 512K+ degradation band.

```yaml
model_templates:
  claude-fable-5-1: &claude-fable-5-1
    limit: {context: 1000000, output: 128000}
    cost:
      input: 10
      output: 50
      cache_read: 0.25
      cache_write: 12.5
      cache_write_1h: 20
    thinking: {type: adaptive, display: summarized}
    compaction: {threshold: 0.7}   # tuned for long agentic sessions

providers:
  anthropic:
    models:
      claude-fable-5-1: *claude-fable-5-1
      claude-opus-5: *claude-fable-5-1     # same profile; adjust cost block
```

`reminder` is omitted on purpose: the derived 0.60 is
a sensible pressure head start for these models; set it explicitly only when
you want the reminder earlier or later than the derived value.

Note the tokenizer change since Opus 4.7: the same text produces ~30% more
tokens on Claude 5 models than on older ones, so a context budget that felt
right on an older model should be scaled down accordingly.

## Thinking behind a Chat Completions gateway

A gateway can expose models on `/v1/chat/completions` and translate each call
into the upstream's native API. Chord's `thinking.*` keys are wire-independent,
but the gateway only reads the thinking controls in the shape its own
translation understands, so Chord writes them into the chat body as the dialect
the model name implies:

| Model | Field Chord adds | Built from |
| --- | --- | --- |
| Gemini | `extra_body.google.thinking_config` | `thinking.level`, `thinking.budget`, `thinking.include_thoughts` — snake_case keys, with the same budget/level rule and the same `include_thoughts` default as the native wire |
| Claude | `thinking: {type, budget_tokens}` | `thinking.type`, `thinking.budget`, `thinking.display` |
| DeepSeek, GLM, Kimi K2.x, Doubao | `thinking: {type}` | `thinking.type`, with `adaptive` mapped to `enabled` |
| Qwen | `enable_thinking` | `thinking.type`, `thinking.budget` |

A model that configures no thinking block sends nothing, and a model outside
these families keeps its thinking settings out of the chat body; name the
dialect explicitly for those, below.

```yaml
model_templates:
  gemini-flash: &gemini-flash
    limit: {context: 1048576, output: 65536}
    thinking:
      include_thoughts: true
    variants:
      high: {thinking: {level: high}}
      medium: {thinking: {level: medium}}
      low: {thinking: {level: low}}

  claude-chat: &claude-chat
    limit: {context: 200000, output: 64000}
    thinking: {type: enabled, budget: 8192}

  deepseek-chat: &deepseek-chat
    limit: {context: 1000000, output: 64000}
    reasoning: {effort: high}
    thinking: {type: enabled}

  glm-chat: &glm-chat
    limit: {context: 200000, output: 64000}
    thinking: {type: enabled}
    compat:
      # Family extras stay in the override; Chord merges them into the
      # thinking object it writes from the model-level block above.
      request_overrides:
        body:
          thinking: {clear_thinking: false}

providers:
  gateway:
    type: chat-completions
    api_url: https://example.com/v1/chat/completions
    models:
      gemini-3.8-flash: *gemini-flash
      claude-fable-5.1: *claude-chat
      deepseek-v4.1-flash: *deepseek-chat
      glm-5.2: *glm-chat

model_pools:
  default:
    - gateway/gemini-3.8-flash@high
    - gateway/claude-fable-5.1
    - gateway/deepseek-v4.1-flash
    - gateway/glm-5.2
```

- Nothing to configure: the field is built from the thinking knobs you already
  set, so one template works unchanged behind the gateway and on the model's
  native endpoint.
- A gateway that rejects unknown body fields instead of ignoring or translating
  them needs `compat.chat_completions.native_thinking: off`, set on the model or
  on the provider.
- A model name that hides the upstream (a gateway alias, a private deployment)
  names the shape directly: `native_thinking: gemini`, `gemini-3`, `anthropic`,
  `thinking`, or `qwen`. Use `gemini` when only the Gemini family is known;
  use `gemini-3` when the alias is known to target Gemini 3 and missing
  thought-signature repair is required. Family names such as `claude`,
  `deepseek`, `glm`, `kimi`, and `doubao` select the same shapes.
- Kimi K3 rejects the K2.x `thinking` parameter, so do not give it a model-level
  thinking block; K2.x models use the block as described above.
- `reasoning.effort` still goes out as the portable `reasoning_effort` field.
  Gateways that map effort themselves (Claude and Gemini ones usually do) work
  with it alone. On Google's own compatibility endpoint `reasoning.effort` and
  `extra_body.google.thinking_config` are mutually exclusive, so set one of
  them there, not both.
- Thought summaries come back as `reasoning_content` and display as thinking.
  Whether the upstream returns summary text at all still depends on the gateway
  and model; the level and budget apply to the request either way.

### Thinking state and signatures

A model that returns provider-bound replay state on its native API does the
same through the gateway, and expects it back on the next call. Chord carries it
in the shape the model's family uses:

| Family | Where the gateway returns it | What Chord sends back |
| --- | --- | --- |
| Gemini | one thought signature per step: `tool_calls[].extra_content.google.thought_signature` (Google's own compatibility endpoint), `tool_calls[].thought_signature`, or `provider_specific_fields.thought_signature` | `extra_content.google.thought_signature` on the step's first tool call |
| Claude | message-level `thinking_blocks` (the LiteLLM convention), also read from the `provider_specific_fields` mirror | the same `thinking_blocks` array on the assistant message |

These blobs are opaque and bound to the backend that produced them, so Chord
replays them to the same model family rather than over the same wire: a
signature captured on a Gemini endpoint is reused when the conversation
continues through a gateway, another provider, or the native API, and the
reverse holds too. A request that reaches a different family strips the blobs;
their readable text still goes out as portable thinking where the target accepts
it.

For a Chat Completions model alias, set `native_thinking` to `anthropic` or
`gemini` to identify its backend; use `gemini-3` when the alias is known to be
Gemini 3 and needs signature repair. Chord records that family with the response,
so saved sessions retain the replay identity even when the model name does not
identify it. Family checks apply before converting between wire formats. A
Gemini signature carried in a Messages thinking block is sent on the first
tool call when continuing over Chat Completions. Tool-call continuations retain
the configured Gemini thinking controls even without visible reasoning text.

Missing or rejected state is repaired instead of sent as a guaranteed failure:

- Gemini 3 rejects a function-call step that has no thought signature. When an
  assistant step after the last user message lost its signature (a model switch,
  a gateway that dropped it), Chord sends the documented placeholder
  `skip_thought_signature_validator`, which the backend accepts in place of a
  real signature. The repair needs to know the endpoint is Gemini 3: for a
  gateway alias that hides the upstream name, pin `native_thinking: gemini-3`.
  A family-only `gemini` pin does not assume a model version.
- A Claude-backed endpoint whose current turn no longer has replayable
  `thinking_blocks` is called without the `thinking` controls, matching the
  history the request carries; asking for reasoning the replayed history cannot
  back would only be rejected.
- A signature the backend does reject still escalates the replay compatibility
  ladder: Chord retries with the blobs stripped instead of failing the turn.

`native_thinking: off` also stops this state from being sent or replayed, which
matches an endpoint that rejects the native request fields.

Whether the gateway returns the state at all is still up to the gateway: a proxy
that drops `extra_content` or `thinking_blocks` leaves Chord nothing to replay.

## Google Gemini

Pair with `~/.config/chord/auth.yaml`:

```yaml
gemini:
  - "$GEMINI_API_KEY"
```

```yaml
model_templates:
  # Shared shape for Gemini 3.x Flash models: 1M window, `level`-controlled
  # thinking.
  gemini-flash: &gemini-flash
    limit:
      context: 1048576
      output: 65536
    modalities:
      input: [text, image, pdf]
    thinking:
      level: high

providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-3.8-flash: *gemini-flash

model_pools:
  default:
    - gemini/gemini-3.8-flash
```

Notes:

- Keep `api_url` at the `/models` base path. Chord appends `/{model}:streamGenerateContent?alt=sse` automatically.
- `type` can be omitted; Chord auto-detects Gemini from the `/models` path.
- Gemini 3.8 Flash (GA September 2, 2026) is the current workhorse: 1M-token context, 64K max output, and thinking levels `low` / `medium` (the provider default) / `high`. `minimal` is not supported and `thinking_budget` is deprecated, so the template above uses `level` only; it pins `high` for agentic work; dropping to `medium` or `low` cuts latency and token burn for everyday tasks.
- Gemini 3.5 / 3.6 Flash share this shape and also accept `minimal`; the Flash-Lite series defaults to `minimal`. Gemini 3.1 Pro takes `low` / `medium` / `high` and rejects `minimal` too, so do not reuse one `minimal` variant across the family.

### Compaction tuning for Gemini

Gemini's long-context behavior differs sharply by tier, so there is no single
compaction rule:

- **Gemini 3.1 Pro** has a genuinely weak multi-needle long context (public
  MRCR v2 8-needle retrieval lands around 0.26), so keep compaction aggressive:
  `threshold` ~0.2 and `reminder` ~0.15 of the usable budget (roughly 150K–210K
  on a 1M window).
- **Gemini 3.8 Flash / Flash-Lite** are built for the 1M window and hold up well
  at long context, so aggressive early compaction just discards context they can
  still use. Leave Flash at the global default (`threshold` 0.8) or omit the
  per-model block entirely. 3.8 Flash buys better accuracy with higher token
  consumption by design, so rising usage on long agentic runs is expected and it
  is not a signal to compact earlier.

```yaml
# Per-model Gemini compaction. Providers referencing the template inherit it.
model_templates:
  gemini-pro: &gemini-pro
    limit: {context: 1048576, output: 65536}
    compaction: {threshold: 0.2, reminder: 0.15}
    thinking:
      include_thoughts: true
    variants:
      high: {thinking: {level: "high"}}
      medium: {thinking: {level: "medium"}}
      low: {thinking: {level: "low"}}
    modalities: {input: [text, image, pdf]}
```

Pricing note: only **Gemini 3.1 Pro** steps up to the higher input tier above
200K tokens (the whole request is billed at the higher tier). Gemini 3.8 Flash
and Flash-Lite are flat-priced at any context length, so there is no cost reason
to compact Flash early; do it only if quality actually degrades for your
workload. If you need both a long reliable window *and* Pro-class quality, that is
the case where a GPT-5.6 Sol / Claude 5-class model is the better fit.

## GLM / BigModel Coding Plan

Pair with `~/.config/chord/auth.yaml`:

```yaml
bigmodel:
  - "$BIGMODEL_API_KEY"
```

The three templates below are the GLM-5.x family base: the `chat` template's
`thinking.type: enabled` + `clear_thinking: false` and `reasoning.effort`
values fall in the intersection of GLM-5.2 (dynamic thinking, effort
`max`/`xhigh`/…/`none`) and GLM-5.3 / 5.3-Flash (forced thinking, effort
`max`/`high`/`low`), so the same templates serve 5.2, 5.3, and 5.3-Flash
unchanged. The `glm-5.2-*` names reflect the model that introduced these
compat settings, not a 5.2-only restriction.

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
        preserve_history: true

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
      reasoning_continuity:
        mode: anthropic_unsigned

  glm-5.2-responses: &glm-5-2-responses
    limit:
      context: 1000000
      output: 128000
    reasoning:
      effort: max

  glm-5.3-chat: &glm-5-3-chat
    <<: *glm-5-2-chat
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      max:
        reasoning:
          effort: max

  glm-5.3-flash: &glm-5-3-flash
    <<: *glm-5-3-chat
    modalities:
      input: [text, image, pdf]

providers:
  bigmodel:
    type: chat-completions
    api_url: https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
    models:
      glm-5.2: *glm-5-2-chat
      glm-5.3: *glm-5-3-chat
      glm-5.3-flash: *glm-5-3-flash

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
    - bigmodel/glm-5.3-flash
```

Notes:

- Chat Completions requires `thinking.type: enabled`, `reasoning_effort`, and
  `max_tokens`. `request_overrides` adds GLM's thinking flags and renames the
  dynamically calculated output-limit field; `openai_visible` replays native
  `reasoning_content` and accepts portable visible reasoning from other wire
  families as `reasoning_content`.
- GLM-5.3 and 5.3-Flash support only `reasoning_effort` `low` / `high` / `max`
  (GLM-5.2 additionally accepts `xhigh` / `medium` / `minimal` / `none`), so
  the `glm-5.3-chat` template adds the three-effort `variants`; use
  `glm-5.3@low|high|max` refs for 5.3 and 5.3-Flash instead of the 5.2
  effort set.
- Messages-compatible endpoints use `thinking` plus `output_config.effort`.
  Disable Anthropic beta headers unless that endpoint documents support. A
  compatible Messages endpoint may return unsigned thinking rather than
  Claude-style signed blocks; do not infer signature replay support from the
  wire format alone. Configure `anthropic_unsigned` only after verifying that
  the endpoint accepts its own visible unsigned thinking in tool-call loops;
  once enabled, Chord can also map portable visible reasoning from other wire
  families into unsigned `thinking` blocks for that target.
- A GLM `/responses` endpoint is gateway-specific. Use a separate template with
  `reasoning.effort` only when the gateway documents OpenAI Responses mapping.
- GLM-5.3 (GA August 2026) keeps GLM-5.2's text-only specs (1M context, 128K
  max output), so it can reuse any of the GLM-5.2 templates above unchanged;
  only the model ID differs (e.g. `glm-5.3` in your provider's `models` map).
- GLM-5.3-Flash (released August 2026) is the family's first natively
  multimodal model: image/video/file input, with 1M context and 128K max output. PDF input is officially supported: the GLM Chat Completion API accepts a `file` content block whose `file` object takes `file_id`, `file_url`, or `file_data` (a Base64 `data:<MIME>;base64,...` URL), up to 50 MB per file, in `pdf`/`txt`/`word`/`jsonl`/`xlsx`/`pptx` formats. That matches Chord's chat-completions PDF payload exactly (`type: file` with `filename` and `file_data`), so no compatibility config is needed.

  Text parameters match GLM-5.3, so it derives from the Chat Completions template above and only adds the multimodal `modalities.input`. `thinking.type` supports `enabled` only (thinking cannot be turned off), which the chat template already sets. Third-party relays may only implement the older URL-only `file_url` form; check the relay before relying on Base64 `file_data`.
- The example default pool uses `glm-5.3-flash`: the Coding Plan workhorse
  with native multimodal input. For text-only work, point the pool at
  `bigmodel/glm-5.3`, or keep `bigmodel/glm-5.2` when you want GLM-5.2's wider
  effort set (`xhigh` / `medium` / `minimal` / `none`); the provider `models`
  map above still lists it as an available text model under the same templates.

### Compaction tuning for GLM-5.x

GLM-5.2/5.3 advertise a 1M window, but independent long-context evals put the
reliable working window of the open-weight GLM/Qwen-class models around
200K–256K (roughly 20–25% of the advertised 1M). If you run long exploratory
sessions on GLM, compact around a quarter of the usable budget:

```yaml
# Add compaction to the glm templates you already use (glm-5.2-chat /
# glm-5.2-messages / glm-5.3-chat ...); every model entry referencing the
# template inherits it.
model_templates:
  glm-5.2-chat: &glm-5-2-chat
    limit: {context: 1000000, output: 128000}
    compaction: {threshold: 0.25, reminder: 0.2}
```

GLM-5.2 is served by several providers in the recipes above (`bigmodel` chat,
`bigmodel-messages`, `glm-responses`); each model entry that references the
template gets the same `compaction`. If your workloads stay short, omit the
`compaction` block and let the model use the global default.

## DeepSeek

Pair with `~/.config/chord/auth.yaml`:

```yaml
deepseek:
  - "$DEEPSEEK_API_KEY"
```

`deepseek-flash` is DeepSeek-V4.1-Flash: 1M context, thinking enabled by
default, and native image input on all three wire families. On the official
API, the legacy IDs `deepseek-v4-flash` and `deepseek-v4-flash-vision-exp`
are still accepted and route here at Flash prices, and `deepseek-v4-pro`
requests will follow from 2026-09-14 04:00 UTC until V4.1-Pro ships.

```yaml
model_templates:
  deepseek-v4.1-chat: &deepseek-v4-1-chat
    limit:
      context: 1000000
      output: 64000
    modalities:
      input: [text, image]
    reasoning:
      effort: high
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      max:
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
        preserve_history: true
      forced_tool_choice:
        suppress_in_thinking: true

  deepseek-v4.1-messages: &deepseek-v4-1-messages
    limit:
      context: 1000000
      output: 64000
    modalities:
      input: [text, image]
    thinking:
      type: adaptive
      effort: high
    variants:
      low:
        thinking:
          effort: low
      high:
        thinking:
          effort: high
      max:
        thinking:
          effort: max
    compat:
      request_overrides:
        headers:
          anthropic-beta: null
      reasoning_continuity:
        mode: anthropic_unsigned
        preserve_history: true

  deepseek-v4.1-responses: &deepseek-v4-1-responses
    limit:
      context: 1000000
      output: 64000
    modalities:
      input: [text, image]
    reasoning:
      effort: high
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      max:
        reasoning:
          effort: max
    compat:
      responses:
        send_reasoning_include: false
        send_max_output_tokens: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

providers:
  deepseek:
    type: chat-completions
    api_url: https://api.deepseek.com/v1/chat/completions
    models:
      deepseek-flash: *deepseek-v4-1-chat

  deepseek-messages:
    type: messages
    api_url: https://api.deepseek.com/anthropic/v1/messages
    models:
      deepseek-flash: *deepseek-v4-1-messages

  deepseek-responses:
    type: responses
    api_url: https://api.deepseek.com/v1/responses
    models:
      deepseek-flash: *deepseek-v4-1-responses

model_pools:
  default:
    - deepseek/deepseek-flash@high
```

Notes:

- DeepSeek Chat thinking uses `thinking.type`, top-level `reasoning_effort`, and
  `max_tokens`. `request_overrides` supplies the request-shape differences; during thinking + tool-call loops, `openai_visible` returns the assistant's `reasoning_content` unchanged. When a request carries tools, DeepSeek requires the full `reasoning_content` back in every later turn and returns a `400` otherwise, so the templates set `preserve_history: true` to keep completed-turn reasoning client-side; without tools the field is ignored.

  DeepSeek also rejects forced tool choice while thinking is active, so the template downgrades loop-forced `tool_choice: required` to the backend default for those requests.
- DeepSeek Responses supports `tool_choice: required`, so its template keeps
  loop-forced tool choice. Plaintext `reasoning_text` makes the encrypted
  reasoning include unnecessary, while `max_output_tokens` remains enabled
  because the endpoint supports it. Fields the endpoint does not support
  (`store`, `background`, `previous_response_id`, …) are silently ignored, and
  the stream ends with a `response.completed` / `incomplete` / `failed` event
  instead of `data: [DONE]`.
- DeepSeek Messages supports `output_config.effort`; Chord derives it from
  `thinking.effort`. Disable Anthropic beta headers for the compatible endpoint; it ignores them outside the Files API. `thinking.budget_tokens` is accepted but ignored: thinking depth comes from the effort value, not from a token budget. DeepSeek's Anthropic-compatible endpoint may return unsigned `thinking` blocks rather than Claude-style signed blocks.

  `anthropic_unsigned` replays same-provider/model unsigned thinking natively and can also accept portable visible reasoning from other wire families as unsigned `thinking` blocks; if the target still rejects that shape, strict compatibility drops the reasoning carrier while preserving the tool round.
- All three wire families accept images, billed as input tokens (the official
  cap is 1024 tokens per image). The endpoint takes inline base64, external
  URLs, or Files API `file_id`s, detects the format by content
  (JPEG / PNG / GIF / WebP), and accepts images only in user messages; an
  image in a system or assistant message returns a `400`. Request limits are
  ≤48 MiB per body, ≤600 images per request, ≤64 MiB of images per request
  (≤200 MiB when `file_id`s are used), and ≤8192 px per side (4096 px once a
  request carries 15 images or more). Chord always sends inline base64, so
  external URLs and `file_id`s are not reachable from inside Chord.
  - `detail` on `image_url` (Chat) or `input_image` (Responses) accepts `low`,
    `high`, `original` (the same as `high`), or `auto`; Chord sends `auto` on
    the Responses provider, omits it on Chat, and exposes no per-request
    configuration.
  - The [`view_image`](./tools.md) tool can load local images into context, but
    only when this model heads the active pool on the `messages` or `responses`
    provider: the `chat-completions` (`deepseek`) provider accepts images in
    user messages yet cannot carry them back in tool results.
- Treat third-party `/responses` endpoints as gateway-specific; use
  `reasoning.effort` and `openai_visible` only when the gateway documents its
  mapping.
- For compatible gateways, use the exact model ID and limits published by that
  gateway/account. See [Troubleshooting: DeepSeek / OpenAI-compatible thinking-mode 400s](./troubleshooting.md#deepseek--openai-compatible-thinking-mode-400s).

Additional notes:

- The official pricing page lists a maximum output of 384K; `limit.output:
  64000` here is a conservative local allocation. Raise it as needed for
  longer outputs.
- `reasoning_effort` (Chat) and `output_config.effort` (Messages) accept `low`
  / `high` / `max`, and Responses `reasoning.effort` also accepts `none` to
  turn thinking off; the default is `high`. Other values are remapped by the
  backend: `medium` and `xhigh` map to `high`, which is why the templates only
  define the `low` / `high` / `max` variants.
- The Responses API lives at `api.deepseek.com/v1/responses`, and its
  `output_tokens_details.reasoning_tokens` field is handled by Chord's standard
  reasoning replay without extra configuration.
- Flash pricing per 1M tokens (off-peak | peak): cache hit $0.003 | $0.006,
  cache miss $0.15 | $0.30, output $0.60 | $1.20. Peak hours are 01:00–04:00
  and 06:00–10:00 UTC on weekdays. See
  [DeepSeek official pricing](https://api-docs.deepseek.com/quick_start/pricing/).

### Gateways still serving the V4 generation

Some providers keep serving the V4-generation weights under the
`deepseek-v4-flash` / `deepseek-v4-pro` IDs. Those models are text-only:
Flash and Pro reject images with a `400` (only the experimental
`deepseek-v4-flash-vision-exp` accepted them), so their entries must not
declare the `image` modality. Reusing `*deepseek-v4-1-chat` directly would
inherit it; override `modalities` or build their templates text-only:

```yaml
# Gateways still serving the V4-generation models: same templates, text only.
model_templates:
  deepseek-v4-chat: &deepseek-v4-chat
    <<: *deepseek-v4-1-chat
    modalities:
      input: [text]

providers:
  deepseek-gateway:
    type: chat-completions
    api_url: https://example.com/v1/chat/completions
    models:
      deepseek-v4-flash: *deepseek-v4-chat
```

### Compaction tuning for DeepSeek V4.1 Flash

DeepSeek V4.1 Flash advertises a 1M window, but long-range reliability is the family's weak spot: independent multi-needle evals of the previous V4 generation put V4 Pro around ~41% at 1M (8-needle) versus ~78% single-needle, a sharp drop that mirrors the Gemini 3.1 Pro cliff. V4.1 has no public long-context evaluation yet, so until one appears the practical guidance stays the same: treat the reliable working window as roughly 200K and compact early.

The Flash family is the cheapest by a wide margin even on cache misses, so frequent compaction is far cheaper than on premium models; compact early and often:

```yaml
# Add compaction to the deepseek-v4.1-chat / -messages / -responses templates
# in the recipes above; every model entry that references them inherits it.
model_templates:
  deepseek-v4.1-chat: &deepseek-v4-1-chat
    limit: {context: 1000000, output: 128000}
    compaction: {threshold: 0.25, reminder: 0.2}
```

DeepSeek's cache-hit rate is the best in the industry ($0.003/M), so a
compaction that preserves the cacheable prefix is nearly free on repeated
reads. Keep short interactive sessions on the global default and only tune the
model entry when you run genuinely long agentic runs.

## Qwen preserved thinking

Qwen returns visible reasoning through `reasoning_content`, but most models
ignore that field in history by default. Only enable replay on a model that
documents `preserve_thinking` support: currently Qwen 3.8 Max; 3.7 Max, Plus,
and Flash; and 3.6 Max preview and Plus, dated snapshots included. Check the
official list for your model: older Qwen 3/3.5 models may still emit reasoning
but should leave continuity disabled.

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
        preserve_history: true

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
reasoning counts as input tokens and billing when `preserve_thinking` is true;
`preserve_history: true` keeps Chord from stripping that history client-side.

## Kimi

Kimi K3 is the current flagship thinking model. It has a 1M-token context,
always reasons, and accepts `reasoning_effort: low`, `high`, or `max` (default
`max`); switching effort mid-session invalidates the prefix cache, so keep it
stable within a conversation. K3 requires the complete assistant message
(including `reasoning_content`) in multi-turn conversations and tool-call
loops. Do not pass the K2.x `thinking` parameter or sampling fields: K3 fixes
`temperature` at 1.0, `top_p` at 0.95, and the penalties at 0.

```yaml
model_templates:
  kimi-k3: &kimi-k3
    limit:
      context: 1048576
      output: 131072
    reasoning:
      effort: max
    compat:
      chat_completions:
        mcp_system_tools_message: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

  kimi-k2.7-code: &kimi-k2-7-code
    limit:
      context: 262144
      output: 32768
    compat:
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

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
        preserve_history: true

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

`mcp_system_tools_message` is an explicit model capability, not a model-name
guess. It lets runtime manual-MCP declarations stay at fixed conversation
anchors instead of rewriting the top-level tool list. Keep it off for gateways
that do not accept a `role: system` message containing `tools` without
`content`; mixed fallback pools automatically use the common top-level shape.

K2.7 Code is the 256K coding-specialized, thinking-only option; its thinking
mode and `keep: all` behavior are fixed, so the template does not send a
`thinking` object. K2.6 is the 256K general-purpose hybrid option and therefore
sets both fields explicitly. K2.5 does not support preserved thinking and is
being retired for new users; prefer K3 for new configurations.

For all `openai_visible` recipes (DeepSeek, GLM, supported Qwen, and Kimi), Chord first replays native reasoning optimistically to any Chat Completions target, so documented in-provider upgrades such as Kimi K2.6/K2.7 to K3 and same-model provider fallback can keep continuity.

Recipes for backends whose tool-mode contract requires the full reasoning history (DeepSeek) and preserved-thinking recipes (GLM `clear_thinking: false`, Qwen `preserve_thinking`, Kimi K3 / `keep: all`) set `preserve_history: true` so the complete assistant history is replayed unchanged. If a target rejects native reasoning, Chord removes or converts only the incompatible reasoning payload.

Completed tool calls and their paired results remain available to the next model; they are not treated as disposable chain-of-thought data. A strict compatibility fallback may textify the completed action history when the target cannot accept the structured shape.

### Cross-protocol fallback continuity

When a pool switches between Chat Completions, Responses, Messages, or Gemini,
Chord preserves the portable parts of completed tool rounds:

- completed calls and paired results are converted to the target protocol's
  structured tool representation whenever possible;
- visible reasoning attached to a tool round (`reasoning_content`, unsigned
  thinking text, Responses reasoning summaries, or Gemini thought text) is
  converted only when the target exposes a structured reasoning carrier
  (`openai_visible` or `anthropic_unsigned`); otherwise it is dropped rather
  than injected into assistant-visible text;
- opaque provider state such as Claude signatures, Responses encrypted
  reasoning, and Gemini thought signatures is never fabricated or copied into
  an incompatible protocol;
- if a target rejects the synthesized structured shape, strict compatibility
  textifies the completed call/result history instead of silently deleting it.

Reasoning-only turns are not copied as fallback text. This keeps cross-protocol
context focused on action-relevant state and avoids paying repeatedly for old
chain-of-thought that is not tied to a tool round.

## Grok (xAI)

xAI recommends the Responses API for Grok. Grok 4.7 supports text and image input, function calling, structured output, reasoning, and a 500K context window. xAI also accepts PDF attachments as `input_file` with a public `file_url` or an uploaded `file_id`, which activates the server-side `attachment_search` tool; Chord sends PDF attachments as inline base64 `file_data`, which the xAI Responses API does not accept for non-image documents, so `modalities.input` stays `[text, image]`.

Grok 4.7 emits reasoning text through `response.reasoning_text.*` stream events and summarized reasoning through `response.reasoning_summary_text.*`; Chord maps both to the normal thinking stream while preserving the ordered Responses output items for tool-loop continuity. The Responses API always returns `reasoning.encrypted_content` for Grok 4.7, so the reasoning items Chord replays unchanged keep the model's reasoning across turns.

```yaml
model_templates:
  grok-4.7: &grok-4-7
    limit:
      context: 500000
    reasoning:
      effort: high
    variants:
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]
    cost:
      input: 2
      output: 6
      cache_read: 0.5
      input_tiers:
        - above_input_tokens: 200000
          input: 4
          output: 12
          cache_read: 1

providers:
  xai:
    type: responses
    api_url: https://api.x.ai/v1/responses
    models:
      grok-4.7: *grok-4-7

model_pools:
  default:
    - xai/grok-4.7
```

xAI publishes a 500K total context window for Grok 4.7, but not a lower,
separate model output cap, so `limit.output` is unset. Chord therefore
does not send `max_output_tokens` to xAI and lets the API fit output within the
remaining context. Locally, Chord still reserves its default `64000` output
budget when deriving the input budget from `limit.context`. Set `limit.output`,
raise Chord's `max_output_tokens`, and enable
`compat.responses.send_max_output_tokens: true` only when you intentionally
want to enforce and send an explicit cap.

The `cost` block models xAI's whole-request tier: a prompt that reaches 200K
tokens bills every token in the request at the higher rate, which
`input_tiers` reproduces in Chord's cost accounting.

Use `grok-4.7` as the model ID, or switch the pool ref to `@low` / `@xhigh`
for a fixed effort. Grok 4.6 is still served at the same price and uses the
same template with only the model ID swapped. Do not configure
`openai_visible`: xAI Responses uses native ordered output/reasoning state, not
Chat Completions `reasoning_content`. `reasoning.effort` accepts `low`,
`medium`, `high`, and `xhigh` (Grok 4.6 and later; older models treat `xhigh`
as `high`). High is the default and reasoning cannot be disabled.

### Chat Completions

xAI still serves Grok 4.7 on the OpenAI-compatible `/v1/chat/completions`
endpoint, but now describes that API as the legacy predecessor of Responses
and steers new integrations to Responses; it stays documented for gateways
and existing integrations. The wire accepts `reasoning_effort` (`low`,
`medium`, `high` default, `xhigh`), rejects `stop`, `presence_penalty`, and
`frequency_penalty` on reasoning models, and deprecates `max_tokens` in favor
of `max_completion_tokens`.

xAI's own Chat Completions API returns no reasoning content for reasoning
models, and third-party gateways differ in the same way. When nothing comes
back, Chord has nothing to replay on assistant tool calls, reads the backend
as replay-incompatible, and strips `reasoning_effort` for the rest of the
turn, so per-request effort tuning then only affects the first request.
Set `compat.chat_completions.keep_reasoning_effort: true` to keep the effort
and reasoning request overrides active for the whole turn:

```yaml
model_templates:
  grok-4.7: &grok-4-7
    limit:
      context: 500000
      output: 64000
    reasoning:
      effort: high
    compat:
      chat_completions:
        keep_reasoning_effort: true
    variants:
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

providers:
  grok-gateway:
    type: chat-completions
    api_url: https://example.com/v1/chat/completions
    models:
      grok-4.7: *grok-4-7

model_pools:
  default:
    - grok-gateway/grok-4.7@xhigh
```

`openai_visible` is still unnecessary: Grok does not require a replayed
`reasoning_content` contract. Cache hits depend on sticky routing: xAI accepts
a `prompt_cache_key` on both `/v1/responses` and Chat Completions and routes it
through `x-grok-conv-id`, so a gateway that forwards neither re-sends every
request as a cache miss.

### Compaction tuning for Grok 4.7

Grok 4.7 has a whole-request pricing tier at 200K prompt tokens: below 200K
the rates are $2 input / $0.50 cached input / $6 output per 1M, while a prompt
that reaches 200K bills the whole request at $4 / $1 / $12.

The recipes above omit `limit.output`, so Chord reserves its default `64000`
output budget and the usable input budget derives as roughly
`500000 − 64000 = 436000`. A `threshold` of 0.4 fires at ~174K, under the 200K
tier with headroom for a single large tool result pushing the next prompt past
the line (the trigger compares the last provider-reported usage with the
budget, so staying under the tier is a tuning goal, not a guarantee; the same
caveat as the GPT pricing tiers). Add it to whichever Grok template you use;
every provider referencing the template inherits it:

```yaml
model_templates:
  grok-4.7: &grok-4-7
    limit: {context: 500000}
    compaction: {threshold: 0.4, reminder: 0.35}
```

The derived value for this threshold is 0.36, so the explicit
0.35 only pulls the pressure notice slightly earlier. If your sessions stay
short, omit the `compaction` block and let the model use the global default.

## MiniMax (OpenAI-compatible)

Pair with `~/.config/chord/auth.yaml`:

```yaml
minimax:
  - "$MINIMAX_API_KEY"
```

The OpenAI-compatible endpoint is `https://api.minimax.io/v1/chat/completions`.
`MiniMax-M3` is the multimodal flagship with a 1M-token window; the M2.x line
(`MiniMax-M2.7`, `MiniMax-M2.5`, `MiniMax-M2.1`, `MiniMax-M2`, and their
`-highspeed` variants) is text-only with a 204,800-token window. Thinking is on
by default on M3 and always on for M2.x; only M3 accepts
`thinking: {type: disabled}` to skip it.

```yaml
model_templates:
  minimax-m3: &minimax-m3
    limit:
      context: 1000000
    modalities:
      input: [text, image]

  minimax-m2x: &minimax-m2x
    limit:
      context: 204800
    modalities:
      input: [text]

providers:
  minimax:
    type: chat-completions
    api_url: https://api.minimax.io/v1/chat/completions
    models:
      MiniMax-M3: *minimax-m3
      MiniMax-M2.7: *minimax-m2x

model_pools:
  default:
    - minimax/MiniMax-M3
```

MiniMax documents the context windows but no output cap, so the templates leave
`limit.output` unset and inherit the global cap. M3 also takes video input;
Chord's chat wire sends text and images only.

By default the API returns thinking inside the assistant `content` field,
wrapped in tags, and asks for that content to be preserved completely. Chord
replays assistant content verbatim, so this shape needs no extra configuration;
thinking shows up as ordinary assistant text rather than in the reasoning
display.

To move thinking onto the reasoning channel, set `reasoning_split: true`: the
API then returns `reasoning_content` alongside `reasoning_details`. Chord parses
and can replay `reasoning_content`, but `reasoning_details` has no Chord
counterpart, and MiniMax asks for both to be preserved. Use the split shape only
when your endpoint accepts plain `reasoning_content` replay:

```yaml
# Structured reasoning: MiniMax moves thinking to reasoning_content.
model_templates:
  minimax-m3-split: &minimax-m3-split
    <<: *minimax-m3
    compat:
      request_overrides:
        body:
          reasoning_split: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true
```

### Compaction tuning for MiniMax M3

MiniMax-M3 doubles its rates above 512K input tokens: calls with ≤512K input
bill at the standard rate, calls above 512K at the higher long-context rate;
cache reads double too.

The M3 template sets no `limit.output`, so Chord reserves its default `64000`
output budget and the usable input budget derives as roughly
`1000000 − 64000 = 936000` (`512000 / 936000 ≈ 0.55`). A `threshold` of 0.5
fires at ~468K, under the 512K rate with headroom; the trigger compares the
last provider-reported usage with the budget, so a single large tool result
can still push the next prompt past the line. Add it to the M3 template you
already use:

```yaml
model_templates:
  minimax-m3: &minimax-m3
    limit: {context: 1000000}
    compaction: {threshold: 0.5, reminder: 0.45}
```

The derived value for this threshold is 0.45; the explicit
value only states the default. The M2.x line (204800 window) has no documented
length surcharge, so leave it on the global default. If your M3 sessions stay
short, omit the `compaction` block entirely.

## Xiaomi MiMo (OpenAI-compatible)

Pair with `~/.config/chord/auth.yaml`:

```yaml
mimo:
  - "$MIMO_API_KEY"
```

MiMo-V2.6-Pro and MiMo-V2.6-Flash are Xiaomi's fully multimodal agentic models on the MiMo Open Platform: a 1,048,576-token context window, a 131,072-token maximum output (the endpoint's default and cap for `max_completion_tokens`), image input, function calling, structured output, and deep thinking that is on by default. The platform is OpenAI- and Anthropic-compatible; this recipe uses `https://api.xiaomimimo.com/v1/chat/completions` because that is where MiMo documents the `reasoning_content` replay contract Chord needs for tool loops.

Thinking mode carries a hard replay contract: in multi-turn tool calls the API expects every earlier `reasoning_content` back and reports `400 - Invalid Format` when it is missing, so the template enables `openai_visible` with `preserve_history: true`. The thinking switch is a `thinking: {type: ...}` object, which Chord only emits when the model pins the Chat Completions dialect (`native_thinking: thinking`); `mimo-*` is not one of the model names Chord infers a dialect from.

```yaml
model_templates:
  mimo-v2.6-base: &mimo-v2-6-base
    limit:
      context: 1048576
      output: 131072
    modalities:
      input: [text, image]
    thinking:
      type: enabled
    variants:
      off:
        thinking:
          type: disabled
    compat:
      chat_completions:
        native_thinking: thinking
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

providers:
  mimo:
    type: chat-completions
    api_url: https://api.xiaomimimo.com/v1/chat/completions
    models:
      mimo-v2.6-pro:
        <<: *mimo-v2-6-base
        cost:
          input: 0.435
          output: 0.87
          cache_read: 0.0036
      mimo-v2.6-flash:
        <<: *mimo-v2-6-base
        cost:
          input: 0.14
          output: 0.28
          cache_read: 0.0028

model_pools:
  default:
    - mimo/mimo-v2.6-pro
```

Notes:

- `mimo-v2.6-pro` takes complex, long-horizon work; `mimo-v2.6-flash` is the cheaper everyday option with the same window, modalities, and limits. `mimo-v2.6-pro-ultraspeed` is the same model on a faster serving tier, sold as a customized service.
- Thinking is on by default. `mimo/mimo-v2.6-pro@off` sends `thinking: {type: disabled}` for turns that do not need it. The platform also forces `temperature` to 1.0 and `top_p` to 0.95 in thinking mode, which Chord does not send anyway.
- `preserve_history: true` keeps completed-turn reasoning in the replayed conversation because the API requires it; that history is billed as input tokens on every request, which MiMo's prompt cache absorbs at the cache-read rate ($0.0036 per 1M on Pro).
- Cache writes are currently free, and `cache_write` has no way to express that: leaving it unset bills estimated writes at the input rate, so cost estimates for cache-heavy sessions run slightly high.
- Prices are $0.435 input / $0.87 output / $0.0036 cached input per 1M on Pro and $0.14 / $0.28 / $0.0028 on Flash, with no long-context surcharge.
- The backend silently drops any `tool_choice` other than `auto`; add `compat.forced_tool_choice: {auto_only: true}` if you want Chord to stop sending a forced choice to this provider.
- `max_completion_tokens` covers visible output and reasoning tokens together, so a long thinking run counts against Chord's `64000` default output budget. Raise the global `max_output_tokens` or the model's `limit.output` (up to 131072) when thinking-heavy work gets truncated.
- The platform accepts both an `api-key` header and bearer auth; Chord defaults to bearer, and `auth_scheme: api-key` switches to the header when an endpoint or key type rejects it.
- The platform does not document `stream_options`. If a streaming request is rejected, set `compat.chat_completions.send_stream_options: false`; MiMo's stream chunks carry usage, so the usage-driven compaction trigger keeps working.
- No `compaction` block: MiMo publishes no long-context pricing tier and no long-context quality band, so the model uses the global default. The derived input budget is `1048576 − 131072 = 917504`.

Verify:

```bash
chord doctor models --model mimo/mimo-v2.6-pro
```

## Meta Muse Spark

Pair with `~/.config/chord/auth.yaml`:

```yaml
meta:
  - "$MODEL_API_KEY"
```

Muse Spark 1.3 is Meta's agentic and coding model on Meta Model API: a
1,048,576-token context window, an output cap of 131,072 in Meta's reference
configuration, text / image / PDF input (the API also takes video and audio,
which Chord's Responses wire cannot send), and always-on reasoning with
`minimal` / `low` / `medium` / `high` / `xhigh` / `max` effort. Use the
Responses endpoint: among the three compatible surfaces it is the only one that
carries the model's reasoning across turns, which Chord replays as encrypted
reasoning items.

```yaml
model_templates:
  muse-spark-1.3: &muse-spark-1-3
    limit:
      context: 1048576
      output: 131072
    reasoning:
      effort: high
      summary: auto
    variants:
      minimal:
        reasoning:
          effort: minimal
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
      max:              # Standard tier only
        reasoning:
          effort: max
    modalities:
      input: [text, image, pdf]

providers:
  meta:
    type: responses
    api_url: https://api.meta.ai/v1/responses
    models:
      muse-spark-1.3: *muse-spark-1-3

model_pools:
  default:
    - meta/muse-spark-1.3@xhigh
```

Notes:

- No `compat` block is needed. Responses providers already send
  `include: ["reasoning.encrypted_content"]` with `store: false`, Meta's
  recommended stateless-replay pairing, and Chord replays reasoning items with
  an explicit `summary` field, which Meta requires. `prompt_cache_key` is sent
  by default and supported; `client_metadata` is accepted and ignored.
- Muse Spark always reasons, so `reasoning.effort: none` returns `HTTP 400`:
  do not add a `none` variant. `max` is available on the Standard tier only.
- The pool starts at `@xhigh`; switch to `@max` for maximum reasoning, or drop
  to `@medium` / `@low` for faster everyday work.
- `muse-spark-1.3-contributor` serves the same model much cheaper in exchange
  for letting Meta train on your prompts and completions. Configure it only
  where that tradeoff is acceptable, and note that it has no `max` effort.
- `limit.output` follows Meta's reference configuration (`131072`). Chord does
  not send `max_output_tokens` on Responses by default; set
  `compat.responses.send_max_output_tokens: true` when you want Chord to
  enforce the cap explicitly.
- Meta's launch benchmarks report near-flat long-context retrieval (MRCR v2
  8-needle 98.5 at 256K–512K and 98.1 at 512K–1M), so there is no documented
  quality cliff to compact under; the global compaction threshold applies.
- A Messages-compatible endpoint (`https://api.meta.ai/v1/messages`) exists for
  Anthropic-format clients; this recipe documents the Responses path. Chat
  Completions is not recommended for agentic work because it does not carry
  reasoning across turns.

Verify:

```bash
chord doctor models --model meta/muse-spark-1.3@xhigh
```

## Verify any recipe

After copying a recipe, run one targeted check first:

```bash
chord doctor models --model provider/model
```

Then verify the exact variant you plan to use, for example:

```bash
chord doctor models --model openai/gpt-5.6-sol@xhigh
chord doctor models --model codex/gpt-5.5@xhigh
chord doctor models --model anthropic/claude-opus-5@high
```

## Per-model compaction tuning

**Per-model compaction tuning.** Every recipe below is a `model_pools` /
`providers` recipe for wiring up the model. To tune context
auto-compaction per model, add a `compaction` block to the model's own
definition or template (see [Context compaction](./context-management.md#context-compaction)):

```yaml
model_templates:
  luna-full-window: &luna-full-window
    limit: {context: 1050000, output: 128000}   # full API window: no `input`
    compaction: {threshold: 0.25, reminder: 0.2}   # stay under the 272K long-context pricing tier

providers:
  openai:
    models:
      gpt-5.6-luna: *luna-full-window
```

A model without a `compaction` block inherits the global `context.compaction.threshold`; `reminder` is derived from `threshold` when unset ([derivation and tuning guidance](./context-management.md#context-compaction)); `reminder: -1` disables the pressure reminder for the model while keeping its automatic compaction. These fields tune the usage-driven automatic-compaction path and take effect whether or not `model_driven` is enabled.

Where the benchmark evidence below gives a recommended usage band for a model, tune its `threshold` to the *top* of that band (compaction keeps the context inside it) and optionally set `reminder` just below it. When a model's long-context reliability is not documented here, omit the `compaction` block and let it use the global default.
