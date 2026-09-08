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
| Writes to disk | ✅ Rewrites session files | ❌ Conversation unchanged (archives dropped payloads) |
| Uses an LLM | ✅ (configurable model pool) | ❌ (heuristic rules only) |
| When it fires | Threshold is reached and a main-model request is about to start / manual `/compact` / error recovery | Before every LLM request |
| Typical latency | Seconds to tens of seconds (waits for LLM) | Milliseconds (in-memory rule matching) |
| User visibility | TUI shows "Compacting context..." progress | Silent (invisible) |
| Loop mode | Enabled; compaction still runs so long sessions can continue | Enabled; loop mode does not change reduction, see [Loop mode](#loop-mode) |

**How they work together**: Reduction is the lightweight first line of defense —
it trims stale tool output before every request, slowing down context growth.
When reduction alone is not enough and the context keeps growing past the
compaction threshold, compaction steps in for a deep compression pass. Most
users only need to care about compaction settings; reduction defaults are
already tuned for common usage patterns.

Before handing history to the summarize model, compaction applies the reduction
rules to it to keep that call affordable, and it follows your configured
reduction settings: a session that raised its retention thresholds also gets a
durable summary built from the larger retained input. The `history-N.md` archive
is unaffected and always holds the full, untrimmed original.

Automatic compaction is primarily driven by provider-reported input usage.
Request-level reduction may make the current prompt smaller, but local estimates
from that reduced prompt do not cancel a compaction request that was already
triggered by provider usage. If a provider or gateway later stops reporting
usage (or reports `input_tokens: 0`), Chord can use the last trusted non-zero
usage sample and current context-contributing message bytes as a conservative
fallback signal for the same automatic threshold.

When a normal main-model response ends with `stop`, reaching the threshold does
not by itself start a new compaction while the agent returns to idle. Chord
keeps the automatic request armed and starts compaction at the next
continuation barrier while preparing the next main-model request. The request
and compaction may run in parallel; an oversized request is suspended until
the compaction applies. If compaction was already running before the response
stopped, Chord does not cancel it; its ready draft is still applied at the next
safe continuation or idle barrier.

## Context compaction

When the main conversation approaches the model context limit, Chord arms
automatic context compaction and starts it while preparing the next main-model
request. The compaction process calls an LLM to analyze the current
conversation, generates a structured summary (covering goals, progress, key
decisions, file evidence, etc.), archives old messages, and replaces the
conversation history with the summary. The compacted session is persisted to
disk.

Every checkpoint opens with a **session anchors** block: the original request
that started the session, plus the standing constraints extracted from your
corrections. Compaction is recursive — each run re-summarizes the previous
checkpoint — so anything left to the summarizer erodes a little every round.
Anchors are exempt: they are copied forward verbatim from the previous
checkpoint instead of being regenerated, and the summarizer is told not to
restate or contradict them. The constraint list is bounded; on overflow the
earliest entries (usually project-wide ground rules) and the newest ones are
kept while the middle is dropped.

Constraints the session later contradicted are not silently dropped: the
newest instruction supersedes the older constraint, and the superseded entry
stays visible in the anchors block with a `~` prefix so the model can see the
direction change. Declarative constraints you state in a plain message — for
example "keep the existing API behavior" — get the same anchor authority as
imperative corrections, because they too are standing instructions that would
otherwise erode over repeated compactions. The checkpoint also lists the most
recently archived `history-N.md` files with their content topics as a **history
map**, so the model can read the exact archive back with the read tool when it
needs the original wording instead of guessing which file to open. Older entries
collapse into a single count so the map cannot grow without bound; their names
follow the same `history-N.md` pattern and stay readable. Each archive also
starts with a short **message index** (one line per message segment: start
line, block kind, first-line snippet, `LARGE` marker for oversized tool
output); the checkpoint tells the model to read the index first and then only
the line ranges it needs, so exact-history lookups no longer mean re-reading
whole archives through truncation.

Continuation-oriented compaction keeps a safe recent tail as verbatim messages
after the checkpoint. It prefers whole user turns (normally the latest two)
within a token budget of about 5% of the context window; when even a single user
turn exceeds that budget — the usual case once that turn carries a full tool loop
— it falls back to the longest safe suffix that does fit rather than dropping the
tail entirely. Tool-call/result pairs are never split, and short histories fall
back to summarizing the full safe head when preserving the tail would leave too
little material to summarize.

### Retained recent messages

Every checkpoint also embeds the newest real user messages from the archived
head verbatim — plus a dangling interrupted assistant reply when the
conversation ends on one — as a `## Retained Recent Messages` section inside
the checkpoint, within a small estimated-token budget (`retain_recent_tokens`,
built-in default 4096). Continuation profiles keep the most recent turns as raw
messages below the checkpoint; the retained section covers the messages just
before them, and for `archival` profiles — which keep no raw tail and are
otherwise summary-only — it is the only verbatim remnant of the latest
instructions. When a model-driven archival checkpoint has no live tail, the
assistant text that declared the `compact_context` call is kept the same way,
so the model's own analysis written just before the reset survives into the
new window. Retention never substitutes for the summary: it only pins the
newest instruction boundary so the continuation can resume without re-reading
the archives.
After a checkpoint applies, the continuation guidance states the precedence
explicitly: when the checkpoint conflicts with a newer source, the newer one
wins — latest user message or Done rejection, then current runtime state
(todos, subagents, background tasks), then the files on disk, then tool results
still in this conversation, then archived artifacts, and only then the
checkpoint's own text. A summary is navigation and candidate working memory; it
never becomes the authority for runtime, file, or transcript facts.
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
| `threshold` | float | `0.8` | Context usage ratio that triggers automatic compaction. Range `0`–`1`, e.g. `0.8` means trigger when usage reaches 80% of the usable input budget. Set to `0` to disable automatic compaction. Values outside `0`–`1` (negative, above `1`, or NaN/±Inf) are rejected with a warning and fall back to the built-in default. |
| `model_pool` | string | clone current agent pool | Name of a dedicated model pool for compaction. Prefer a large context window over raw cost: the summarize input is trimmed to fit the compaction model's own window, dropping the **earliest** archived messages first, so a small-window model can leave the summary blind to how the session started. A fast, cheap model with a large window is the ideal choice. |
| `reserved` | int | `0` | Fixed token headroom added on top of the proportional headroom left by `threshold`, for tokenizer drift, tool schema overhead, and compaction/recovery safety. Usually omit it (leave it at `0`); a non-zero value is subtracted from the input budget before applying `threshold`. |
| `preset` | string | auto-detected | Force a specific compaction implementation. Usually unnecessary. |
| `profile` | string | `auto` | Compaction strategy. Usually unnecessary. |
| `reminder` | float | `0` (derived) | Context-pressure reminder line as a usage ratio. `0` (the default) derives the line as `min(0.60, threshold × 0.90)`; a fraction in `(0,1]` sets it explicitly; `-1` disables the pressure reminder while keeping automatic compaction on (per model as well). A reminder below the threshold fires when usage reaches it (`min(reminder, threshold)` — whichever line comes first); one at or above the threshold does not fire separately, because usage only reaches it on requests that already crossed the threshold, which carry the grace "compaction imminent" notice or the externalization warning instead. `threshold: 0` disables both. Any other value (negative, above `1`, or NaN/±Inf) is rejected with a warning and falls back to the derived default. |
| `model_driven` | bool | `false` | Experimental opt-in: expose the `compact_context` tool to the main agent so the model can request a durable context checkpoint once it has externalized its working state (written it into files or structured arguments). The checkpoint is built deterministically without a summarization model call, applies at a tool-batch barrier that pauses the next main-model request, and continues the same turn on the compacted context. The tool is MainAgent-only, must be called alone, and only references `state_files` paths without reading them. Low-gain requests are skipped automatically. Off by default; enable only for projects where long exploratory sessions benefit from explicit resets. |
| `retain_recent_tokens` | int | `4096` (built-in) | Estimated-token budget for the newest real user messages kept verbatim inside every compaction checkpoint (see [Retained recent messages](#retained-recent-messages)); `0` or omitted uses the built-in default. Only the message text counts toward the budget. Set it higher to keep more of the latest turns across a compaction, or lower to reclaim more context; the retained section never replaces the summary — it pins the newest instruction boundary verbatim. |

Per-model overrides live on the model definition (`ModelConfig.compaction`,
with `threshold` and `reminder` subfields), so `model_templates` can share them
through `<<:`. There is no `context.compaction.models` map.

`threshold` and `reminder` (global or per-model) drive the usage-driven
compaction path and the context-pressure reminder for **all** users; they do
not depend on `model_driven`, which only registers the `compact_context` tool.
The TUI context usage display (sidebar Context value/gauge and the status-bar
percentage pill) uses the same two lines for its colors: green below the
reminder, orange/yellow from the reminder up to the threshold, red at the
threshold. The request-side reminder and warning overlays, by contrast, are
only injected while `model_driven` is enabled (see below).

Set the global lines under `context.compaction` and tune per model on the model
definition itself (`ModelConfig.compaction`). Model templates make this
reusable: define a template that carries both `limit` and `compaction`, then
reference it from every provider that serves that model.

```yaml
context:
  compaction:
    threshold: 0.65          # global automatic-compaction line (fallback)

model_templates:
  luna-cost-first: &luna-cost-first
    limit: {context: 1050000, output: 128000}   # full API window: no `input`
    compaction: {threshold: 0.25, reminder: 0.2}   # stay under the 272K long-context pricing tier

providers:
  openai:
    models:
      gpt-5.6-luna: *luna-cost-first
      gpt-5.6-sol:
        limit: {context: 1050000, output: 128000}
        compaction: {threshold: 0.55}   # quality-first
```

A model without a `compaction` block inherits the global
`context.compaction.threshold` / `reminder`. A model-level `threshold: 0`
disables automatic compaction (and reminders) for that model only, and a
model-level `reminder: -1` disables the pressure reminder while automatic
compaction stays on.

When the automatic-compaction threshold is crossed, Chord starts the
usage-driven compaction right away — it runs asynchronously in the background
and applies at the next continuation barrier, so the request that crosses the
line keeps running in parallel with it. Provider rejections (oversize) still
force compaction immediately.

While model-driven compaction is enabled (`compact_context` visible), the
first crossing in a compaction window instead defers the start across two
main-model requests: the first request after the crossing and one more run
before the summary-based compaction takes over, giving the model room to wrap
up the phase and request a model-driven checkpoint or externalize state. Every
request inside the deferral carries a "compaction imminent" notice with the
true remaining countdown — two requests left on the crossing request itself,
one on the final round — so the model always sees how much room is left even
if it never saw the crossing notice (a one-shot notice would be gone by the
time the model reached the final round). The grace is skipped or cut short
once usage reaches 95% of the usable input budget (a single batch that pulled
in large tool output cannot ride the grace into a provider oversize rejection)
and ends as soon as a model-driven request settles without applying (skip /
failure / cancel) after the crossing: the model already took its shot, so the
safety net takes over on the next gate. A request that settled while usage was
still below the threshold does not spend the grace. The grace is spent once
per window; any durable apply, session switch, restore, or model change starts
a fresh window.

The request-side reminder and warning overlays only fire while model-driven
compaction is enabled; with it off, automatic compaction is fully
runtime-owned — as in Codex's local and remote compaction paths, which never
notify the working model — and the session simply keeps running until the
compaction applies at the barrier. The reminder only applies while the session
keeps issuing main requests — if the turn ends right at the crossing, the
usage-driven compaction runs through the normal end-of-turn path instead. The
one-shot externalization warning is shown on the request that actually starts
the compaction, which under the grace period is not the first request after
the crossing.

Switching models applies the new model's per-model thresholds and starts a
fresh reminder window. When the switch lands on a model with a smaller context
window while the current context already crosses its line, Chord compacts
ahead of the move: from idle the automatic compaction starts right away, and
with a turn active the next main-model request is deferred until the
compaction applies, so a request never runs over the new model's threshold
right after the switch. A one-line status notice reports the downshift
compaction once it applies.

### Model-driven context checkpoint (experimental)

When `context.compaction.model_driven: true`, the main agent gains the
`compact_context` tool. Registration is part of enabling the feature, so
wildcard-only permission rules — such as an allowlist's `"*": deny` plus a few
explicitly allowed tools — never hide the tool or block its calls; only a
non-global tool rule whose pattern matches `compact_context` still applies
(`deny` removes the tool and reports a one-time diagnostic, `ask` keeps it
behind confirmation, and `allow` matches the default). Narrow patterns such as
`compact_*` count as matching rules. A role whose allowlist grants no file-writing
tools can still checkpoint its state in the structured arguments. The model calls it alone (no sibling tool calls in the
same response) once its working state is fully externalized — the facts it
needs later are written into files named in `state_files`, or fully expressed
in the structured `active_objective` / `completed` / `decisions` /
`open_issues` / `next_step` arguments. The runtime validates the request,
waits for the tool batch to close, then:

1. snapshots the conversation and archives the head (no summarization model
   call — the checkpoint is deterministic),
2. refuses the reset when the projected savings are below a conservative
   low-gain gate (2048 tokens and 10% of the prepared surface), or when fewer than three
   main-model requests have passed since the last applied checkpoint,
3. applies the checkpoint atomically, preserves anything appended after the
   snapshot as a live tail, and continues the same turn on the compacted
   context.

An automatic compaction never locks the model out of its checkpoint. When a
usage-driven compaction is already running (a threshold crossing started its
background worker, or its draft is ready and waiting at the continuation
barrier), the request running alongside it may still submit `compact_context`.
The model chose that boundary on purpose, so its checkpoint wins: the runtime
discards the automatic draft and applies the model's checkpoint instead.
Automatic compaction is the fallback, not a lock — a threshold crossing never
takes the reset away from a model that is wrapping up. The one-shot
externalization warning does not mention this override (the model does not
need to know an automatic compaction is running, only that the current context
is ending soon); a voluntary checkpoint made earlier on its own initiative
keeps working exactly as before.

A skip is a normal policy result: retrying the same request immediately is
cooled down briefly and does not change the outcome — the model should wait or
move on. When context usage stays above the reminder line, requests carry a
context-pressure reminder: the full text once per compaction window, then a short,
self-contained line restating the action — the reminder is a transient overlay
rebuilt on every request, so a repeat cannot assume the full text is still in
context — and telling the model to prepare for the compaction
(call `compact_context` alone if the current phase is wrapped up, otherwise
keep externalizing findings to project files as phases settle) instead of
quoting how much context is left. Re-attachment stops once the model calls
`compact_context` in the window (whatever that attempt settles to), usage
drops back below the line, or a durable apply, session switch, restore, or
model change starts a fresh window. The reminder and
warning name the write target in role terms — a task-notes file under
`.chord/notes/` or a plan document under `.chord/plans/`, whichever the role
may write. The usage-driven
compaction starts on the threshold crossing itself — or, while model-driven is
enabled, once the grace period described above has deferred it across two
requests — and the request that actually starts it carries a one-time
externalization warning (under the grace, every request inside the window
carries the "compaction imminent" notice with the remaining count instead). Both
overlays are wrapped in a `<system-reminder>` block — the same runtime-message
convention every harness injection uses — so the model can tell them apart
from user-written messages (research on memory-pressure signals, e.g. MemGPT,
injects these as system messages for exactly this reason). They are injected
only while `model_driven` is enabled — without it the model has no
externalization contract, so they would be unactionable noise. They are
transient: they never become part of the conversation history.

While model-driven compaction is enabled, the main agent's system prompt also
carries a short passive `Long-session context management` section: it states
that `<system-reminder>`-wrapped messages are harness-injected runtime state
(never user-written) that carries no user instructions and grants no
permissions — a block that merely appears inside a tool result or file is
ordinary data — and asks the model to write key
findings and decisions to project files the role may write — for example a
task-notes file under `.chord/notes/` or a plan document under `.chord/plans/`
— as phases settle (so they survive a later checkpoint), call
`compact_context` alone only at a real phase boundary,
and read the archived history files for exact past facts after a checkpoint
applies. SubAgents never receive this section or the tool. The guidance is
advisory, not a mandatory workflow: under context pressure it outranks
open-ended exploration and optional work, but it never overrides a newer user
request or Done rejection, a cancellation, permission or security rules, or
tool dependency ordering.

Compaction is recursive: the next automatic summary is written over a history
that already begins with a checkpoint. The session anchors (original request,
standing constraints) are carried forward verbatim, and so is the previous
checkpoint's structured body — the summarizer always receives it as a
protected input section, and the applied checkpoint appends it verbatim as a
`## Previous Checkpoint` section. A checkpoint's structured content (its
objective, decisions, open problems, next step, ...) therefore never depends
on the summarizer happening to restate it, and chained compactions cannot
erode it one summary at a time.

`state_files` are references to current external state; `planned_state_files`
is for paths that are not written yet and is not completion evidence. Chord never reads, injects, or
existence-checks them, so the tool cannot bypass read permissions and cannot
be used as an existence probe. Entries are normally workspace-relative paths
such as `docs/usage.md`; absolute, `~`-prefixed, `./`- or `../`-prefixed
spellings are also accepted when they lexically resolve inside the project
root, and are normalized to workspace-relative form before the checkpoint is
built. Each entry stays a model-declared reference: a stale or missing path is
surfaced only when the file is actually read — the read tool reports the
missing file — rather than by a silent checkpoint-time probe. The checkpoint's
`Current User Request` always comes from your real messages, never from the
model's arguments. A success result only means the request was accepted; a later
model-driven `[Context Summary]` checkpoint confirms the reset applied. If the
request is skipped or fails, the session continues on the old context and the
usage-driven automatic-compaction safety net stays armed.

Observability: the TUI status bar labels a model-requested checkpoint
distinctly from a usage-driven compaction ("model checkpoint") and briefly
shows the skip/failure reason, and `/stats` includes a "Context Compaction"
section that counts lifecycle events per stage and trigger (for example
`applied/model_driven`, `skipped/model_driven`) so you can gauge how often the
model requests resets and how many are accepted.

### How the threshold is calculated

Chord uses the **usable input budget** as
the baseline. If the model config sets `limit.input`, that value is used
as-is; otherwise Chord derives it as `limit.context` minus the model's own
`limit.output` — the provider-published input allocation (e.g. the Codex
400K-window/128K-output pair yields a 272K budget). Only a model declaring no
`limit.output` falls back to reserving the effective default output cap
(`max_output_tokens`, default `64000`). If `reserved` is set, it is subtracted
first. The effective
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
prompt — it never rewrites the conversation stored on disk.** Decisions use tool
type, actual main-model request batches, size, and local validity state. Context
usage affects durable compaction only and cannot change the reduction surface.

**Every lossy summary leaves a recovery address.** When reduction summarizes a
payload larger than 2000 bytes, it first writes the full output to the session's
`reduced-artifacts/` directory and appends a `Full output saved to <path>`
reference to the marker, so nothing this layer drops is unrecoverable. Archives
are content-addressed: identical payloads share one file, so repeated copies of
the same output do not each cost a write. Summaries that already carry their own
recovery route are exempt, because an extra copy would buy nothing: a superseded
read points at the newer copy, a read invalidated by an edit or a patch can be
re-read for the parts that did not change while the replaced text stays in the
edit's own arguments, diagnostics keep their structured body, and a confirmation
has no payload. A read invalidated by a whole-file write, a delete or a change
made outside the editing tools is archived instead — re-reading returns the new
content, so the version that was actually observed exists nowhere else.

### First-use tool-output budget

Before an ordinary tool result enters the conversation, Chord applies a separate
inline safety budget. A result that fits within the default 50 KiB budget is
returned verbatim on first use. A long line or more than 2,000 lines alone does
not force the model to reopen an otherwise small result; those limits only shape
the preview of an output that already exceeds the byte budget.

When a result exceeds the byte budget, Chord saves the complete output under the
session's `tool-outputs/` directory and returns a bounded preview with a stable
reference. The model should search or read only the omitted range it actually
needs. This execution-time safeguard is separate from context reduction: the
latter may summarize old results in a later request, while the former determines
whether the first result is inline and recoverable.

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
    diff_protect_age_turns: 12
    shell_success_age_turns: 2
    shell_success_bytes: 3000
    shell_read_only_age_turns: 3
    read_like_age_turns: 2
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
- Recent high-risk tool outputs are protected by request-batch age. Failures, stack traces, permission/security output, and active-work evidence use `high_risk_protect_age_turns: 4`; diff/patch evidence uses the dedicated `diff_protect_age_turns: 12` so long reviews retain the exact change until there has been time to form findings. Parallel tool calls and results from one assistant response share one batch and do not age one another.
- Successful shell output is treated as low risk once it is old enough and larger than `shell_success_bytes`. Chord keeps a compact summary with output size, line count, salient success lines when present, and a tail excerpt fallback; the shell command itself remains available from the associated tool call. Recent failures, stack traces, diffs, and warning-heavy build logs are routed through high-risk or structured-log handling before this success-output summary path; older outputs may later be summarized when they are no longer protected by the recent high-risk window.
- After a successful shell invocation that is not on the static read-only allowlist, Chord rechecks durable hashes for previously read files in the affected stable/recovered prefix. A confirmed replacement, deletion, or hash change marks the old read `truncated=stale`; unreadable paths or legacy reads without a durable hash are not guessed stale.
- Large old tool results are age/byte-pruned, but Chord preserves structured hints before falling back to generic omission: `read` keeps path/range metadata, `grep` / `glob` / LSP references keep query scope plus a byte-bounded location list with explicit omissions, JSON output keeps top-level shape/counts, successful shell output keeps size/salient-line context, diff/patch output keeps files, hunks, change counts and bounded representative lines, and build/test logs keep key failure or warning lines. Older errors, diagnostics, and confirmations are reduced to compact fixed markers or summaries.
- Reduction diagnostics keep the aggregate `reread_after_reduction` counter and additionally distinguish same-revision re-reads from changed-revision refreshes when both reads carry durable hashes.
- Re-fetch evidence feeds back into retention: when the model re-issues a call identical to one whose output was reduced earlier (a re-read, re-search, or read-only shell re-run), the newest output of that input becomes exempt from reduction for the rest of the session and the skip is recorded as `recalled_input_protect`. Older duplicates still collapse to repeated markers, a read known to be stale keeps its stale marker, and re-running a mutating command (such as a test) earns no exemption — that seeks fresh state, not lost content. The exemption set is in-memory session state; it is dropped with the reduction caches on restore or model switch and rebuilds from live evidence.

### Loop mode

Loop mode does not change how reduction works: requests made in loop mode go
through the same request-level reduction and stable-prefix reuse as any other
request. Switching loop mode itself does not add, remove, or rewrite stable
system-prompt text. Changing the system prompt on a loop toggle would invalidate
prompt-cache reuse even when the underlying task context did not otherwise
change.

On models that explicitly support Chord's request-only dynamic tool mounts
(`compat.chat_completions.mcp_system_tools_message` or
`compat.responses.mcp_additional_tools`), enabling `/loop on` during an in-flight
request may late-mount the `done` tool on the next loop request when the current
frozen top-level tool surface does not already include it. The late mount is
request-local and does not rewrite the frozen top-level tool definitions, so
turning loop mode on can preserve the existing prompt-cache boundary. If the
frozen tool surface already contains `done`, Chord does not inject a duplicate.
Models that do not support these request-only dynamic tool mounts keep the
existing behavior: if enabling loop mode requires a tool-surface change, the
next request may still lose prompt-cache reuse because the top-level tool
definitions changed.

### Reduction categories

Tool results are classified by output type and age.
Specialized summaries are tried before the generic stale-output fallback, so old
large outputs can keep high-value structure without changing durable session
history.

| Category | Typical examples | Age threshold | Size threshold | Rationale |
|----------|-----------------|---------------|----------------|-----------|
| Confirm / permission | Tool permission confirmations, user authorizations | `confirm_age_turns` (default 2) | — | Permission decisions become stale quickly |
| Errors | Failed tool results | `error_age_turns` (default 3) | — | Failure reasons may still be relevant, kept a bit longer |
| Shell success / logs | Successful commands, build/test/lint logs | `shell_success_age_turns` (default 2 — a result is already age 1 at the first request that can react to it, so the model always sees a fresh success in full exactly once); commands on the shell tool's read-only allowlist (`cat`, `ls`, `git log`, ...) use `shell_read_only_age_turns` (default 3) | `shell_success_bytes` (default 3000) | Successful output is usually reproducible; read-only commands are content fetches — the shell analogue of a read without validity tracking — so they get a longer window (identical re-calls arrive with a median gap of ~3 request batches in session data); summaries keep size, line count, salient success lines when present, and a tail fallback; the command remains available from the associated tool call; large logs keep key failures/warnings when summarized |
| Read-like | `read`, file content previews | none for reads — an invalidated/superseded read renders its validity marker as soon as the state is known (stale content is misleading at any age, so it skips the age gate and the protection branches); other read-like output waits for `read_like_age_turns` (default 2) | `read_like_output_bytes` (default 3000) | A read overlapped by a later local edit/apply_patch (or followed by a whole-file/unknown-range mutation) is trimmed and marked `truncated=stale`; one covered by a later read of the same range is marked `truncated=superseded`. A read that is still the current view of its content is never trimmed, regardless of age or size — trimming it would force a re-read or, worse, an answer guessed from a summary |
| Search-like | `grep`, `glob`, LSP references | `read_like_age_turns` (default 2) | `read_like_output_bytes` (default 3000) | Hit lists are reproducible, but the `path:line` list is what a multi-site task acts on — summaries keep the full location list (every matched file with its line numbers) within a byte budget, snippets only for the leading files, and an explicit omission tail beyond the budget |
| JSON / structured output | JSON from `shell` or structured tools | JSON documents wait for `stale_age_turns` (default 3) — the key/item skeleton is the lossiest summary and values are typically consumed over several requests; NDJSON log streams (e.g. `go test -json`) use the surrounding category's age | category-specific size gate | Large structured blobs keep top-level object keys or array counts before generic omission |
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
