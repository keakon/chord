# Model configuration

<!-- description: Connect models through the built-in catalog: chord config add, catalog refresh, custom-endpoint borrowing, and gateway behavior notes. -->

`chord config add` saves explicit model settings in `config.yaml`, using the
built-in model catalog for context,
input, and output limits, input modalities, reasoning variants, and — for
verified endpoint bindings — the Responses fields a route accepts. It writes
`catalog: false` so the saved model does not depend on the catalog at runtime. The
catalog's data lives in the public
[chord-models](https://github.com/keakon/chord-models) repository; each chord
release embeds a verified snapshot of it, and a catalog refresh brings newer
data without waiting for a release. Verified model facts come from cited
documentation. Documented connections supply setup information; managed
bindings separately record verified route behavior. A model fact or connection
does not imply a live request test. **Candidates** retain incomplete discoveries
with their scope and sources and never supply runtime defaults.

Browse the catalog with `chord config add` (no arguments), which lists the
managed presets and their verified wire models; `chord doctor config` states
which snapshot is in effect and where it came from — the embedded one, or a
refreshed cache with its upstream tag. Protocol and field semantics live in
[Configuration](./configuration.md); this page is about connecting a model and
keeping the setup current.

## Fastest path: `chord config add`

A provider on a managed preset (`openai`, `anthropic`, `gemini`, `codex`)
needs one command:

```bash
chord config add openai/gpt-6.1-sol
```

A preset-bound wire name resolves against the catalog once. The command writes
the resolved model settings and a pool reference, preserving any explicit
settings you already have. Flags cover the rest of the wiring
(`--pool` for the target pool, `--api-key-env` to store a credential
reference) — see [`chord config add`](./cli.md#chord-config-add).

Thinking levels are saved as named `variants`, leaving the base effort or level
unset unless you already configured one. The endpoint's verified variants take
precedence; without that list, the command uses documented model levels only
when the protocol matches. Select a level with `--variant high` or a model
reference such as `openai/gpt-6.1-sol@high`. With no explicit selection, the
endpoint default applies.

A custom gateway that serves a catalog model under its own name borrows the
facts explicitly:

```bash
chord config add mygw/claude-gw --url https://gateway.example.com/v1/messages \
  --catalog anthropic/claude-opus-5-5
```

In an interactive terminal, run `chord config add mygw/gpt-6-sol` directly. If there is no catalog binding, press a number to choose a suggested verified model without Enter, press `m` to enter a catalog ID, or press Esc to cancel. For a new provider, supply the API URL and API-key environment variable. You can then choose an existing pool or create a new one, and adjust the reasoning variant and request compression, review the preview and confirm saving. Existing providers reuse their URL and credentials. Enter `q` or decline saving to cancel without changing files.

For scripts, pass `--no-interactive` and explicit arguments. An unmatched name in non-interactive mode shows verified suggestions and refreshed candidates, then returns an error; no model or candidate is adopted automatically. To fetch recently published catalog updates, opt into network access:

```bash
chord config add mygw/gpt-6.2-sol --url ... --refresh-catalog
chord config refresh-catalog   # or refresh without adding anything
```

A refreshed snapshot replaces the catalog as a whole, by version, and any
failure (network, corrupt snapshot, incompatible schema) falls back to the
snapshot already in effect — the built-in catalog keeps working offline either
way.

An identical release from the same source can also deliver candidates when
refreshing the embedded version; conflicting data under the same version is
rejected.

Refreshing affects future additions and models that use runtime catalog
filling. Models saved with `catalog: false` keep their settings. Repeating
`chord config add`, even with `--catalog`, preserves existing explicit values;
edit those values in `config.yaml` to change them.

Handwritten catalog references can receive freshness advisories at startup and
from `chord doctor config`. Acknowledge a reference for the current catalog
version with:

```bash
chord config add mygw/claude-gw --keep-current
```

Verify a model end to end before relying on it:

```bash
chord doctor models --model mygw/claude-gw
```

## Gemini 4 Argon

The embedded catalog snapshot, [v2026-11-11.1](https://github.com/keakon/chord-models/tree/v2026-11-11.1), includes `google/gemini-4-argon`. Existing installations can refresh the catalog before connecting to the official API with `GEMINI_API_KEY`:

```bash
chord config refresh-catalog
chord config add google/gemini-4-argon --api-key-env GEMINI_API_KEY
chord doctor models --model google/gemini-4-argon
```

Input, thinking, and output share a 1,048,576-token window, with no separately documented input cap. The available thinking variants are `low`, `medium` and `high`; `minimal` is not supported. Select a variant with a reference such as `google/gemini-4-argon@high`, or leave it unset to use the endpoint default. Model facts retain the long-output capacity, while the official `gemini` binding sets `limit.output` to 65,536 for thinking and output combined in the current streaming request. The actual request budget also respects global `max_output_tokens` (64,000 by default) and the remaining context window.

With `compat.generate_content.automatic_continuation: true`, Chord resumes the same generation using the server checkpoint, including recovery from interrupted connections with the same model, endpoint, and credentials. If recovery fails, a fallback model starts again from the reliable history preceding the generation; it cannot inherit the original thinking. This setting applies only to native `generate-content`, not OpenAI-compatible endpoints or Code Assist. Ordinary multi-turn tool use is distinct from continuing one generation across HTTP requests.

The embedded catalog retains its single-request limit. To enable native continuation explicitly:

```yaml
max_output_tokens: 262144
providers:
  gemini:
    type: generate-content
    api_url: https://generativelanguage.googleapis.com/v1beta
    response_header_timeout: 1200
    stream_idle_timeout: 1200
    models:
      gemini-4-argon:
        catalog: false
        compat:
          generate_content:
            automatic_continuation: true
            single_request_output_limit: 65536
        limit:
          context: 1048576
          output: 1048576
        variants:
          low:
            thinking:
              level: low
          medium:
            thinking:
              level: medium
          high:
            thinking:
              level: high
```

Each compat field inherits independently: model configuration > provider configuration > catalog defaults. Explicit `false` disables continuation; `single_request_output_limit` then caps the request budget. `null` or an empty object does not disable inherited settings. The default is off, with no model-name inference. A gateway must forward continuation tokens to support this feature; Chat Completions `native_thinking` does not enable it.

The global default remains 64,000 tokens. Increasing `max_output_tokens` grants a larger total thinking/output budget, still constrained by model limits and remaining context. A final `MAX_TOKENS` never increases that budget automatically. Multiple requests incur repeated input charges; the Context reading is separate from cumulative billing input. Local tools wait for the final response to be confirmed and persisted. Checkpoints exist only during the current in-process request; restarting cannot resume native thinking.

Argon requires a paid API account with available quota. A free-tier quota of zero returns HTTP 429; Google AI Pro / Ultra subscriptions do not replace API credentials and quota. Use `doctor models` above to check your own connection; catalog inclusion does not establish account access.

## Gemini 4 Carbon

The embedded catalog snapshot, [v2026-11-11.1](https://github.com/keakon/chord-models/tree/v2026-11-11.1), includes `google/gemini-4-carbon` as well. Carbon is Google's strongest model for complex, multi-step coding and long-horizon agentic work, while Argon remains the everyday choice for most software engineering. The connection shares the Argon route and `GEMINI_API_KEY`:

```bash
chord config refresh-catalog
chord config add google/gemini-4-carbon --api-key-env GEMINI_API_KEY
chord doctor models --model google/gemini-4-carbon
```

Carbon matches the Argon capacity profile: input, thinking, and output share a 1,048,576-token window with no separate input cap, the official `gemini` binding caps the streaming request at 65,536 tokens, and thinking variants are `low`, `medium` (default) and `high`. Input accepts text, image, video, audio and PDF. The same `compat.generate_content.automatic_continuation` setup from the Argon section above applies unchanged, with `gemini-4-carbon` in place of `gemini-4-argon`.

Carbon launched at Argon's introductory rates: $2/$10 per million input/output tokens through November 30, 2026, then $4/$20. A free-tier quota of zero returns HTTP 429 exactly as on Argon; the 2026-11-11 smoke check reached the API and was quota-blocked, so verify your own connection with `doctor models`.

## Custom endpoints: borrowing catalog facts

`chord config add ... --catalog <id>` copies the selected recipe into the model
and writes `catalog: false`. The selection takes priority over the preset binding and any
saved catalog choice; it must be valid for the selected preset. A custom
endpoint (no preset) copies limits, modalities and compaction
hints. When its protocol matches the official connection, it also copies
the model behavior, `compat`, reasoning variants and Responses field-send rules.
The provider keeps its URL, credentials and transport settings.

For runtime catalog filling, you can instead write a catalog reference by hand.
That model depends on the selected catalog entry at every load:

```yaml
providers:
  mygw:
    type: messages
    api_url: https://gateway.example.com/v1/messages
    models:
      claude-gw:
        catalog: anthropic/claude-opus-5-5
```

Unknown facts stay unset; fill them in from your endpoint's documentation.
Set `cost` explicitly for usage accounting, including that endpoint's cache
prices. Adding a model preserves an existing `cost` block. Explicitly
configured values always win over catalog facts; `chord config advise` only
compares values you declared yourself.

Refreshed **candidates** appear in the suggestion list annotated with the
provider scope they were observed on and their sources. When the same wire
name was seen on several scopes, Chord lists the sightings side by side,
prefers the one matching your endpoint, and never picks for you: values from
a non-matching scope are reference values only. Adopting a candidate means
writing its observed values into your config as your own explicit settings —
a candidate never becomes a default. If it references a verified model,
borrowing that model (`--catalog <id>`) is the verified adoption path.

## Gateway behavior experience

Verified preset bindings carry Chord's recorded knowledge of an endpoint; a
custom gateway may behave differently. The knobs below are documented in
detail in [Configuration](./configuration.md); these are the working rules of
thumb.

### Thinking behind a Chat Completions gateway

Chord's `thinking.*` keys are wire-independent, but a gateway translating
`/v1/chat/completions` into a native API only reads the thinking controls in
the shape its translation understands. `compat.chat_completions.native_thinking`
selects the shape Chord writes into the chat body: `gemini` / `gemini-3`
(`extra_body.google.thinking_config`), `anthropic`
(`thinking: {type, budget_tokens}`), `thinking` (the native
`thinking: {type}` object used by DeepSeek, GLM, Kimi K2.x, and Doubao), and
`qwen` (`enable_thinking`); family names such as `claude` and `kimi` select
the same shapes. Only a DeepSeek route picks the `thinking` object without a
selector.

The selector is also the only signal that a gateway model is Gemini or
Claude, whatever its model ID says: without it Chord does not write Gemini
thought signatures back, and Gemini 3/4 rejects the request that follows each
tool call (HTTP 400). Pin `native_thinking: gemini-3` on every Gemini 3/4
model behind a gateway, even one without a thinking block.
`chord doctor config` warns about a Gemini 3/4 model without the selector and
about a gateway model whose thinking block would be dropped.

### Reasoning continuity across protocols

Backends that require the complete reasoning history back on every request
need `compat.reasoning_continuity.reasoning_replay: all` — the default
(`current_turn`) replays only the current turn. DeepSeek Chat/Messages keep
the full history on their own; recipes that preserve thinking
(GLM `clear_thinking: false`, Qwen `preserve_thinking`, Kimi K3
`keep: all`) and hard-replay contracts such as MiniMax's thinking mode set
`reasoning_replay: all` explicitly. See
[Choosing the replay contract](./reasoning.md#decide-the-replay-contract) and
[What crosses a fallback pool](./reasoning.md#what-crosses-a-fallback-pool).

### Compaction tuning

Automatic compaction uses a fixed model baseline: a documented independent
input cap, or the total window minus the model's maximum output capacity.
If output capacity is unknown or spans the whole window, the baseline is the
total window. Changing the requested output cap does not move the threshold. An explicit
model or global threshold takes priority; otherwise, Chord uses the model's
catalog recommendation, or the global default of 0.8 when none is recorded.
See [Context compaction](./context-management.md#context-compaction).

Catalog thresholds are starting recommendations, not provider limits or
measured performance optima. Tune against representative tasks, considering
quality, latency, request cost and cache reuse. The selected model's
`config_profile` and `cost.notes` are published in the
[chord-models](https://github.com/keakon/chord-models) repository; long-context
pricing can differ from the listed base rate.

## What a catalog recipe contains

The catalog contains token limits and modalities, reasoning or thinking
variants, model `compat` fields and documented compaction hints.
`chord config add` resolves this recipe and saves its model fields explicitly.
It writes the official URL for a new provider or preserves your custom URL.
For an alias such as `sample/model` with `--catalog moonshotai/kimi-k3`, it
copies the full reasoning replay setting into `compat`.
Every field and its sources are published in the [chord-models](https://github.com/keakon/chord-models) repository.

When a gateway exposes a different protocol, limits, modalities and compaction
hints still transfer; protocol-specific fields require that gateway's recipe.

Explicit compat fields take priority over inherited compat fields; explicit
provider compat also overrides catalog defaults. Other model blocks are kept
as configured, with catalog defaults filling only absent blocks. Setting a block to `null` prevents the catalog from filling it.
Model compaction hints fill unset fields only. An explicit global or model
`threshold` prevents the catalog from filling an unset `reminder`, which is
derived from the effective threshold. An explicit global reminder also takes
priority over a catalog hint. Explicit model values take priority over the
global settings.

Request-body compression is separate from context compaction. Automatically
added providers send uncompressed bodies, except providers using
`preset: codex`, which send `zstd` the way the Codex client does. The catalog
records known endpoint support (`zstd` for Codex, `gzip` for Anthropic) as
reference information. To override the preset default, set provider
`compress: gzip`, `compress: zstd` or `compress: off`. Adding a model preserves
an existing explicit compression setting.

Keep reusable model settings in `model_templates` and reference them with YAML
anchors. Use `variants` to select a thinking effort through a `model@variant`
reference. Documented model reasoning options may exceed the variants verified
on a managed route; check which options your endpoint accepts.

The Messages configuration below shares settings across Claude models and
defines their supported effort levels: `low`, `medium`, `high`, `xhigh` and
`max`. It leaves the base model's effort unset, so a reference without a variant
uses the endpoint's default. Add a separate `cost` block to each model using
your endpoint's prices:

```yaml
model_templates:
  claude-5: &claude-5
    catalog: false
    limit:
      context: 1000000
      output: 128000
    modalities:
      input: [text, image, pdf]
    thinking:
      type: adaptive
      display: summarized
    prompt_cache:
      mode: explicit
      ttl: 5m
      cache_tools: true
    compaction:
      threshold: 0.7
      reminder: 0.6
    variants:
      low:
        thinking:
          effort: low
      medium:
        thinking:
          effort: medium
      high:
        thinking:
          effort: high
      xhigh:
        thinking:
          effort: xhigh
      max:
        thinking:
          effort: max
providers:
  mygw:
    type: messages
    api_url: https://gateway.example.com/v1/messages
    models:
      claude-fable-5-2: *claude-5
      claude-opus-5-5: *claude-5
      claude-sonnet-5-5: *claude-5
model_pools:
  fable:
    - mygw/claude-fable-5-2
```

To select an effort, use a reference such as `mygw/claude-fable-5-2@high`
in the pool; the corresponding `variants.high` entry must exist.
Run `chord doctor config` to check the configuration and variants, then
`chord doctor models --model mygw/claude-fable-5-2` to verify the connection.
