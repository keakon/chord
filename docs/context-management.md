# Context Management

Chord provides two complementary context management layers:
**context compaction** rewrites the session history with an LLM-generated
summary, while **context reduction** trims stale tool output from each
individual request prompt. They operate at different levels and serve different
purposes.

Both are configured under the top-level `context:` key in `config.yaml`. For
the surrounding configuration model (files, layers, providers), see
[Configuration & Auth](./configuration.md).

## Quick comparison

| Aspect | Context compaction | Context reduction |
|--------|-----------|-----------|
| What it does | Calls an LLM to generate a structured summary and replaces old history | Applies deterministic rules to trim stale tool output from the current request |
| Writes to disk | ✅ Rewrites session files | ❌ Session files unchanged |
| Uses an LLM | ✅ (configurable model pool) | ❌ (heuristic rules only) |
| When it fires | Context exceeds threshold / manual `/compact` / error recovery | Before every LLM request |
| Typical latency | Seconds to tens of seconds (waits for LLM) | Milliseconds (in-memory rule matching) |
| User visibility | TUI shows "Compacting context..." progress | Silent (invisible) |
| Loop mode | Enabled; compaction still runs so long sessions can continue | Disabled for new messages; see [Loop mode and the Codex quota freeze](#loop-mode-and-the-codex-quota-freeze) |

**How they work together**: Reduction is the lightweight first line of defense —
it trims stale tool output before every request, slowing down context growth.
When reduction alone is not enough and the context keeps growing past the
compaction threshold, compaction steps in for a deep compression pass. Most
users only need to care about compaction settings; reduction defaults are
already tuned for common usage patterns.

Automatic compaction is primarily driven by provider-reported input usage.
Request-level reduction may make the current prompt smaller, but local estimates
from that reduced prompt do not cancel a compaction request that was already
triggered by provider usage. If a provider or gateway later stops reporting
usage (or reports `input_tokens: 0`), Chord can use the last trusted non-zero
usage sample and current context-contributing message bytes as a conservative
fallback signal for the same automatic threshold.

## Context compaction

When the main conversation approaches the model context limit, Chord
automatically triggers context compaction. The compaction process calls an LLM
to analyze the current conversation, generates a structured summary (covering
goals, progress, key decisions, file evidence, etc.), archives old messages,
and replaces the conversation history with the summary. The compacted session
is persisted to disk.

Continuation-oriented compaction keeps a small, safe recent tail (normally the
latest two user turns, within a token budget) as verbatim messages after the
checkpoint. Tool-call/result pairs are never split, and short histories fall
back to summarizing the full safe head when preserving the tail would leave too
little material to summarize. Explicit `archival` profiles remain summary-only.
Key files reloaded from the checkpoint are request-local overlays read from disk
on every request; each `<file>` block includes its SHA-256 revision and whether
it changed since that checkpoint's first injection. The overlay is injected only
after the stable reduction surface is remembered, so it never enters
prefix-compatibility checks and cannot invalidate incremental reduction reuse.

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
| `reserved` | int | `0` | Fixed token headroom added on top of the proportional headroom left by `threshold`, for tokenizer drift, tool schema overhead, and compaction/recovery safety. Usually omit it (leave it at `0`); a non-zero value is subtracted from the input budget before applying `threshold`. |
| `preset` | string | auto-detected | Force a specific compaction implementation. Usually unnecessary. |
| `profile` | string | `auto` | Compaction strategy. Usually unnecessary. |

### How the threshold is calculated

Chord uses the **usable input budget** as
the baseline. If the model config sets `limit.input`, that value is used;
otherwise Chord derives it from `limit.context - effective requested output`
(where effective output is `max_output_tokens` capped by the model's
`limit.output`). If `reserved` is set, it is subtracted first. The effective
trigger is therefore `(input budget - reserved) × threshold`: `reserved` adds
to, rather than replaces, the unused proportional headroom left by
`threshold`. The TUI `Context` indicator in the info panel and footer uses the
same input-budget baseline after subtracting `reserved`, so its percentage
matches automatic compaction thresholds. For
providers that report prompt-cache writes separately, Chord counts the current
prompt-side usage as `input_tokens + cache_write_tokens` so newly cached prompt
segments are included in the displayed context burden.

Provider usage is the authority for this automatic trigger. Chord does not use
local token estimates from request-level reduction to clear an already-triggered
automatic compaction request, because those estimates can diverge from provider
accounting for multimodal inputs, tool schemas, and gateway-specific framing.
There is one fallback for missing usage: after Chord receives a trusted non-zero
`input_tokens` sample, it records the context-contributing message byte size for
that sample, including content plus replayed tool-call arguments, thinking
blocks, and reasoning text. If later responses omit usage or report zero while
those bytes have grown, Chord estimates `input_tokens` by scaling that sample by
the byte ratio and can trigger automatic compaction when the estimate reaches
`threshold`. This byte-calibrated estimate is only an early compaction signal;
it is not used for billing or as an exact context-window measurement.

**Additional fixed headroom example (only when needed)**:

Usually, setting `threshold` is sufficient. With `input: 272000` and
`threshold: 0.8`, omitting `reserved` triggers compaction at
`272000 × 0.8 = 217600` tokens, already leaving `54400` tokens (20%) of
proportional headroom.

Set a non-zero `reserved` only when you need additional fixed headroom, such
as with a very high threshold, unreliable provider usage, or an unusually
large tool schema:

```yaml
context:
  compaction:
    threshold: 0.8
    reserved: 16000
```

The usable budget is then `256000`, and automatic compaction triggers when
context reaches `256000 × 0.8 = 204800` tokens, `12800` tokens earlier than
with `threshold: 0.8` alone. The TUI `Context` percentage also uses `256000` as
its denominator. If you are unsure whether you need additional fixed
headroom, keep the default value of `0`.

Note: a non-zero `reserved` cannot be reset from a project config. Chord uses
the first positive `reserved` across the project and global layers, so a
project-level `reserved: 0` falls back to the global value; lower the global
setting instead.

### Manual compaction and oversize recovery

Beyond automatic triggering, you can manually compact at any time with the
`/compact` command in the TUI. Manual compaction uses the same background
worker as automatic compaction: it can be started while the agent is already
working, shows progress in the background compaction status slot, and applies
at the next safe continuation/idle barrier rather than interrupting the active
turn immediately. You can also use `/compact --no` to temporarily disable
subsequent automatic compaction for the current session.

If every attempted candidate model rejects a request with a context-length error
and automatic compaction is enabled, Chord starts an oversize-recovery compaction
and retries after it applies. If automatic compaction is disabled (`threshold: 0`
or `/compact --no`), Chord stops the turn and reports a clear error instead of
continuing to retry the same oversized prompt.

### Split input/output limits

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

## Context reduction

Before each LLM request, Chord applies deterministic rules to inspect tool
results and trim large, stale output. **This only affects the current request
prompt — it never rewrites session files on disk.** Decisions use tool type,
actual main-model request batches, size, and local validity state. Context usage
affects durable compaction only and cannot change the reduction surface.

Reduction is enabled by default and usually needs no per-field tuning. Either
form keeps the built-in defaults:

```yaml
context:
  reduction: true
```

```yaml
context:
  reduction: {}
```

`context.reduction: false` disables request-level reduction entirely (durable
Compaction still applies); `true` / `{}` — or omitting `context.reduction` —
keeps the default request-level reduction behavior.

Configuration layers use the usual more-specific-wins rule. In particular, a
project-level `true` or mapping explicitly re-enables reduction after a global
`false`; omitting the project value inherits the global setting.

The full set of fields and their defaults:

```yaml
context:
  reduction:
    confirm_age_turns: 2
    error_age_turns: 3
    high_risk_protect_age_turns: 4
    shell_success_age_turns: 1
    shell_success_bytes: 3000
    read_like_age_turns: 1
    read_like_output_bytes: 3000
    stale_age_turns: 3
    stale_output_bytes: 1500
    wrap_up_grace_requests: 1
    min_tool_results_prune: 6
    min_incremental_saved_tokens: 2048
```

Unset or non-positive threshold fields use these defaults. Project-level
`.chord/config.yaml` can override global config field by field.

> **Most users do not need to configure this section.** The built-in defaults
> are conservative and work well for common scenarios. In empirical local-session
> analysis, reduction produced meaningful savings without systematically
> breaking prompt-cache reuse; the tuning table below shows how to bias further
> in either direction.

### Default behavior

- Chord runs lightweight request-level reduction before each main-model request; normal prompt-cache warmup does not protect otherwise reducible tool output.
- When `todo_write` marks every TODO as completed or cancelled, Chord treats the next main-model request as a wrap-up request. The default `wrap_up_grace_requests: 1` avoids low-value prompt-surface churn only when the same model is active, no user input is queued, and estimated savings are below `min_incremental_saved_tokens`.
- Reduced messages freeze and are reused byte-for-byte. Unreduced non-read results store their next request-batch review frontier, so only new, due, repeated, or invalidated items are reclassified. Due frontiers cannot be bypassed by small-tail reuse. Reads keep path/range-aware read/edit validity analysis; a still-current read stays full until a later mutation or covering read marks it `truncated=stale` / `truncated=superseded`.
- Stable surfaces analyze only the new tail and due frontier. History shape, tool schema, model, Reduction policy, session, or incompatible message changes invalidate the surface.
- Context pressure does not alter Reduction; usage thresholds belong to durable Compaction.
- Recent high-risk tool outputs are protected by request-batch age. Parallel tool calls and results from one assistant response share one batch and do not age one another.
- Successful shell output is treated as low risk once it is old enough and larger than `shell_success_bytes`. Chord keeps a compact summary with output size, line count, salient success lines when present, and a tail excerpt fallback; the shell command itself remains available from the associated tool call. Recent failures, stack traces, diffs, and warning-heavy build logs are routed through high-risk or structured-log handling before this success-output summary path; older outputs may later be summarized when they are no longer protected by the recent high-risk window.
- After a successful shell invocation that is not on the static read-only allowlist, Chord rechecks durable hashes for previously read files in the affected stable/recovered prefix. A confirmed replacement, deletion, or hash change marks the old read `truncated=stale`; unreadable paths or legacy reads without a durable hash are not guessed stale.
- Large old tool results are age/byte-pruned, but Chord preserves structured hints before falling back to generic omission: `read` keeps path/range metadata, `grep` / `glob` / LSP references keep query scope plus representative hits, JSON output keeps top-level shape/counts, successful shell output keeps size/salient-line context, and build/test logs keep key failure or warning lines. Older errors, diagnostics, and confirmations are reduced to compact fixed markers or summaries.
- Reduction diagnostics keep the aggregate `reread_after_reduction` counter and additionally distinguish same-revision re-reads from changed-revision refreshes when both reads carry durable hashes. These are telemetry only; Chord does not automatically retune retention windows from a small number of samples.

### Loop mode and the Codex quota freeze

In loop mode, reduction is not applied to newly added messages. If you enable
`/loop on` while an LLM request is already in flight, Chord freezes and reuses
that request's already-prepared prefix for subsequent loop requests. This avoids
flipping old history from a reduced form back to full raw tool output, preserving
prompt-cache prefix stability; messages produced during the loop remain
unreduced until loop mode is turned off. Switching loop mode itself does not add,
remove, or rewrite stable system-prompt text. Changing the system prompt on a
loop toggle would invalidate prompt-cache reuse even when the underlying task
context did not otherwise change.

When the active main-agent provider uses the Codex rate-limit surface and a 5h
or 7d quota window has less than 10% remaining, Chord temporarily freezes the
LLM-facing request surface for continuous automatic continuations. The frozen
surface includes request-level reduction, the installed system prompt, and the
visible tool definitions. This is intentional: near quota exhaustion, Codex can
continue a `stop_reason=tool_call` chain until `end_turn` only when the context
surface is unchanged. Changing the context shape at that point can prevent Codex
from continuing after the quota is exhausted. The freeze is lifted at an
interactive boundary — when the agent returns to idle or the user sends a real
new message — so explicit user changes such as MCP or YOLO toggles can rebuild
the surface on the next request. If the key or running model changes, Chord also
allows the next request to rebuild the surface, because the previous frozen
surface no longer matches the active Codex identity.

### Reduction categories

Tool results are classified by output type and age.
Specialized summaries are tried before the generic stale-output fallback, so old
large outputs can keep high-value structure without changing durable session
history.

| Category | Typical examples | Age threshold | Size threshold | Rationale |
|----------|-----------------|---------------|----------------|-----------|
| Confirm / permission | Tool permission confirmations, user authorizations | `confirm_age_turns` (default 2) | — | Permission decisions become stale quickly |
| Errors | Failed tool results | `error_age_turns` (default 3) | — | Failure reasons may still be relevant, kept a bit longer |
| Shell success / logs | Successful commands, build/test/lint logs | `shell_success_age_turns` (default 1) | `shell_success_bytes` (default 3000) | Successful output is usually reproducible; summaries keep size, line count, salient success lines when present, and a tail fallback; the command remains available from the associated tool call; large logs keep key failures/warnings when summarized |
| Read-like | `read`, file content previews | `read_like_age_turns` (default 1), applied only to invalidated/superseded reads | `read_like_output_bytes` (default 3000) | A read overlapped by a later local edit/apply_patch (or followed by a whole-file/unknown-range mutation) is trimmed and marked `truncated=stale`; one covered by a later read of the same range is marked `truncated=superseded`. A read that is still the current view of its content is never trimmed, regardless of age or size — trimming it would force a re-read or, worse, an answer guessed from a summary |
| Search-like | `grep`, `glob`, LSP references | `read_like_age_turns` (default 1) | `read_like_output_bytes` (default 3000) | Hit lists are reproducible; summaries keep scope, counts, and representative hits |
| JSON / structured output | JSON from `shell` or structured tools | category-specific gate, then stale fallback | category-specific size gate | Large structured blobs keep top-level object keys or array counts before generic omission |
| Other stale results | Tool output not covered above | `stale_age_turns` (default 3) | `stale_output_bytes` (default 1500) | Catch-all fallback; most conservative to avoid losing hard-to-reconstruct data |

How to read the age and size parameters:

- `*_age_turns` keeps its configuration name, but the unit is an actual
  main-model request batch. Chord allocates a batch immediately before provider
  dispatch, so failed requests leave age gaps. Parallel tool calls and results
  from one assistant response share one batch and count as one round. Legacy
  sessions without batch metadata use a conservative user/assistant-response
  fallback.
- `*_bytes` is the **minimum output size in bytes** for that category to be
  eligible for trimming. Smaller outputs stay intact — short output doesn't
  need reduction.
- A `read` output that is still current — its displayed range has not been
  overlapped by a later edit/apply_patch, its file has not been replaced or deleted,
  and no later read covers the same range — is **never trimmed**, regardless
  of age, size, or how many other reads share the context. Such an output is
  the model's only current view of that content; trimming it forces either a
  redundant re-read (extra rounds, broken prompt cache) or an answer guessed
  from a summary. Capacity pressure is durable Compaction's job, not
  reduction's: every read result is already bounded by the read tool's own
  per-call output budget, so retained reads grow the prompt linearly and
  Compaction archives them once the threshold is reached. Successful `edit`
  and `apply_patch` calls already retain their applied delta in the tool-call
  arguments; their results therefore keep only the application summary and
  diagnostics instead of echoing the changed text. Legacy sessions or
  mutations without a reliable changed range conservatively invalidate all
  reads of that file.
- `min_tool_results_prune` (default 6) is a **safety gate** for the generic
  stale-output fallback: once a result is old enough and large enough for that
  catch-all path, Chord still waits until the conversation has at least this
  many tool-result messages before applying the generic stale trim. Category-
  specific paths such as shell-success, read-like, search-like, JSON, and
  build/log summaries still follow their own age/size rules. This setting does
  not control request-batch age.
- `wrap_up_grace_requests` (default 1) protects the next main-model request
  after `todo_write` reports all TODOs completed/cancelled. It is counted in
  LLM requests, not user turns. The grace is skipped when the model changed.
- Recent high-risk outputs are protected regardless of the thresholds above:
  while fewer than `high_risk_protect_age_turns` request batches have passed,
  results that look like diffs,
  failed assertions, stack traces, or permission/security errors are kept intact
  even when they would otherwise be eligible for trimming. Parallel results in
  the same batch do not increase this age.

### Tuning guidance

Keep the defaults when prompt-cache stability matters and your sessions commonly
reuse the same active files across several turns. If your main problem is
hitting context limits quickly in tool-heavy sessions, lower the byte
thresholds, for example `read_like_output_bytes: 2500`. A cost-first setup can
also lower the high-risk protection window:

```yaml
context:
  reduction:
    high_risk_protect_age_turns: 1
```

| If you see this... | Try this... |
|--------------------|-------------|
| Prompt-cache reuse is good but medium reads/logs still change the request prefix too often | Raise `read_like_output_bytes` and `shell_success_bytes` further |
| Short conversations with many tool results hitting limits | Lower `min_tool_results_prune` (e.g. `4`) |
| Permission confirmations dominating the prompt | Lower `confirm_age_turns` (e.g. `1`) |
| Build/test logs are important context to keep | Raise `shell_success_bytes` further (e.g. `16000`) |
| File contents often need to be revisited | Nothing to tune: still-valid reads are always retained until invalidated or superseded |
| Final answers after TODO completion cost more because the prompt cache was disturbed | Keep `wrap_up_grace_requests: 1`; use `2` only if your workflow usually needs one extra verification request after TODO completion |
| All tool output is important, nothing should be dropped | Raise all `*_age_turns` and `*_bytes` globally |

## Related

- [Configuration & Auth](./configuration.md) — configuration files, layers, and the full schema cheatsheet
- [Usage — `/compact`](./usage.md#local-slash-commands)
- [Performance](./performance.md)
- [Troubleshooting](./troubleshooting.md)
