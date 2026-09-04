# Model configuration recipes

Use this page when you already know which provider/model family you want and just need a copy-paste-ready starting point. Keep [Configuration & Auth](./configuration.md) for field semantics and full schema details; use [Examples](./examples/index.md) for full multi-file workstation/team layouts.

> **Per-model compaction tuning.** Every recipe below is a `model_pools` /
> `providers` recipe for wiring up the model. To tune context
> auto-compaction per model, add a `compaction` block to the model's own
> definition or template (see [Context compaction](./context-management.md#context-compaction)):
>
> ```yaml
> model_templates:
>   luna-full-window: &luna-full-window
>     limit: {context: 1050000, output: 128000}   # full API window: no `input`
>     compaction: {threshold: 0.25, reminder: 0.2}   # stay under the 272K long-context pricing tier
>
> providers:
>   openai:
>     models:
>       gpt-5.6-luna: *luna-full-window
> ```
>
> A model without a `compaction` block inherits the global
> `context.compaction.threshold`; `reminder` defaults to
> `min(0.60, threshold × 0.90)` when unset; `reminder: -1` disables the
> pressure reminder for the model while keeping its automatic compaction.
> These fields tune the usage-driven
> automatic-compaction path and take effect whether or not `model_driven` is
> enabled. Where the benchmark evidence below gives a recommended usage band
> for a model, tune its `threshold` to the *top* of that band (compaction keeps
> the context inside it) and optionally set `reminder` just below it. When a
> model's long-context reliability is not documented here, omit the
> `compaction` block and let it use the global default.


## OpenAI Responses-compatible: GPT-5.4 / GPT-5.5 / GPT-5.6

The GPT-5.6 snippets use the conservative Codex/common-relay allocation by
default (`400000` context / `272000` input / `128000` output), because many
Responses relays expose Codex-backed limits rather than the full OpenAI API
window. If your account or gateway explicitly supports the full GPT-5.6 API
window, the notes below show how to opt in to 1.05M context manually. The cost
blocks use OpenAI API pricing; override them when your relay charges different
rates. Codex OAuth has a separate preset block below. Pair API-key providers
with the matching entry in
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

The three 5.6 tiers share the same window, reasoning, variants, and
modalities, so a common `&gpt-5-6-base` anchor carries those; each tier only
adds its own `cost` block (permanent list prices; temporary promotions are not
maintained here).

```yaml
model_templates:
  gpt-5.6-base: &gpt-5-6-base
    # Codex subscription profile as of 2026-09: 872K input + 128K output = 1M
    # (max_context_window=872000). Fall back to 400000/272000/128000 when
    # your account/relay still serves the older profile; for the official
    # OpenAI API use the full-window template below.
    limit:
      context: 1000000
      input: 872000
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
      input: [text, image, pdf]

providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6:
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
```

Notes:

- The GPT-5.6 examples use `1000000 / 872000 / 128000` for Codex-backed
  accounts and relays: the 2026-09 server catalog sets
  `max_context_window = 872000`, and Codex counts context as input + output,
  so 872K + 128K = 1M (the documented `model_context_window: 1000000`). If
  your account/relay still serves the older profile, fall back to
  `400000 / 272000 / 128000`.
- For the official OpenAI API, or a gateway confirmed to expose the full API
  window, change `context` to `1050000` and remove `input`. Chord then derives
  the usable input budget as `context` minus the model's `output` (128000 →
  922000); the `64000` output-cap default is only reserved for models that
  declare no `limit.output`. Do not keep `input: 272000`: above 272K is the
  long-context pricing threshold, not the full API input cap.
- `gpt-5.6` currently resolves to Sol, so its `cost` block should match Sol pricing.
- GPT-5.6 API reasoning efforts can include `none`, `low`, `medium`, `high`, `xhigh`, and `max`.
- Responses defaults `reasoning.summary` to `auto` while reasoning is active; set `reasoning.summary: none` when you do not want Chord to request a readable summary.
- Chord does not currently expose GPT-5.6 `reasoning.mode: pro`.

Verify:

```bash
chord doctor models --model openai/gpt-5.6@max
```

#### Compaction tuning for GPT-5.6

Start from two separate questions: **which window the model runs in** — the
272K-input Codex/relay allocation or the full 1.05M API window — and **what
the threshold is for**: keeping quality up, staying under the 272K
long-context pricing tier, or using the window for raw capacity. The same
ratio fires at very different token counts under the two windows, so a recipe
tuned for one does not transfer to the other.

**Long-context quality** (MRCR v2 8-needle results as reported by OpenAI):
Sol/Terra stay strong in the 256K–512K band (91.5% / 89.6%) and drop to ~73%
(73.8% / 72.5%) in the 512K–1M band, while Luna sits at 41.3% in both — a
cliff, not a slope. The bands are averages, so treat them as a broad guide
for where quality starts slipping, not as an exact cliff location.

**Pricing** (official OpenAI API): a prompt that exceeds 272K input tokens
(exactly 272000 does not) bills the **entire request** at the long-context
rates — 2x input / cache-read / cache-write and 1.5x output, not just the
portion above 272K. Relays and Codex OAuth set their own prices, so this tier
does not necessarily apply there. Chord's cost accounting selects the tier
from the full prompt, but automatic compaction does not know about price
tiers: it fires on a usage ratio, so keeping requests under 272K is a tuning
goal, not a guarantee. The trigger compares the last provider-reported usage
with the budget, a single large tool result can push the next prompt past the
line, and while `model_driven` is enabled the grace period lets crossing
requests run before compaction starts. Leave headroom below the line — and
note that every compaction costs a summarization call and loses raw context,
so compressing too eagerly can cost more than the tier it avoids.

##### Full API window (1.05M)

Remove `input`; the usable input budget then derives as `context` minus the
model's declared `limit.output` — 922K for the 1.05M/128K template below,
regardless of the 64K default request-output cap (that default is reserved
only when the model declares no `limit.output`). The 272K pricing line is
roughly 29% of that budget, which is why the cost-first answer sits in the 0.2
range, not a typo.

Cost-first (Sol/Terra/Luna share this: it keeps usage under the 272K tier
and below Luna's 256K+ collapse zone):

```yaml
model_templates:
  gpt-5.6-full-cost: &gpt-5.6-full-cost
    <<: *gpt-5-6-base
    limit:
      context: 1050000      # full API window: `input` removed
      output: 128000
    compaction:
      threshold: 0.25       # fires at ~231K–247K, under the 272K tier
      reminder: 0.2
```

Quality-first (Sol/Terra; Luna has no strong long-context band to aim for):

```yaml
model_templates:
  gpt-5.6-sol-quality: &gpt-5.6-sol-quality
    <<: *gpt-5-6-base
    limit:
      context: 1050000
      output: 128000
    compaction:
      threshold: 0.55       # fires at ~507K–542K; 0.5–0.65 are reasonable
```

The `reminder` above is optional: it derives as `min(0.60, threshold × 0.90)`
when omitted (0.50 for the quality-first template). Going above ~0.65 moves
the trigger past ~600K–640K, already inside the band where Sol/Terra measure
~73%; 0.7 (~645K–690K) is a capacity-first choice that deliberately accepts
the long-context rate and some quality loss, and 0.8 (~738K–789K) even more
so. Do not reuse the old 0.3 Luna recipe under this window: it fires at
~277K–296K, already past the pricing line.

##### Codex subscription windows are server-controlled — verify before setting `limit.input`

On a Codex subscription endpoint (`preset: codex`, or a `/codex/responses`
relay), the window a ChatGPT account gets comes from the server-delivered
model catalog (`context_window` / `max_context_window`). Codex counts context
as *input + output*, so a `max_context_window` of 872000 is the raw input
side of the 1M budget — 872K input + 128K output = 1M, which is exactly what
the documented `model_context_window: 1000000` configuration asks for. The
95% factor only turns that into a client-side usable-input figure (~828.4K),
and Codex's own automatic compaction defaults to 90% of the resolved raw
window (~784.8K); none of those is a total-window clamp.

The catalog values have changed repeatedly and have differed between
accounts: the input side long sat at 272K (the 400K allocation = 272K + 128K),
expanded to a 872K maximum in mid-August 2026, and the server began
delivering the expanded profile in early September 2026. Accounts can still
lag, and `/status` may show the configured value before the first request and
the real cap only after it. So:

- Before setting `limit.input`, measure what the endpoint actually accepts:
  configure the candidate value, run a long session, and watch the logs for
  `context_length_exceeded` / oversize rejections.
- On the subscription endpoint, `input: 872000` is the expanded 1M profile
  (2026-09 state); fall back to `input: 272000` if your account/relay still
  serves the older profile. `threshold` is decoupled from the window: it is
  the fraction of the usable budget at which to compact, chosen by your
  quality/cost tradeoff — but the API's >272K-input whole-request 2× pricing
  cliff applies regardless of the window, so keep the trigger inside it if
  that pricing applies to your route.
- The 922K input cap (1.05M window − 128K output) applies only when hitting
  the OpenAI API directly (`api.openai.com/v1/responses`); still leave
  headroom for compaction and oversized single batches.

As everywhere on this page, the `compaction` block lives on the model
template so every provider referencing it inherits it, and the fields tune
the usage-driven automatic-compaction path regardless of `model_driven`. If
you use the `gpt-5.6` alias (it resolves to Sol), put its `compaction` on
the alias' template.

## Codex OAuth preset

Use this when you want ChatGPT/Codex OAuth instead of API keys. Codex uses its
own model allocation; its model limits, provider preset, and authentication
method can all differ from the API-key examples above.

The Codex GPT-5.x limits used in this section are:

| Model | `limit.context` | `limit.input` | `limit.output` |
| --- | ---: | ---: | ---: |
| GPT-5.4 | 1,050,000 | 950,000 | 128,000 |
| GPT-5.5 | 400,000 | 272,000 | 128,000 |
| GPT-5.6 Sol / Terra / Luna | 1,000,000 | 872,000 | 128,000 |

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
          context: 1000000
          input: 872000
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

- Keep API-key and Codex OAuth providers separate when you use both because their credentials and model allocations differ.
- GPT-5.4 uses `1050000 / 950000 / 128000`: the 1.05M total window, Codex's effective input budget (about 90% of the window; Chord uses the declared input as-is — like other published non-additive caps it is not clamped to `context - output`), and the model's maximum output.
- GPT-5.6 Sol/Terra/Luna use the expanded Codex profile `1000000 / 872000 /
  128000` (872K input + 128K output = 1M, matching the documented
  `model_context_window: 1000000`; the API's `>272K` whole-request 2× pricing
  cliff still applies if your route bills that way). If the server catalog for
  your account/relay has not rolled out the expanded profile yet, fall back to
  `400000 / 272000 / 128000`.
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

Claude Opus 5 / 4.8 / 4.7 share the same context window (1M), max output (128K), pricing, adaptive thinking, and input modalities, so all three reuse the single `&claude-opus` template — only the model ID differs. Remove the entries you don't use, and point `model_pools` at your preferred model (e.g. `anthropic/claude-opus-5@high`).

For a lower-cost Claude family config, use the same shape with `claude-sonnet-5`, `cost: {input: 2, output: 10}`, and `output: 64000` for a conservative local allocation. Sonnet 5's $2 / $10 per-1M pricing became permanent in August 2026.

### Claude Fable 5.1

`claude-fable-5-1` (released September 2026) keeps Fable 5's $10 / $50 per-1M input/output rates but cuts cache reads to $0.25 per 1M tokens — 0.025x of base input instead of the standard 0.1x multiplier — so set `cache_read: 0.25`, not 1.0. It shares Fable 5's 1M context, 128K max output, adaptive thinking, and PDF support.

```yaml
model_templates:
  claude-fable-5.1: &claude-fable-5-1
    limit:
      context: 1000000
      output: 128000
    cost:
      input: 10
      output: 50
      cache_read: 0.25
      cache_write: 12.5
      cache_write_1h: 20
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
      claude-fable-5-1: *claude-fable-5-1

model_pools:
  default:
    - anthropic/claude-fable-5-1@high
```

`claude-fable-5` remains available with the same rates except cache reads at $1.0.

#### Compaction tuning for Claude 5

The whole Claude 5 line (Fable 5.1, Opus 5, Sonnet 5) advertises 1M tokens
with 128K output and flat per-token pricing across the window. MRCR v2 8-needle
shows Opus-class models holding ~76% even at 1M (the flattest curve of any
current family), so the reliable window is genuinely large. Opus 4.7-era
models trade retrieval accuracy for refusal honesty; Opus 5 and Fable 5.1
restore strong long-context retrieval. The default `threshold: 0.8` is a
reasonable starting point for these models; if you run many-hour agentic
sessions, 0.7 keeps the model out of the mild 512K+ degradation band.

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

`reminder` is omitted on purpose: it derives to `min(0.60, 0.7×0.9) = 0.60`,
a sensible pressure head start for these models — set it explicitly only when
you want the reminder earlier or later than the derived value.

Note the tokenizer change since Opus 4.7: the same text produces ~30% more
tokens on Claude 5 models than on older ones, so a context budget that felt
right on an older model should be scaled down accordingly.

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
      gemini-3.7-flash:
        limit:
          context: 1048576
          output: 65536
        modalities:
          input: [text, image, pdf]
        thinking:
          level: high

model_pools:
  default:
    - gemini/gemini-3.5-flash
```

Notes:

- Keep `api_url` at the `/models` base path. Chord appends `/{model}:streamGenerateContent?alt=sse` automatically.
- `type` can be omitted; Chord auto-detects Gemini from the `/models` path.
- Gemini 3.7 Flash (GA August 2026) is the current workhorse: introductory $0.75 / $3.75 per 1M tokens through 2026, then $1.50 / $7.50 from 2027. Its thinking levels are `low` / `medium` / `high` only — `minimal` is not supported, and `thinking_budget` is deprecated, so the template above omits `budget`. Gemini 3.5/3.6 Flash remain available with the older template.

#### Compaction tuning for Gemini

Gemini 3.x is the steepest long-context cliff of the current frontier: strong
at 128K (84.9% MRCR v2 8-needle) but collapsing to ~26% at 1M, so the
*reliable* window is only around 128K–200K even though the window advertises
1M. Keep Gemini sessions compacted well before that: tune the model's
`threshold` to ~0.15–0.25 of the usable budget (roughly 150K–250K on a 1M
window) and set `reminder` just below it, so the model gets a pressure hint
and a chance to actively reset before auto-compaction runs.

```yaml
# Add compaction to each Gemini model template you already define; every
# provider referencing the template inherits it.
model_templates:
  gemini-3.1-pro: &gemini-3.1-pro
    limit: {context: 1048576, output: 65536}
    compaction: {threshold: 0.2, reminder: 0.15}
  gemini-3.7-flash: &gemini-3.7-flash
    limit: {context: 1048576, output: 65536}
    compaction: {threshold: 0.25, reminder: 0.2}
```

Gemini also doubles input pricing above 200K tokens (the whole request is
billed at the higher tier), so compacting before 200K saves money as well as
quality. If your workload truly needs long context, prefer a GPT-5.6 Sol /
Claude 5-class model instead of pushing Gemini past its reliable band.

## GLM-5.2 / BigModel Coding Plan

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
    - bigmodel/glm-5.2
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
  max output), so it can reuse any of the GLM-5.2 templates above unchanged —
  only the model ID differs (e.g. `glm-5.3` in your provider's `models` map).
- GLM-5.3-Flash (released August 2026) is the family's first natively
  multimodal model: image/video/file input, with 1M context and 128K max
  output. PDF input is officially supported: the GLM Chat Completion API
  accepts a `file` content block whose `file` object takes `file_id`,
  `file_url`, or `file_data` (a Base64 `data:<MIME>;base64,...` URL), up to
  50 MB per file, in `pdf`/`txt`/`word`/`jsonl`/`xlsx`/`pptx` formats. That
  matches Chord's chat-completions PDF payload exactly (`type: file` with
  `filename` and `file_data`), so no compatibility config is needed. Text
  parameters match GLM-5.3, so it derives from the Chat Completions template
  above and only adds the multimodal `modalities.input`. `thinking.type`
  supports `enabled` only (thinking cannot be turned off), which the chat
  template already sets. Third-party relays may only implement the older
  URL-only `file_url` form; check the relay before relying on Base64
  `file_data`.

#### Compaction tuning for GLM-5.x

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

## DeepSeek V4 (Flash / Pro)

Pair with `~/.config/chord/auth.yaml`:

```yaml
deepseek:
  - "$DEEPSEEK_API_KEY"
```

`deepseek-v4-pro` and `deepseek-v4-flash` share the same API surface, so the
protocol-level config is identical; they reuse the wire-family templates
below. Both models support the Responses API.

```yaml
model_templates:
  deepseek-v4-chat: &deepseek-v4-chat
    limit:
      context: 1000000
      output: 64000
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
      forced_tool_choice:
        suppress_in_thinking: true

  deepseek-v4-messages: &deepseek-v4-messages
    limit:
      context: 1000000
      output: 64000
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

  deepseek-v4-responses: &deepseek-v4-responses
    limit:
      context: 1000000
      output: 64000
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

providers:
  deepseek:
    type: chat-completions
    api_url: https://api.deepseek.com/v1/chat/completions
    models:
      deepseek-v4-pro: *deepseek-v4-chat
      deepseek-v4-flash: *deepseek-v4-chat

  deepseek-messages:
    type: messages
    api_url: https://api.deepseek.com/anthropic/v1/messages
    models:
      deepseek-v4-pro: *deepseek-v4-messages
      deepseek-v4-flash: *deepseek-v4-messages

  deepseek-responses:
    type: responses
    api_url: https://api.deepseek.com/v1/responses
    models:
      deepseek-v4-pro: *deepseek-v4-responses
      deepseek-v4-flash: *deepseek-v4-responses

model_pools:
  default:
    - deepseek/deepseek-v4-flash@high
```

Notes:

- DeepSeek Chat thinking uses `thinking.type`, top-level `reasoning_effort`, and
  `max_tokens`. `request_overrides` supplies the request-shape differences;
  during thinking + tool-call loops, `openai_visible` returns the assistant's
  `reasoning_content` unchanged. DeepSeek rejects forced tool choice while
  thinking is active, so the template downgrades loop-forced `tool_choice:
  required` to the backend default for those requests.
- DeepSeek Responses supports `tool_choice: required`, so its template keeps
  loop-forced tool choice. Plaintext `reasoning_text` makes the encrypted
  reasoning include unnecessary, while `max_output_tokens` remains enabled
  because the endpoint supports it. Other unsupported fields are silently
  ignored by DeepSeek.
- DeepSeek Messages supports `output_config.effort`; Chord derives it from
  `thinking.effort`. Disable Anthropic beta headers for the compatible endpoint.
  DeepSeek's Anthropic-compatible endpoint may return unsigned `thinking`
  blocks rather than Claude-style signed blocks. `anthropic_unsigned` replays
  same-provider/model unsigned thinking natively and can also accept portable
  visible reasoning from other wire families as unsigned `thinking` blocks;
  if the target still rejects that shape, strict compatibility drops the
  reasoning carrier while preserving the tool round.
- Treat third-party `/responses` endpoints as gateway-specific; use
  `reasoning.effort` and `openai_visible` only when the gateway documents its
  mapping.
- For compatible gateways, use the exact model ID and limits published by that
  gateway/account. See [Troubleshooting — DeepSeek / OpenAI-compatible thinking-mode 400s](./troubleshooting.md#deepseek--openai-compatible-thinking-mode-400s).

Additional notes:

- The official pricing page lists a maximum output of 384K; `limit.output:
  64000` here is a conservative local allocation shared with pro. Raise it as
  needed for longer outputs.
- Flash and pro support the Responses API (`api.deepseek.com/v1/responses`).
  The `output_tokens_details.reasoning_tokens` field in responses is handled
  by Chord's standard reasoning replay without extra configuration.
- `reasoning_effort` officially supports `low` / `high` / `max` (default
  `high`). `xhigh` is mapped to `high` and `medium` to `high`, so the
  templates define only the `low` / `high` / `max` variants.
- To override per-model differences (e.g. a different default thinking
  effort), inherit the shared template with YAML anchors and override the
  diff only:

```yaml
  deepseek-v4-pro-chat: &deepseek-v4-pro-chat
    <<: *deepseek-v4-chat
    reasoning:
      effort: max
```

  That gives pro a `max` default thinking effort while flash keeps `high`,
  and reuses everything else (limit, compat, variants).

- Flash pricing is roughly 1/3 of pro (off-peak, no cache hit: input $0.22 /
  output $0.66 per 1M tokens; peak nearly doubles those, and cache-hit input
  starts at $0.007), suitable for high-volume / low-cost scenarios. See
  [DeepSeek official pricing](https://api-docs.deepseek.com/quick_start/pricing/).

### DeepSeek V4 Flash Vision (experimental)

`deepseek-v4-flash-vision-exp` is the vision variant of Flash: identical text
capabilities and thinking behavior, plus image input (JPEG / PNG / GIF / WebP,
inline, URL, or the Files API). It is the only V4 model that accepts images —
Flash and Pro reject them with a `400` ("This model does not support image"). The
model is officially labeled experimental and priced identically to Flash: images
count as input tokens, capped at 384 tokens per image (see the pricing table and
[the official Vision guide](https://api-docs.deepseek.com/guides/vision/)). Images
are accepted on all three wire families (Chat Completions `image_url`, Responses
`input_image`, and the Anthropic-compatible endpoint), so each family reuses its
shared V4 template, adding only `modalities`:

```yaml
model_templates:
  deepseek-v4-vision-chat: &deepseek-v4-vision-chat
    <<: *deepseek-v4-chat
    modalities:
      input: [text, image]

  deepseek-v4-vision-messages: &deepseek-v4-vision-messages
    <<: *deepseek-v4-messages
    modalities:
      input: [text, image]

  deepseek-v4-vision-responses: &deepseek-v4-vision-responses
    <<: *deepseek-v4-responses
    modalities:
      input: [text, image]

providers:
  deepseek:
    type: chat-completions
    api_url: https://api.deepseek.com/v1/chat/completions
    models:
      deepseek-v4-pro: *deepseek-v4-chat
      deepseek-v4-flash: *deepseek-v4-chat
      deepseek-v4-flash-vision-exp: *deepseek-v4-vision-chat

  deepseek-messages:
    type: messages
    api_url: https://api.deepseek.com/anthropic/v1/messages
    models:
      deepseek-v4-pro: *deepseek-v4-messages
      deepseek-v4-flash: *deepseek-v4-messages
      deepseek-v4-flash-vision-exp: *deepseek-v4-vision-messages

  deepseek-responses:
    type: responses
    api_url: https://api.deepseek.com/v1/responses
    models:
      deepseek-v4-pro: *deepseek-v4-responses
      deepseek-v4-flash: *deepseek-v4-responses
      deepseek-v4-flash-vision-exp: *deepseek-v4-vision-responses
```

The per-request `detail` field on `image_url` (or `input_image`) accepts `low`,
`high`, `original`, or `auto` — `high` and `original` are equivalent — and tunes
how images are processed, which changes the token cost. Chord currently sends
`auto` for every image and does not expose per-request `detail` configuration;
images are delivered as inline base64, so external URLs and Files API `file_id`
inputs are not supported from within Chord. The
[`view_image`](./tools.md) tool can load local images into context, but only
when this model heads the active pool on the `messages` or `responses` provider:
the `chat-completions` (`deepseek`) provider accepts images in user messages yet
cannot carry them back in tool results.

#### Compaction tuning for DeepSeek V4

DeepSeek V4 Pro/Flash advertise a 1M window, but the MLA architecture degrades
noticeably at long range: independent multi-needle evals put V4 Pro around
~41% at 1M (8-needle) versus ~78% single-needle, a sharp drop that mirrors the
Gemini cliff. The reliable working window is roughly 200K on the 1M window.
V4 is the cheapest family by a wide margin even on cache misses, so frequent
compaction is far cheaper than on premium models — compact early and often:

```yaml
# deepseek-v4-chat / deepseek-v4-messages / deepseek-v4-responses already
# share a base in the recipes above; add compaction to the shared template so
# every deepseek-v4-pro / deepseek-v4-flash entry inherits it.
model_templates:
  deepseek-v4-chat: &deepseek-v4-chat
    limit: {context: 1000000, output: 128000}
    compaction: {threshold: 0.25, reminder: 0.2}
```

DeepSeek's cache-hit rate is the best in the industry ($0.0036/M), so a
compaction that preserves the cacheable prefix is nearly free on repeated
reads. Keep short interactive sessions on the global default and only tune the
model entry when you run genuinely long agentic runs.

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

For all `openai_visible` recipes (DeepSeek, GLM, supported Qwen, and Kimi),
Chord first replays native reasoning optimistically to any Chat Completions
target, so documented in-provider upgrades such as Kimi K2.6/K2.7 to K3 and
same-model provider fallback can keep continuity. Recipes for backends that
drop earlier-turn reasoning server-side (DeepSeek) omit `preserve_history`,
so Chord strips completed-turn reasoning before replay instead of paying to
resend it; preserved-thinking recipes (GLM `clear_thinking: false`, Qwen
`preserve_thinking`, Kimi K3 / `keep: all`) set `preserve_history: true` so
the complete assistant history is replayed unchanged. If a target rejects native
reasoning, Chord removes or converts only the incompatible reasoning payload.
Completed tool calls and their paired results remain available to the next
model; they are not treated as disposable chain-of-thought data. A strict
compatibility fallback may textify the completed action history when the target
cannot accept the structured shape.

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

## Grok 4.6 (xAI Responses)

xAI recommends the Responses API for Grok. Grok 4.6 supports text and image
input, function calling, structured output, reasoning, and a 500K context
window. xAI also accepts PDF attachments as `input_file` with a public
`file_url` or an uploaded `file_id`, which activates the server-side
`attachment_search` tool; Chord sends PDF attachments as inline base64
`file_data`, which the xAI Responses API does not accept for non-image
documents, so this recipe keeps `pdf` out of `modalities.input`. Grok 4.6
emits reasoning text through `response.reasoning_text.*` stream events; Chord
maps those events to the normal thinking stream while preserving the ordered
Responses output items for tool-loop continuity.

```yaml
model_templates:
  grok-4.6: &grok-4-6
    limit:
      context: 500000
    reasoning:
      effort: high
    modalities:
      input: [text, image]

providers:
  xai:
    type: responses
    api_url: https://api.x.ai/v1/responses
    models:
      grok-4.6: *grok-4-6

model_pools:
  default:
    - xai/grok-4.6
```

xAI publishes a 500K total context window for Grok 4.6, but not a lower,
separate model output cap, so this recipe omits `limit.output`. Chord therefore
does not send `max_output_tokens` to xAI and lets the API fit output within the
remaining context. Locally, Chord still reserves its default `64000` output
budget when deriving the input budget from `limit.context`. Set `limit.output`,
raise Chord's `max_output_tokens`, and enable
`compat.responses.send_max_output_tokens: true` only when you intentionally
want to enforce and send an explicit cap.

Use `grok-4.6` as the model ID. Do not configure
`openai_visible`: xAI Responses uses native ordered output/reasoning state, not
Chat Completions `reasoning_content`. `reasoning.effort` accepts `low`,
`medium`, `high`, and `xhigh` (Grok 4.6 only; models that do not support it
treat it as `high`). High is the default and reasoning cannot be disabled.

## Verify any recipe

After copying a recipe, run one targeted check first:

```bash
chord doctor models --model provider/model
```

Then verify the exact variant you plan to use, for example:

```bash
chord doctor models --model openai/gpt-5.6@max
chord doctor models --model codex/gpt-5.5@max
chord doctor models --model anthropic/claude-opus-5@high
```
