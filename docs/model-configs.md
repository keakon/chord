# Model configuration

<!-- description: Connect models through the built-in catalog: chord config add, catalog refresh, custom-endpoint borrowing, and gateway behavior notes. -->

Chord fills in what a model can do from its built-in model catalog: context,
input, and output limits, input modalities, reasoning variants, and — for
verified endpoint bindings — the Responses fields a route accepts. The
catalog's data lives in the public
[chord-models](https://github.com/keakon/chord-models) repository; each chord
release embeds a verified snapshot of it, and a catalog refresh brings newer
data without waiting for a release. Entries are either **verified** (official
docs checked, request behavior validated — they provide runtime defaults) or
**candidate** (discovered, shown with their scope and sources, never
defaults).

Inspect the catalog with `chord config show --catalog`. The view states which
snapshot it shows and where that snapshot came from — the embedded one, or a
refreshed cache with its upstream tag. Protocol and field semantics live in
[Configuration](./configuration.md); this page is about connecting a model and
keeping the setup current.

## Fastest path: `chord config add`

A provider on a managed preset (`openai`, `anthropic`, `gemini`, `codex`)
needs one command:

```bash
chord config add openai/gpt-6.1-sol
```

A preset-bound wire name resolves against the catalog: no model entry is
written, the pool reference is, and limits, modalities, and reasoning variants
fill in at load from the verified binding. Flags cover the rest of the wiring
(`--pool` for the target pool, `--api-key-env` to store a credential
reference) — see [`chord config add`](./cli.md#chord-config-add).

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

After a catalog update, references whose verified model a newer same-family
entry likely supersedes are reported as advisories at startup and by
`chord doctor config`. Re-run `chord config add` to rebind, or acknowledge for
the current catalog version:

```bash
chord config add mygw/claude-gw --keep-current
```

Verify a model end to end before relying on it:

```bash
chord doctor models --model mygw/claude-gw
```

## Custom endpoints: borrowing catalog facts

`chord config add ... --catalog <id>` writes a `catalog:` reference under the
model. A custom endpoint (no preset) inherits limits, modalities and compaction
hints. When its protocol matches the official connection, it also inherits
the model behavior, `compat`, reasoning variants and Responses field-send rules.
The provider keeps its URL, credentials and transport settings.
Turn a borrow off per model with `catalog: false`:

```yaml
providers:
  mygw:
    type: messages
    api_url: https://gateway.example.com/v1/messages
    models:
      claude-gw:
        catalog: anthropic/claude-opus-5-5
```

Facts the catalog does not have stay unset and are reported as diagnostics —
fill them in manually instead of copying a neighbor's numbers. Explicitly
configured values always win over catalog facts; every catalog-filled value
is visible as the `catalog` layer in `chord config show`.

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
thought signatures back, and Gemini 3 rejects the request that follows each
tool call (HTTP 400). Pin `native_thinking: gemini-3` on every Gemini 3
model behind a gateway, even one without a thinking block.
`chord doctor config` warns about a Gemini 3 model without the selector and
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

Automatic compaction derives its budget from the model's context window, so
with catalog-filled windows the global default (`threshold` 0.8) is the right
starting point. Tune a model's `compaction` block only when its long-context
reliability or pricing gives a reason to — see
[Context compaction](./context-management.md#context-compaction) for the
mechanics. Family starting points recorded so far: Claude 5 long agentic
sessions 0.7; GPT long-context pricing tiers around 0.25 with the reminder
just below; Gemini 0.2/0.15; GLM and DeepSeek 0.25/0.2; Grok 0.4/0.35;
MiniMax 0.5/0.45. When a model's long-context behavior is undocumented, stay
on the default.

## What a catalog recipe contains

The catalog contains token limits and modalities, reasoning or thinking
variants, model `compat` fields and documented compaction hints. Preset bindings
and custom endpoints using the same protocol inherit the model recipe at load.
`chord config add` writes the official URL for a new provider, or preserves your
custom URL and records a `catalog` reference. This also works for an alias such
as `sample/model` with `--catalog moonshotai/kimi-k3`: full reasoning replay is
inherited without having to copy its compat settings.
Inspect every field and its sources with `chord config show --catalog --json`.

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

Request-body compression is separate from context compaction. It is disabled
by default for all automatically added providers, including official ones.
The catalog records known endpoint support (`zstd` for Codex, `gzip` for
Anthropic) as reference information. To enable it, set provider `compress: gzip`
or `compress: zstd` after confirming endpoint support. Adding a model preserves
an existing explicit compression setting.
