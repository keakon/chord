# Changelog

This project follows Semantic Versioning-style releases. Before 1.0, releases may include breaking changes.

## Unreleased

### Breaking Changes

- MCP configuration scopes are now explicit: a same-name server in `.chord/config.yaml` atomically replaces the global server definition instead of recursively inheriting individual fields, while agent-level MCP entries are additive, auto-start only. A conflicting agent server name or agent-scoped `manual: true` now fails startup; remove the agent entry to inherit the server, rename it for an agent-private connection, or configure manual servers at the top level.
- Compared with v0.7.1 and earlier, `compat.reasoning_continuity.mode: openai_visible` now only replays assistant `reasoning_content`; it no longer injects GLM-specific `thinking.type` or `clear_thinking` fields. Configure provider-specific request differences with the new protocol-agnostic `compat.request_overrides`: `body` recursively patches JSON (`null` deletes a field), `rename_body_fields` preserves dynamically computed values under another key, and `headers` sets or removes request headers. Existing GLM Preserved Thinking configs should add `thinking: {type: enabled, clear_thinking: false}` under `request_overrides.body`; DeepSeek thinking configs should add `thinking: {type: enabled}` and rename `max_completion_tokens` to `max_tokens` where required.

### Improvements

- Delegated `expected_write_scope` is now enforced at tool execution instead of remaining advisory metadata: read-only tasks reject workspace mutations, any scoped task rejects arbitrary Shell execution, file/path scopes constrain native edit tools using canonical paths, nested delegation cannot broaden its parent scope, and admission serializes child limits, duplicate detection, scope conflicts, runtime registration, and parked-task reactivation.
- Quiescent SubAgents now release their event-loop goroutine, LLM client, context manager, and other hot runtime resources while retaining a durable task descriptor and transcript. Focus, history, and follow-up remain available; explicit user input, authorized targeted notifications, or relevant descendant mailbox events rehydrate a new runtime instance on demand. Failed or cancelled tasks require explicit user restart and cannot be revived by model notifications.
- Focused parked SubAgents keep their task-specific skill list in the TUI information panel, matching live focused workers.
- Headless now exposes reliable structured `agent_started`, `agent_notify`, and enriched `agent_done` events for delegated workstreams. SubAgent `assistant_message` payloads also include task, agent type, and parent-agent identity for downstream labeling and routing.
- Streaming `write`, `edit`, and `patch` cards now show their path as soon as the path field is complete, keep the received-character count while arguments continue arriving, and switch to their full content or diff preview only when argument streaming finishes.
- Sessions can now have a custom display title via `/rename <title>`; `/rename` clears it. The title is shown in the session picker and terminal title while the immutable session ID and on-disk directory remain unchanged.
- `delegate` permission patterns now match `agent_type` consistently across advertised targets, prompts, execution, nested delegation, and hook-modified arguments.
- The documentation stack now uses Astro 7 and Starlight 0.41 with updated Vite, devalue, js-yaml, yaml, and related transitive dependencies; duplicate documentation ID warnings from the old Astro content loader are gone, and the production dependency audit no longer reports known vulnerabilities.
- OpenAI Responses and Chat Completions requests now enable `parallel_tool_calls` by default so independent tool calls can be returned together. Model and variant configuration can explicitly disable it when a backend or workflow requires serial calls.
- Context-reduction defaults were retuned from recent-session statistics: old read-like and successful shell outputs are summarized after 1 effective turn, read-like and shell-success byte gates default to 3000, generic stale cleanup starts after 3 effective turns, `min_tool_results_prune` defaults to 6, and `min_incremental_saved_tokens` defaults to 2048. Older successful shell output now keeps output size, line count, salient success lines, and a tail fallback instead of a fixed omission marker.
- `preset: codex` Responses requests no longer maintain a Chord-local reasoning-effort whitelist. Chord now normalizes `reasoning.effort` and passes it through so model-specific values such as `max` can be validated by the upstream backend instead of being silently dropped client-side.
- The setup wizard now configures GPT-5.6 Sol/Terra/Luna and selects `gpt-5.6-sol` as the initial Codex OAuth model; model configuration recipes document GPT-5.6 and other common providers.

### Fixes

- Fixed SubAgent transcript persistence failures being reduced to a transient log warning. Persistence health is now stored with the durable task, degraded workers checkpoint their complete transcript before parking or shutdown, and abandoned LLM clients cancel their owned background work. Concurrent admission also merges with the latest task registry and honors caller cancellation through its final durable commit point.
- Fixed provider responses with excessive parallel tool calls being able to exhaust the main-loop follow-up reserve or block escalation behind a full external event queue. Loop-owned causal events remain non-blocking and ordered, oversized responses now fail normally at a documented per-response limit, and multipart text or attachment payloads count toward context-recovery token budgets.
- Fixed oversized SubAgent contexts failing permanently after model fallback. A strictly classified context-length error now triggers one bounded local transcript recovery and automatic retry while preserving task identity and recent high-value context; a second oversize error terminates normally instead of entering a recovery loop.
- Fixed SubAgent mailbox messages and acknowledgements becoming visible or consumed before their append-only logs were durably updated. Non-progress messages now persist before task/UI routing, failed writes remain retryable in memory, and consumed/reply state advances only after the ack log succeeds.
- Fixed concurrent delegation crossing `/new`, `/resume`, fork, plan execution, shutdown, caller cancellation, or parent completion and registering a worker on the wrong lifecycle track. Session transitions now pause admission and invalidate initialization that started against an older epoch; all transition exits reliably reopen admission.
- Fixed long-running delegated sessions retaining unbounded event overflow, terminal task registry entries, and consumed mailbox history. Event bursts now use bounded backpressure with safe progress coalescing, while durable terminal tasks and mailbox logs are compacted without discarding resumable or unconsumed state.
- Fixed failed or cancelled parent tasks leaving joined descendants running or restored under a terminal owner; joined descendants are cancelled recursively, independent children are reparented to main, and inconsistent restored trees are repaired before activation.
- Fixed compact TUI tool results such as `grep` using logical newline count as their collapsed height budget. Long matches now count their soft-wrapped screen lines toward the 10-line preview limit, while expanding the card still shows the complete result.
- Fixed concurrent MainAgent/SubAgent streaming splitting one assistant response into multiple visible cards. Assistant text, thinking state, rollback, and tool-call boundaries are now isolated per agent, so hidden background tool events cannot settle the foreground response.
- Fixed global idle detection after delegation: the terminal title no longer shows completion while any SubAgent is connecting, retrying, streaming, or executing; completion is shown only after all Agents are idle.
- Fixed TUI unresponsiveness when switching to a SubAgent with a large transcript. Focus switches now reuse bounded transcript windows and initially load only the tail, while small transcripts remain fully visible.
- Fixed empty MODEL information after resuming and focusing a parked SubAgent. Model refs are now persisted in task, meta, and recovery records; legacy sessions recover them from the usage ledger and fall back to the latest Agent configuration and current model.
- Fixed TUI hangs when pressing Enter to continue from a parked SubAgent view. Transcript loading, SubAgent rehydration, and continuation requests no longer run synchronously on the TUI update path.
- Fixed infinite recursion when the info panel queried invoked skills after rehydration: a SubAgent no longer calls back into MainAgent's focused skill router. Skill discovery is shared as a workspace catalog, while each Agent applies its latest permissions and independently records and restores invoked state.
- Process stderr is now bound directly to the current rotating log file instead of being piped back through golog. Runtime panics and stack overflows can write complete diagnostics and exit instead of hanging forever when no Go goroutine can consume the pipe during a fatal stop.
- SubAgent restore now treats the durable task as the canonical UI identity. Historical runtime instances contribute transcript segments but are no longer exposed as duplicate sidebar Agents or independent focus targets.
- Queued input for a busy SubAgent now remains blocked until the event loop has consumed the completed LLM response, preventing a newer turn from making the valid response appear stale and discarding it.
- SubAgent mailbox acknowledgement state is loaded once per session and updated with ack writes instead of rereading the complete JSONL log for every live mailbox event.
- `response_header_timeout` now bounds the complete pre-response phase, including connection setup and request-body upload, while still stopping at response headers so healthy streams are not subject to a total request timer.
- Resuming a session now restores every SubAgent as a lightweight parked task and reloads mailboxes without dispatching them or starting requests, LLM clients, or event-loop goroutines. Restored mailboxes are delivered only when the user explicitly continues or submits input to the owning MainAgent/SubAgent; live mailbox events still dispatch normally and wake the owning task. Completed, failed, and cancelled tasks remain focusable with the same transcript and stable task ID; rehydration creates a new runtime agent ID and reports the previous ID. Their composer stays available, and request progress/model status is keyed by exact runtime identity so background agents—including names beginning with `main-`—cannot make the current agent appear busy.
- `Shift+Tab` now always keeps the main-agent view reachable. SubAgents that stop without completing remain in the view cycle, and a stale SubAgent focus falls back to main instead of becoming an unresponsive view.
- Cancelling a turn with `Esc` now keeps that turn's user prompt visible when the preserved interrupted reply is taller than the viewport. Restoring a session ending in the same state also opens at the prompt instead of the bottom of the partial reply, while completed replies retain the existing tail-follow behavior.
- Focused SubAgent status bars now report received response bytes and events instead of remaining at `0 B` while tool arguments stream.
- Restored idle SubAgents can now continue from their existing context after being focused, matching idle MainAgent behavior. Chord reacquires the SubAgent concurrency slot, restores its running state, and starts a new turn without adding a synthetic user message.
- TUI card numbers are now scoped to the viewed agent, so the main transcript and each SubAgent transcript independently start at `#1`. Switching agent views rebuilds the complete available history—including earlier instances of rehydrated delegated tasks—so older cards remain reachable with page-up, top navigation, search, and the message directory instead of leaving a partial live tail.
- Subagents now queue promoted speculative tool results directly on their event loop instead of synchronously filling their own fixed-capacity result channel. Parallel batches larger than eight calls no longer deadlock the scheduler, and promoted results retain batch order without spawning one blocked goroutine per result.
- When the currently viewed subagent finishes, the TUI now returns to the main-agent conversation and merges the completion summary into its Delegate card. Subagents that finish in the background do not steal focus from the view currently being inspected.
- Restored interactive `!` commands now return as `TERMINAL` cards instead of ordinary user messages, including compatible legacy session records. Their model-visible context records the executed `command` and its `output` once, without repeating the original `!command` input or large-paste placeholder.
- The TUI `USAGE` block now calculates the `Bytes` reduction percentage by comparing the current request’s unreduced context with the context actually sent. Frozen summaries reused by incremental reduction still count toward the current request’s savings instead of reporting only newly reduced bytes.
- Retry diagnostics now keep persistent logs free of API key prefixes by recording only a suffix plus a stable one-way fingerprint, while the in-memory TUI error panel uses a separately generated masked label for human identification.
- Transcript search now validates visible rendered text across card types, expands collapsed matches, handles Markdown formatting and HTML entities, and avoids hidden or truncated false matches.
- Done tool guidance now makes direct assistant text the mandatory default for ordinary completion. The model may call `done` only when the active runtime or workflow explicitly requires a tool-based completion signal, such as a loop exit, and the report schema no longer implies that every finished task requires the tool.
- Newly created session directories and internal persisted artifacts now use owner-only permissions (`0700` for directories and `0600` for files), preventing transcripts, snapshots, subagent mailboxes, artifacts, task state, and compaction history from being exposed to other local users by default.
- TUI text paste no longer probes clipboard images on the Bubble Tea update path: ordinary terminal paste and `Cmd+V` are text-only, while `Ctrl+V` / `Alt+V` asynchronously attaches images or PDFs through native clipboard backends without `osascript`, preventing intermittent multi-second input freezes. Clipboard PNG/JPEG are supported directly, and BMP/WebP are normalized for Windows, WSLg, and Linux compatibility.
- OpenAI Responses and Chat Completions usage now records GPT-5.6 cache-write tokens in a separate bucket without double-counting them as ordinary input, keeping context thresholds and cost analytics accurate.
- MCP/LSP integration status, controls, system prompts, and model tool definitions now honor active-role and live `/rules` permissions consistently, including partial permissions within one MCP server, exact allows for lazy/manual servers, and overlapping MCP server names.

## 0.7.1 - 2026-07-08

### Breaking Changes

- **Model input modality default:** `modalities.input` now defaults to text-only when unset, instead of the previous `[text, image]`. Models that accept images or PDFs must now declare `modalities.input: [text, image]` (add `pdf` if supported) explicitly; configs that relied on the implicit image default will have image attachments dropped before the request is sent. Update any vision-capable model entries that previously omitted `modalities.input`.
- **Reasoning continuity defaults:** OpenAI-compatible `type: chat-completions` models no longer implicitly replay assistant `reasoning_content`. Enable `compat.reasoning_continuity.mode: openai_visible` only for endpoints that explicitly require visible reasoning replay, such as GLM Preserved Thinking. `type: responses` targets also no longer convert historical reasoning into visible `output_text`; they continue to rely only on Responses-native continuity surfaces.

### Features

- Added `preset: azure` for Azure OpenAI Responses providers, including Azure `api-key` authentication, Azure-compatible Responses headers/default `store: true`, official-API 400 handling, setup-template support, and docs/examples for `/openai/v1/responses`. Provider type auto-detection now also uses the URL path suffix while ignoring query strings and fragments, so endpoints with `?api-version=...` are detected correctly.
- Added provider-level `auth_scheme` so compatible endpoints can override the credential header independently from the request transport, supporting `anthropic-api-key`, `bearer`, and `api-key`.

### Improvements

- Improved prompt-cache stability for long agent loops: dynamic environment data now lives in the session-context reminder instead of the system prompt, already-reduced request prefixes are frozen across incremental reduction, and Anthropic explicit cache breakpoints can target the frozen reduced-prefix boundary.
- Thinking translation now validates model output more strictly, rejecting symbol-only or excessively compressed translations, and runs after assistant thinking is durably recorded instead of during streaming so rollback/retry paths do not leave stale translated thinking.
- TUI streaming renders assistant/thinking deltas with less per-token cache invalidation, reducing redraw work during long streamed responses.
- The resume session picker now opens faster for projects with many sessions by rendering a lightweight initial list, reusing cached session summaries, and filling exact message counts and missing previews in the background.
- File tools now offer broader not-found path suggestions, including whitespace-repair hints for common model-generated paths and the same suggestion flow for `read`, `view_image`, `edit`, and `patch` while preserving patch's create-file guidance.
- Native file tools now notify matching LSP servers of workspace file Created/Changed/Deleted events before syncing text documents, so Pyright, TypeScript, gopls, rust-analyzer, and similar servers refresh project/module graphs more promptly and post-tool diagnostics are less likely to show transient unresolved imports for newly created files.
- Tool path defaults are now anchored to the session working directory instead of implicit process cwd: relative file paths, omitted `shell` / `spawn` workdirs, and omitted `grep` / `glob` roots use the same base that is injected into the model before the first user message and after compaction. Tool cards can display paths relative to that base while raw tool-call arguments remain unchanged for audit/export.
- TUI file mentions now support 1-based line suffixes such as `@path:42` and `@path:10-20`, injecting only the requested text-file lines while preserving typed line suffixes when accepting completion.

### Fixes

- Tool-result truncation now preserves recoverability: `question` answers are no longer truncated before being sent to the model, per-line truncation saves the complete tool output under the session `tool-outputs/` directory, `read` keeps line paging as the focused code-reading path, LSP tool positions now use the same Unicode character counting for both input and returned locations, and internally budgeted outputs from `read`, `glob`, and `web_fetch` now include a saved-full-output reference when Chord can produce the complete result.
- Automatic compaction now has a usage-missing fallback: after a trusted non-zero provider usage sample, Chord scales that sample by current context-contributing message bytes when later responses omit usage or report zero, so long sessions can still compact before hitting the provider limit. If every attempted candidate model reports `context_length_exceeded` while automatic compaction is disabled, Chord now stops with an actionable error instead of falling through to generic fallback exhaustion.
- Compaction summaries now strip a leading orphan `</think>` close tag (emitted when a provider streams reasoning on a separate channel but leaks the closing tag into visible content) instead of forcing a summary repair retry; genuinely ambiguous cases such as an unclosed leading `<think>` or inline mid-body tags are still rejected and repaired.
- Restored or cross-provider histories now skip empty or unreplayable reasoning-only assistant messages, preventing provider API rejections when replaying prior reasoning/thinking content.
- TUI streaming now flushes buffered thinking deltas before tool-call cards, preventing spurious extra thinking cards when providers interleave thinking and tool-use events.
- Patch tool now tolerates the common model-generated `@@` no-op anchor pattern: a context-only hunk (only unchanged lines) is accepted as a no-op anchor that matches the file and advances the search position for later hunks, as long as the overall patch has at least one `+`/`-` change. A compatibility note is appended only to the model-visible tool context (not the TUI display) so models learn the preferred single-hunk form without retry cost. Tool descriptions and error messages now clearly separate the context marker space from source indentation and give pure-insertion examples. A patch with only unchanged context lines is still rejected with an actionable message.
- Patch tool now recovers from model-generated malformed trailing `*** End Patch...` footers only after strict parsing fails, while preserving valid hunk context and continuing to reject other top-level `*** ...` operations. Its model-facing format guidance is shorter and emphasizes direct single-file `@@` hunks instead of Codex `apply_patch` envelopes.
- Text truncation paths used by compaction, hooks, subagent prompts, and skill listings now preserve UTF-8 rune boundaries, preventing corrupted history or model-facing text when Chinese/Japanese/emoji content is truncated.
- When fallback or replay targets cannot accept image/PDF content, Chord now drops the unsupported binary parts before sending the request instead of failing over the whole request or forwarding attachments that the target cannot process.

## 0.7.0 - 2026-06-28

### Breaking Changes

- **Edit tool names and formats:** the patch-hunk editor formerly exposed as `edit` is now `patch`; `edit` now uses an `old_string`/`new_string` replacement format. Update permission rules, hook filters, skill `allowed_tools`, and integrations that referenced the old `edit` patch-hunk format.
- **Edit-tool permission fallback:** `edit` and `patch` now share one edit-tool family. A rule for one format applies to the counterpart when that counterpart has no explicit same-tool rule, including `deny`. Configure both names when one model-facing format should be disabled while the other remains available.
- **Anthropic transport config:** `providers.<name>.compat.anthropic_transport` is no longer read. Remove this setting from existing configs; Anthropic Messages requests always use the Claude Code-style transport hints described below.
- **Import CLI:** `chord import --tool-mode` has been removed because recognizable external tool calls are now always normalized to structured Chord tool cards.
- **Codex OAuth runtime state:** Codex OAuth runtime cache now uses `auth.state.json` instead of the previously released `auth.state.yaml`. Existing quota/reset/account-status cache data can be regenerated by warm-up/polling, but the YAML cache is not migrated automatically.

### Highlights

- Model-specific editing now exposes the best-trained editor for each model family: GPT/o-series models use `patch` @@ hunks, while Claude/Qwen/DeepSeek-style models use `edit` old/new replacements.
- Provider transports and retry behavior were aligned across Responses, Anthropic Messages, Codex OAuth, streaming timeouts, error classification, and fallback status reporting.
- Request-level context reduction is safer and more useful, with stronger protection for recent high-risk outputs and better typed summaries for older tool results.
- TUI rendering, status surfaces, error diagnostics, changed-file stats, model fallback display, and wide-terminal card layout received broad reliability and performance polish.
- Tooling became more robust: raw `read` output, multi-root `grep`/`glob`, faster `patch`, better patch failure diagnostics, `question` scalar tolerance, and image tool results attached to tool cards.

### Improvements

- Responses API requests now use the same Responses wire shape for every `type: responses` provider, explicitly sending `tool_choice`, `parallel_tool_calls`, `store`, `stream`, `include`, and Codex-compatible `client_metadata` when a session id is available. `store` still defaults to `false`, but explicit provider/model config is now honored. Encrypted reasoning content is requested only when the request carries a reasoning block. Relay endpoints that validate this request shape no longer reject Chord with `invalid codex request`.
- Anthropic Messages requests now always send Claude Code-style client hints, including `x-app: cli`, the default Claude Code beta feature list, and JSON-formatted `metadata.user_id` for cache/routing affinity. These transport details are implicit like the Responses/Codex wire shape; the previous `compat.anthropic_transport` provider setting is no longer read or needed. Upgrade note: this setting existed in released versions including `v0.6.3`, so remove any `providers.<name>.compat.anthropic_transport` entries from existing configs when upgrading. Provider-level `user_agent` remains configurable for gateways that require a specific client/version string. The `context-1m-2025-08-07` beta is the exception: because the official API enforces it (tier-gated, long-context pricing above 200K, and an error on unsupported models), Chord opts in only when the model's declared window reaches 1M tokens (`limit.input` if set, otherwise `limit.context` >= 1000000), matching how Claude Code only sends it for 1M-capable models.
- Reasoning is now sent when either `effort` or `summary` is configured (previously only `effort` triggered it), fixing a bug where summary-only reasoning configs were silently dropped.
- Responses-compatible providers outside the official Codex backend now pass `reasoning.effort` through after normalization, allowing provider-specific values such as GLM `max`, `minimal`, or `none`; Codex still keeps its restricted effort set.
- Responses HTTP requests now send the same SSE headers (`originator`, `Accept`, `OpenAI-Beta: responses=experimental`) alongside the authorization header, independent of `preset: codex`, while keeping the normal `User-Agent: chord/<version>` default and honoring provider-level `user_agent` overrides.
- WebSocket Responses transport now propagates the `include` array from the request body instead of sending a hardcoded empty array.
- Provider-level timeout settings now allow per-provider overrides for initial HTTP response-header timeout, stream idle timeout, and Responses WebSocket handshake timeout via `response_header_timeout`, `stream_idle_timeout`, and `websocket_handshake_timeout`.
- JSON processing is faster on hot paths including LLM stream parsing, MCP JSON-RPC encoding/decoding, session import JSONL parsing, and `auth.state.json` loading.
- Local file tools now prefer UTF-8 or BOM-marked Unicode when reading existing text files and retain constrained support for common regional encodings including GB18030, Big5, and Shift-JIS. Ambiguous or unsupported encodings still fail fast; `web_fetch` continues to honor declared HTTP response charsets.
- `read` now returns raw file text without line-number gutters or extra indentation, making copied snippets safe for patch hunks and indentation-sensitive formats.
- `read` now uses a slimmer `READ_RESULT lines=a-b total=N` header, reports `truncated=budget` only when requested lines are actually dropped, omits UTF-8 encoding noise, and errors when `offset` is strictly past EOF instead of silently clamping it.
- `grep` now accepts `paths` and `includes` arrays for multi-root searches and path glob filters, `glob` now accepts a `patterns` array, and invalid `grep` regex patterns automatically fall back to literal-text search with a visible result note. `glob` permission checks also evaluate every requested pattern so a later deny/ask rule cannot be bypassed by an earlier allowed pattern.
- `grep` now reports per-path failures as result notes and returns partial results when only some search paths fail, instead of failing the whole call; it errors only when every requested path fails.
- `chord import` now always converts recognizable external tool calls to structured Chord tool cards when their arguments can be normalized. The previously released `--tool-mode` flag was removed because it no longer changes import behavior.
- `edit` and `patch` no longer require a prior `read` or system-resolved `@file` mention before modifying a file. The tool prompts still recommend inspecting the target area first when the exact text or hunk context has not been verified; previously observed files remain tracked as snapshots so external changes can warn and create backups before risky writes.
- Failed patch hunks now point out a near-miss file line and the first differing column when the mismatch is only a small long-line drift, making stale single-line prompts, URLs, and doc strings easier to recover.
- Failed patch hunks whose old lines do not form one contiguous block now explain why: how many hunk lines still exist in the file, or the longest adjacent matching run and its starting line. When the file changed on disk after it was last read, the error also notes the hunk may be based on stale content.
- TUI content viewers now copy the raw viewed content when using copy-all shortcuts, failed `patch` tool-card copy uses the full raw patch when the visible card content was trimmed, and restored inline image/PDF attachments use filename labels in the composer without adding duplicate text parts to model messages.
- TUI assistant cards now end their background surface at the wrapped-text cap on wide viewports, reducing unnecessary card-width background fill on large terminals.
- Natural-language prose (user/assistant messages, thinking, status cards) now wraps at up to 160 columns on wide terminals, while code blocks, diffs, and tool cards keep the 120-column cap that suits column-aligned content.
- TUI palette contrast is improved with widened card-surface greyscale steps and adjusted secondary foreground colors, making tool cards and assistant messages easier to distinguish.
- Streaming render is more efficient during long model responses, providing smoother text appearance.
- The resume session picker now shows an aligned `Msgs` column so large and small sessions are easier to distinguish before opening them.
- The TUI now includes a `Ctrl+E` error panel that records retry and final errors for the active conversation, clearing the list when `/new` starts a fresh conversation. It includes provider, model, a masked `key=...` label, HTTP status, and structured API code/type fields where available.
- Permission confirmation rule suggestions now include the matched ask rules for compound Shell commands, and the rule picker pre-selects every matched ask rule so one approval can save all blocking rules.
- `question` now tolerates a single question object in place of the documented `questions` array, mirroring the scalar-to-list tolerance used by `grep` and `glob`.
- Request-level context reduction now protects recent high-risk tool outputs such as diffs, failed assertions, stack traces, and permission/security errors from being pruned solely because a long single-turn tool chain advances effective age. The default `context.reduction.read_like_age_turns` is also raised from 1 to 2 based on recent-session statistics, preserving freshly read file context one effective turn longer at low observed token cost.
- Context reduction now avoids reusing an unreduced prompt-cache surface as a protection path: normal request-level reduction still runs before each main-model request, and low-pressure stable-prefix reuse is limited to prefixes that already have reduction savings.
- Context reduction now gives the first main-model request after all TODOs are completed a one-request wrap-up grace window. The default `context.reduction.wrap_up_grace_requests: 1` avoids low-value last-minute prompt-surface churn only when the same model is still active, no user input is queued, the context is not under high pressure, and newly estimated savings are below `min_incremental_saved_tokens`; if a previous reduced prefix exists, wrap-up reuses that reduced prefix instead of restoring raw tool output.
- Context reduction now keeps shorter successful shell-output excerpts by default, reducing low-signal terminal output retained under pressure.
- Request-level context reduction now produces safer and more useful summaries for older tool outputs: stale errors keep key failure/assertion/auth lines instead of becoming marker-only omissions, generic stale outputs keep head/tail excerpts or route to search/source/path-list summaries when detected, shell outputs are content-routed before generic success omission, search summaries are grouped by file, and debug reduction stats include aggregate skip-reason and possible over-compression signals for offline tuning.
- Codex OAuth runtime state now identifies accounts by the user-in-workspace key (`account_user_id`) instead of account/workspace ID alone, keeping quota/status updates separate when different users share the same workspace. Refresh-only credentials migrate from temporary `refresh_sha256:<digest>` state keys after their first successful refresh; OAuth access tokens now need parseable account and user/account-user claims.
- Codex OAuth runtime state now uses `auth.state.json` instead of the previously released `auth.state.yaml`; existing runtime cache data can be regenerated by warm-up/polling, but cached quota/reset/account status from the YAML file is not migrated automatically.
- `chord auth refresh <provider>` now refreshes every refresh-token backed Codex OAuth credential for a provider, reports per-account success/failure/skip status, and preserves rate-limit reset hints.
- Images returned by tools such as `view_image` now stay attached to the corresponding tool result, appear as openable TUI thumbnails on the tool result card, and are sent through provider-native multimodal tool/function result formats for APIs that support them. `view_image` is visible only when permitted and the effective model pool's first model supports image input without using OpenAI Chat Completions; when a later replay or fallback target does not support image/PDF input, Chord drops the unsupported binary parts before sending the request rather than forwarding attachments that target cannot process.

### Fixes

- The TUI status bar and info panel now update the displayed model immediately when a fallback/retry attempt switches provider or model, showing the model currently being tried instead of waiting for the first provider that successfully responds.
- Streaming interruption recovery now covers OpenAI-compatible Chat Completions as well as Anthropic, Gemini, and Responses providers: when a stream ends after visible assistant text, Chord preserves that text as interrupted context while still discarding incomplete tool calls, thinking, and reasoning so the next request can continue without replaying unsafe partial structures.
- LSP resource shutdown no longer logs normal stderr pipe closure as an error when idle language-server processes are unloaded.
- Successful or skipped context compaction now clears stale pre-compaction request-size token samples before saving recovery state, preventing missing or failed post-compaction usage reports from immediately triggering a second tiny automatic compaction.
- Tool-call parsing now preserves valid tool metadata when Responses-compatible gateways emit duplicate partial function-call events, keeps streaming tool-call callbacks on a stable ID when gateways fill `call_id` late, pairs recovered Responses tool-call callbacks, drops malformed Anthropic/Gemini/OpenAI-compatible/Responses tool calls with missing IDs or names without emitting orphan stream start, delta, or completion callbacks, and reports missing or unknown tools as invalid calls instead of misleading permission-policy denials.
- Request-level context reduction now skips stale stable-prefix reuse when it would break the current tool-call/tool-result chain, preventing orphan tool results and strict provider 400 errors.
- Retry and LSP service-note logs now distinguish actionable failures from intermediate fallback or suppressed non-actionable notes, reducing misleading runtime noise during successful operation.
- Tool call card headers now prioritize the main argument and can use wider viewports for the one-line summary, while secondary parenthesized parameters are shortened first. `grep` search paths that equal the current working directory are hidden, and child directories are displayed relative to the workspace.
- Streaming retries and rollbacks now clear partial thinking content and pending thinking translations, preventing stale thinking text from remaining in the TUI or recovered session state after a failed stream.
- Thinking translation language detection improvements:
  - Normalize language codes before comparison (e.g., `zh` vs `zh-Hans`, `en` vs `en-US`) to prevent false language mismatches
  - Switch from letter-based to semantic-unit-based ratio calculation (Latin words vs Han characters) for fairer weight distribution
  - Skip translation when target language is dominant (≥ 50%), preventing incorrect translation of mixed-language content where the user's language is actually the primary language despite misdetection
- Anthropic-compatible providers that report a `thinking_tokens` usage field now have it parsed into reasoning-token usage and shown in the TUI info panel as a separate `Think` line, alongside existing input/output and cache usage figures. (The official Anthropic API does not report this field; thinking is counted within `output_tokens`.)
- Sidebar file change tracking now normalizes file paths before comparing them, preventing duplicate entries when the same file is referenced with different path representations (e.g., `file.go` vs `./file.go`)
- Patch tool now uses soft anchor fallback for @@ headers without specific identifiers, reducing false failures when header format is imprecise
- Patch tool now detects and rejects patches containing only context lines (no +/- changes) with actionable error messages
- System prompt and tool schema descriptions now dynamically adapt to visible tools, preventing references to unavailable tools
- Edit/patch tool visibility is now enforced at execution time, so a model that only sees `edit` cannot execute a hidden `patch` call learned from earlier conversation history; LSP diagnostic guidance also uses the live model-appropriate edit tool name.
- LSP tool visibility now correctly requires a configured LSP manager instance
- Sidebar file statistics now prioritize showing complete +/- counts over long filenames, preventing truncation of change metrics
- Write tool operations now properly tracked in file change summaries with accurate line counts for new files and overwrites
- Model matching for edit/patch tool selection now uses strict pattern matching to prevent false positives (e.g., `o10` or `gptx` models incorrectly using patch tool)
- Permission fallback between edit and patch tools now correctly handles wildcard rules and explicit per-format overrides, so disabling `patch` while explicitly allowing `edit` lets GPT/o-series models fall back to `edit` instead of losing all edit capability.
- Speculative file mutation tracking now correctly extracts paths from both patch and edit tool arguments, fixing file tracking for ReplaceEditTool
- Interactive command detection now correctly allows piped commands (e.g., `man git | grep`) and provides command-specific non-interactive alternatives instead of generic terminal suggestions
- `write` now reports execution progress while writing file contents, matching other local file mutation tools more closely during longer writes.
- Patch tool performance: optimized from ~30s to milliseconds for large files by applying normalization fallbacks lazily, stopping hunk matching as soon as uniqueness/ambiguity is known, bounding expensive diagnostic scans before falling back only when needed, and adding fast diagnostic paths. Provides ~600x speedup in failure scenarios on 3000+ line files.
- Code blocks in tool cards (Done reports, confirmations, etc.) now wrap long lines with continuation indent instead of overflowing the card boundary, fixing display issues with CSV data and long shell commands
- Provider error classification now prefers structured `code`/`type` signals (including nested JSON in the error body) over free-text matching, with message fallbacks kept for gateways that omit them. Unrecognized HTTP 400s are now treated as terminal request/parameter errors instead of being retried across keys and models, while quota, oversize-context, concurrency, Codex WebSocket chain-state, and `Retry-After` 400s keep their existing retry/cooldown handling.
- Compatible-gateway transient HTTP 400s now use a short one-second probe cooldown when no `Retry-After` is provided, and pure all-keys-cooling waits report `cooling` instead of `retrying`. Configured `stream_retry_rounds` now also caps all-keys-cooling retry rounds, matching the documented retry-cap semantics.
- Request-local tuning overrides used by compaction and length-recovery continuations now merge with model/variant defaults instead of replacing them, so OpenAI Responses keeps configured `reasoning`/`text` fields and Anthropic/Gemini keep thinking and cache defaults after those recovery paths.
- The `CHORD_API_BASE` environment variable is now honored as a fallback for `--api-base`; the CLI flag still wins when both are set.
- Provider streaming HTTP clients no longer apply a total request timer to healthy streams: `response_header_timeout` controls the initial response-header wait, while `stream_idle_timeout` controls gaps between streamed chunks. Auxiliary non-streaming calls no longer reuse the response-header setting as a total request timeout.
- Streaming assistant cards whose content is only a placeholder (dots or an ellipsis) are no longer added to the conversation; the placeholder is replaced once real content arrives, and placeholder-only blocks are discarded instead of rendering as empty cards.
- Tool failures whose result text already equals the error text are now returned once instead of appending a duplicate `Error:` block; evidence collection and request-level context reduction now also classify tool errors by the structured tool-result status, so such results are still treated as errors without the `Error:` prefix.
- AGENTS.md workspace instructions are now injected with explicit scope and visibility framing for both main and sub-agents: Chord loads the complete applicable AGENTS.md content from the project root through the current working directory and sends it as an internal user-role meta message before the first real user message, headed by `# AGENTS.md instructions` and bounded by an `<INSTRUCTIONS> ... </INSTRUCTIONS>` block so it is treated as durable workspace guidance rather than optional context.
- Forked TUI messages now preserve inline image and PDF attachments after session restore and fork events, without being cleared by a deferred transcript rebuild.
- Gemini tool schemas now strip Chord-only coercion markers before sending function declarations to the provider.
- Shell permission fallback checks now keep compound-command review semantics when exposing matched rule suggestions, so narrow allow rules do not auto-approve unparsed compound commands.
- Pending model-pool switches now preserve their original pool while a request is in flight, so cancelling or applying the switch restores the intended state.
- Cache-read percentages in the TUI now use the input-side prompt plus separately reported cache-write tokens as the denominator.
- Anthropic-compatible gateways that report usage in `message_delta` events no longer overwrite non-zero input token counts with zero.
- The TUI info panel changed-files section now prioritizes full `+N -N` line-change stats over long filenames, matching the narrow sidebar behavior.
- The TUI info panel now scrolls independently with the mouse wheel or touchpad when the pointer is over it, so long changed-file or status sections are no longer clipped by the input area.
- Collapsed Shell tool cards that were rejected now show the expand hint before the rejection reason, so the hint is no longer pushed below the result text.
- Successful `edit`/`patch` tool cards now show LSP diagnostics when a file edit leaves errors, while hiding routine success boilerplate.
- Narrow TUI sidebars now prioritize changed-file `+N -N` stats over long filenames, keeping file change counts visible.
- Editing forked TUI messages now preserves inline image attachments even when the visible prompt text is edited before resubmission.
- Provider-reported usage now remains authoritative for automatic compaction: request-level local token estimates no longer clear an already-triggered usage-driven compaction request.
- Switching Codex OAuth keys now clears stale inline rate-limit snapshots so a previously exhausted key no longer keeps request-level context reduction frozen for the next key.
- Done confirmation dialogs now render Markdown and fenced code blocks on dialog-specific surfaces, avoiding assistant-card background bleed in the confirmation modal.
- TUI tool error cards now avoid repeating a leading `Error:` prefix in the displayed error body.
- Assistant Markdown tables in the TUI can use wider cards on large terminals, reducing vertical wrapping in wide review tables.
- TUI message rendering now escapes raw control characters before drawing cards, avoiding background color artifacts when pasted or model-generated text contains bytes such as `\x01`.
- Switching model pool while a request is in flight now takes effect at the next request boundary instead of disrupting the in-flight request; the status bar and info panel show the model the next request will use.
- Failed `patch` tool cards now show the attempted patch before the error text, making the failed hunk easier to inspect before reading the diagnostic.
- Focused-agent submissions now include `@file` mention content parts instead of sending plain text only.
- OAuth keys are now permanently removed from selection when the provider reports account or workspace deactivation, including deactivation responses surfaced as HTTP 402 errors.
- Resuming sessions after `view_image` no longer shows tool-returned images as user-authored messages.
- TUI rendering now disables terminal hard-scroll optimizations to avoid duplicate separators or stale border rows in Chord's sticky transcript layout.
- Pasting clipboard images in the TUI composer no longer adds duplicate image attachments, and deleted inline image placeholders now remove their attachment chips.

## 0.6.3 - 2026-06-05

### Highlights

- Multimodal: PDF input support across providers and built-in `view_image` tool for local images
- Context reduction now uses typed summaries for large tool results, preserving key information
- Unload idle LSP/MCP resources to reduce memory footprint
- Improved `edit` patch tolerance and cleaner TUI tool result display
- `grep` and `glob` now avoid walking the entire search root when the caller already knows the exact file path: a plain relative filename in `grep` `includes` or in `glob` `patterns` is resolved directly under the search path and read or statted without a recursive traversal. When a search still scans a very large root (for example the system temp directory, the home directory, or `/`) and matches very few candidate files, it aborts early with a recoverable error that advises passing the full file path as the search path or narrowing the search path, instead of hanging for minutes. The tool descriptions also clarify that path/name filters apply during traversal and do not avoid walking the search root.

### Features

- Added multimodal input support for PDFs across Gemini, Anthropic, OpenAI Responses, and OpenAI Chat providers, including TUI attachment chips and session recovery.
- Added the built-in `view_image` tool so models with image-input support can load local PNG/JPEG files into context using the same local-path permission handling as `read`.

### Improvements & Fixes

- Context reduction now uses typed summaries for older large tool results: search outputs keep query/count/sample hits, JSON blobs keep top-level shape/counts, build/test logs keep key failures, and read summaries include range metadata instead of falling straight back to generic omission.
- LLM-facing tool definitions now use the registry's stable name-sorted order, reducing prompt-cache misses from semantically unchanged tool ordering drift while preserving existing OpenAI `prompt_cache_key` and Anthropic `cache_control` behavior.
- Improved `edit` patch tolerance for blank context lines inside hunks, reducing failed model-generated edits.
- Chord now unloads idle LSP and MCP resources after several minutes of inactivity and restores MCP servers on the next request; idle LSP/MCP rows are shown dimly instead of as failures.
- `edit` results are less noisy in the TUI: routine successful patch summaries are hidden from the expanded card while diagnostics still show, failed edits show the attempted patch preview, and copied tool cards keep the full result.
- `edit` now reports successful patches with project-relative paths and concise added/removed counts, and failed patches include the attempted patch context for easier recovery.
- File tool cards now use compact success summaries for routine edit/write/delete results, and tool error results can be displayed and copied instead of being dropped from the card.
- Fixed an OAuth credential refresh crash when the active auth state uses a negative credential-index sentinel.
- `@` file completion now treats supported image/PDF files as attachments, hides unsupported media types for the current model, and marks unsupported or encrypted attachments in the composer/transcript.
- Switching to a model without image/PDF input support now filters unsupported historical binary parts before provider requests while preserving historical tool-call structure.
- Fixed the info panel's context `Bytes` display so fresh sessions start at zero user context and restored sessions immediately show the same post-reduction size and savings estimate used for the next request.
- Cancelling a turn now preserves tool calls that already completed successfully, so pressing Esc on a slow later tool no longer rewrites earlier successful tool cards as `context canceled`.
- Pending model or pool switches are now shown explicitly in the TUI while a turn is busy, so the status line and info panel distinguish the currently running model from the queued switch.
- `edit` now gives models clearer patch-writing guidance and accepts more common patch context, reducing avoidable edit failures.
- Deferred tool argument streaming updates now force a final TUI refresh when their throttled render state changes, so hidden or partially rendered tool arguments no longer stay stale.
- Editing the last user message with `ee` now removes that message from the current session and loads it into the composer instead of forking a new session.

## 0.6.2 - 2026-06-02

### Highlights

- Built-in tool names are now snake_case (`ApplyPatch` → `edit`, `WebFetch` → `web_fetch`, `TodoWrite` → `todo_write`, …) — update any permission rules that referenced the old names.
- Smarter context reduction that trims stale output sooner on long tool chains while staying prompt-cache friendly.

### Breaking Changes

- All built-in model-visible tool names changed to snake_case (for example `ApplyPatch` → `edit`, `WebFetch` → `web_fetch`, `TodoWrite` → `todo_write`). There are no compatibility aliases — update permission rules, hook tool filters, skill `allowed_tools`, and any saved integrations that used the old PascalCase names. Imported sessions still recognize source names like Codex `apply_patch` and map them to `edit`.

### Improvements & Fixes

- Context reduction now trims stale tool output sooner on long single-turn tool chains (age is measured by overall progress, not just later user messages), while warmup protection avoids re-trimming low-pressure prompts so prompt caching stays effective. `context.reduction` accepts `true` or `{}` for the default tuning and exposes finer keys (`cache_aware_min_usage`, `warmup_message_limit`, `min_incremental_saved_tokens`, `high_pressure_usage`, `force_prune_usage`); `context.reduction: false` is now rejected instead of silently using defaults.
- The sidebar's reduction savings now show the current request's total saved messages/bytes/tokens and stay visible after the turn goes idle.
- `edit` gives clearer guidance when a patch fails to apply — pointing out copied line-number gutters, indentation drift, stale file content, or mismatched function/class anchors.

## 0.6.1 - 2026-06-01

### Highlights

- New `edit` tool applies patch hunks to existing files, greatly reducing the exact-string match failures of the old `Edit`.
- YOLO mode: temporarily bypass permission prompts with `--yolo`, `/yolo on|off`, or `Ctrl+Y`.
- Git status sidebar showing branch, changed/staged/stash counts, and ahead/behind.
- More resilient Codex auth and quota handling, plus automatic recovery from WebSocket state errors.

### Breaking Changes

- **Permissions:** remembered permission rules now save into agent config files — project rules in `<project>/.chord/agents/<role>.yaml`, global rules in `<config-home>/agents/<role>.yaml` — instead of a separate permissions overlay. Rules previously written to `.chord/permissions/<role>.yaml` are no longer loaded; move any you still need. The built-in planner now allows `write`/`edit` only under `.chord/plans/*` by default.
- **Config:** the HTTP `User-Agent` override moved to a provider-level `user_agent`. Requests now default to `User-Agent: chord/<version>` unless overridden.
- **Config:** removed the unused `context.reduction.model_pool` and `maintenance.size_check_interval_hours` settings. Context reduction stays deterministic and does not call a model; use `context.compaction.model_pool` for LLM-backed compaction.
- **Config:** removed the `supports_fast` model field — migrate models to `supported_service_tiers: [fast]` (or omit it for preset/provider defaults).
- **Compatibility:** removed remaining pre-1.0 fallbacks — Codex import accepts only the current rollout schema, `--config` is no longer an alias for `--config-home`, and headless model switching accepts only `set_current_model_pool`.

### Features

- `edit` replaces the old `Edit` tool with native single-file patch hunks for localized changes (`write` still does full-file writes, `delete` removes whole files). It enforces read-before-edit, tolerates Codex `apply_patch` envelope markers (`*** Begin Patch` / `*** Update File:` / `*** End Patch`), and reports candidate lines when a hunk matches multiple places. If a file changed since you last read it, edits validate against the current content and back up risky overwrites under the session directory (capped per file and per session).
- YOLO mode (`--yolo`, `/yolo on|off`, `Ctrl+Y`) temporarily bypasses main-agent permission prompts; Handoff, Delegate, Cancel, and Done still require approval, and the status bar shows a YOLO pill while it's on.
- `/rules` now opens even when no rules exist and can add session/project/global allow/ask/deny rules; the remembered-rule picker lets you edit the suggested pattern before saving.
- Git status sidebar: a compact, collapsible Git summary (branch or detached commit, worktree name, changed/staged/stash counts, ahead/behind) that refreshes asynchronously without blocking rendering.
- LSP: Python diagnostics gain a quick `ruff` backend; large files use it automatically instead of blocking on full analysis. New top-level `diagnostics.*` config controls per-backend commands and output limits.
- Headless: a new `local_shell` command/event runs `!`-style local commands, and `Handoff` emits a structured `handoff_request` event with a `handoff` approve/deny command.
- Service tier (`/tier`, `Ctrl+R`) now propagates to SubAgents.
- Thinking translations persist per block to the session directory and restore on resume.
- Consistent mouse text selection across cards, viewers, and the composer (double-click selects a word, triple-click a line); `yy` copies a tool card as structured Markdown.
- Session import converts recognizable external tools (`read`, `shell`, `grep`, `glob`, `edit`, `write`, `delete`) to the closest Chord tool cards.

### Improvements & Fixes

- `@` file completion falls back to root-directory prefix matching, so files like `AGENTS.md` complete even when excluded from the Git index by `.gitignore`.
- `Grep` and `Glob` have lower default result caps and byte ceilings so broad searches don't crowd out more relevant context.
- Codex: access tokens must carry a parseable account ID, and token/refresh-token auth failures are classified as unrecoverable without redundant refresh attempts. `key_order: smart` treats only fully-used (100%) windows as exhausted — 99% still counts as usable — and compares short vs. weekly windows separately. WebSocket state-mismatch 400s now reset the chain and retry once as a full send; usage-limit errors skip the slow HTTP fallback and go straight to key/quota handling.
- Duplicate credential slots that share one access token now update cooldown, recovering, quota-exhausted, and success state together, so an exhausted token can't be re-selected through another slot.
- Compatible gateways: transient-looking 400s (e.g. "Concurrency limit exceeded") cool the key, rotate, and keep retrying; official-API request-shape 400s still stop immediately. API 400s are treated as model-level failures so the client can try another configured model.
- Resumed sessions repair structurally broken turns before sending to the provider, and orphan tool results without a matching tool call are dropped.
- OAuth slots are marked expired only after a real auth failure, not from local `expires` metadata; Codex runtime state reloads automatically when `auth.state.yaml` changes (so another Chord process's updates apply without a restart).
- Loop mode: `Done` no longer has a verification-status gate (open TODOs and active subagents still block); automatic and manual compaction now run in loop mode so long sessions can continue past the context budget. `/compact` runs in the background and applies at the next safe point, even mid-turn.
- The info panel's Bytes/Messages now reflect what will actually be sent to the model, with a post-reduction percentage.
- LSP diagnostics wait for fresh results after edits (fewer false positives from servers like gopls) and are capped to concise, priority-ordered blocks.
- Plan execution passes the plan as an `@<plan-path>` file mention instead of inlining it into the system prompt.
- Confirmation and `Question` deny reasons preserve your full text, including line breaks.
- TUI polish: `gg`/`G` move focus to the first/last card, restored tool cards keep their state and diffs, the `Ctrl+T` message directory renders inline with paging, completions show custom-command scope and `/tier` parity, overlays drop undocumented shortcut keys, and scrolling/focus recovery is smoother in large sessions.
- `chord cleanup sessions` also removes empty per-project session directories.
- `git stash show -p`, `git stash list --patch`, and other read-only stash subcommands are no longer blocked as interactive.

## 0.6.0 - 2026-05-20

### Highlights

- Request-time context reduction (`context.reduction`) keeps long sessions lean before compaction is needed.
- First-run setup wizard when `config.yaml` is missing.
- Loop mode uses `Done` (with a required completion report) as its single exit path.
- `chord import` brings in sessions from Claude Code, Codex, and OpenCode.
- `chord worktree finish` reworked to merge-then-squash, with a non-mutating `--check` preflight.

### Breaking Changes

- **Config:** renamed `context.compact_threshold` to `context.compaction.threshold`; there is no compatibility alias.
- **Config:** removed `context.auto_compact`. Automatic compaction is now enabled when `context.compaction.threshold > 0`; set it to `0` to disable.
- **Config:** removed `context.compact_model`. Compaction now only accepts `context.compaction.model_pool`; when unset, it clones the current agent model pool instead of a single model.
- **Headless:** removed the external `tool_result` event. Non-loop `Done` reports now use the dedicated `done_completion` event; loop-mode `Done` exit requests keep using `confirm_request` with explicit `done_report` / `done_reason` fields.

### Features

- Deterministic request-time context reduction under `context.reduction`, including stale tool-result pruning thresholds, kept off in loop mode.
- First-run setup wizard for the default `chord` command: it writes a minimal `config.yaml` (plus `auth.yaml` when needed), can complete Codex OAuth login, reuses matching existing credentials, and prints the exact paths it used.
- `Done` now requires a non-empty `report` with the full completion report. In loop mode it's the only exit path: premature calls are rejected back to the model, and valid exits open a confirmation dialog showing the report.
- `chord import` for external sessions from Claude Code, Codex, and OpenCode, writing a resumable session plus `import-report.json`.

### Improvements & Fixes

- Source builds and release artifacts now require Go 1.26.3+ (a patched toolchain that closes reachable standard-library vulnerabilities).
- OAuth account status now lives in `auth.state.yaml`, with a new `invalidated` status, and is kept out of `auth.yaml`.
- `chord worktree finish` first merges the target branch into the worktree branch to surface conflicts there, then squashes the result back onto the target as a single commit. `--check` previews that merge without touching the real worktree or target branch, and `finish` refuses to start if a rebase or merge is already in progress.
- Automatic input-method switching only runs for the foreground tab/window, so background tabs no longer clobber the active tab's IME.
- Hardened local file/path safety (rejecting device-style paths) and made config/auth writes atomic.
- Image paste de-duplicates key and terminal paste events so one paste no longer inserts two copies.
- A background confirmation alert keeps blinking the terminal title until you focus the window; fixed stale `TOKENS` in the info panel after compaction.

## 0.5.3 - 2026-05-11

### Features

- Replaced `chord test-providers` with `chord doctor models`: exact `provider/model[@variant]` checks, model-pool audits, all-model/all-pool modes, per-target timeouts, JSON output, and optional `--retry`.
- Project `.chord/config.yaml` now merges consistently across startup, auth login, and diagnostics; malformed project config is reported instead of silently ignored. New `stream_retry_rounds` can bound public LLM retry rounds for automation.

### Improvements & Fixes

- Resumed sessions rebuild durable `Read` file-state, so later `Edit`/`Write` keep the read-before-write safety check without falsely requiring every file to be re-read.
- When `limit.input` is omitted, compaction and model-pool fallback sizing reserve the output budget from `limit.context`, reducing oversized-prompt retries.
- `chord doctor models` reuses refreshed OAuth credential status across targets, avoiding stale-token false failures.
- Fixed Markdown preview syntax highlighting so trailing list/heading lines keep their color in `Read`/`Write` cards and fenced code blocks.

## 0.5.2 - 2026-05-11

### Breaking Changes

- Renamed the model-visible command tool from `Bash` to `Shell` (no runtime alias). Update permission rules (`permission.Shell`), hook tool filters, skill `allowed_tools`, saved/imported tool calls, headless consumers, and any prompts referring to the old `Bash` name before upgrading.

### Features

- `chord worktree finish --check`: a temporary isolated rebase preflight that shows whether the worktree would finish cleanly, without mutating the real worktree or leaving it mid-rebase.
- `Write` tool cards now show the written file as a numbered, syntax-highlighted preview, like `Read` cards.

### Improvements & Fixes

- Sidebar file list renamed `EDITED FILES` → `CHANGED FILES`; deleted files render struck-through with no fake `-1` line stat.
- Default shortcuts realigned: `Ctrl+P` is the model selector, the message directory moved to `Ctrl+T`, and the default `Ctrl+F` image-attach binding was removed (set `insert_attach_file` to restore it).
- API `402` quota/payment errors now behave like per-key rate limits — cool the exhausted key and try other keys before falling back.
- Non-interactive Shell/Spawn guardrails let plain `read`/`select` stdin proceed while still blocking TTY-dependent commands.
- Codex usage polling and OAuth browser/device-code logins cancel promptly on Ctrl+C or shutdown.
- Reduced Ghostty/cmux stale-cell artifacts after tab focus or resize recovery.

## 0.5.1 - 2026-05-09

### Features

- Manual MCP server control for `manual: true` servers: `/mcp` (`status`, `enable`, `disable`) and an MCP selector (`Ctrl+O`) to connect or disconnect on-demand servers at runtime. Auto-start servers stay read-only.

### Improvements & Fixes

- Fixed the initial LLM client not using the builder agent's full model pool, so first-request failures now correctly fall back across models, not just API keys.
- `Write` cards show a clear line/byte summary instead of a misleading "only a few lines shown" diff for full-file writes.
- Thinking translation tries the next model in the pool when one returns an empty result.
- `Bash` and `Spawn` reject high-confidence interactive commands and run with non-interactive defaults; timeout/cancel now escalates to force-kill so stuck child processes don't hang calls.
- Codex usage polling wakes proactively when WebSocket streams go quiet, keeping the RATE LIMIT panel current.
- Upgraded the Bubble Tea rendering stack and fixed Ghostty/cmux stale-cell artifacts after focus/resize recovery.

## 0.5.0 - 2026-05-08

### Features

- `chord import` for external sessions from Claude Code, Codex, and OpenCode, writing a resumable session plus `import-report.json`.
- Request-time model compatibility normalization safely replays or downgrades provider-specific history (Anthropic signed thinking, structured tools) when switching providers/models.

### Improvements & Fixes

- Agent configs use `mode: main` for MainAgent roles and `mode: subagent` for SubAgents (`sub_agent`/`sub` also accepted); hook `agent_kind` filters use `main`/`subagent`.
- Fixed a potential hang when a tool batch/turn is cancelled while waiting for the shared execution quota — Chord now emits a cancelled tool result so the UI can't get stuck.
- `chord worktree finish` gives step-by-step recovery commands on rebase conflicts and exits early if a rebase is already in progress.
- Fixed `/models` pool switching while busy so queued messages pick up the new pool at the next request boundary.
- Tool/Bash spinner animation advances one frame per tick (no skipped frames); background sessions keep the same cadence.
- Fixed THINKING card ordering when reasoning is followed quickly by assistant text.
- A background agent going busy → idle now shows a one-shot `✅` completion marker in the terminal title.

## 0.4.0 - 2026-05-07

### Highlights

- Git worktree support: `chord --worktree [name]` creates or enters an isolated worktree with its own sessions, cache, and exports.
- Google Gemini as a first-class provider.

### Features

- `chord --worktree [name]` (on the default command and `chord headless`) creates or enters a chord-managed git worktree, isolating sessions/cache/exports per worktree; combine with `--continue`/`--resume`. Auto-named when empty, and an existing worktree branch is reused.
- `chord worktree list` / `remove <name>` to manage worktrees, and `chord resume <session-id>` locates and resumes a session in whatever worktree it belongs to.
- `worktree.branch_prefix` config overrides the default `chord/` branch prefix (malformed refs are rejected at startup).
- Google Gemini as a first-class provider (`type: generate-content`): streaming text/tool/thinking output, inline images, function-calling tools, and `Retry-After` handling.

### Improvements & Fixes

- `session-meta.json` gains worktree fields; existing sessions stay compatible.
- Local slash commands (`/export`, `/models`) always run on the main event loop, fixing a stuck "busy" state when submitted during an LLM retry.
- Slash completion keeps the selected command visible when scrolling past 8 entries; `/new` clears the sidebar file list.

## 0.3.0 - 2026-05-07

### Highlights

- Runtime model pools: group models into named pools and switch the active pool at runtime via `/models` or the TUI selector.

### Breaking Changes

- Agent model configuration must now reference one or more top-level `model_pools`; the flat per-agent `models` list is no longer accepted. Every agent needs at least one pool, and the first listed pool is the default.

### Features

- Model pools with `/models` (`status`, `<pool>`, `--agent <name> <pool>`); switching while busy applies at the next request boundary without waiting for full idle.
- Build identity in diagnostics and `chord --version` (commit, dirty state, build/VCS time, Go version, executable mtime).

### Improvements & Fixes

- SKILLS sidebar no longer shows failed or unknown skills as loaded, and drops the legacy "(loaded)" suffix.
- Codex RATE LIMIT panel no longer sticks at "1s" after a window resets; it hides the timer and refreshes usage promptly.
- Deferred diagnostics/export status cards appear right after the current assistant block instead of waiting for idle.
- Fixed `Cmd+V` paste in permission-confirmation edit and deny-reason inputs.

## 0.2.0 - 2026-05-05

### Breaking Changes

- LSP `options` passed via `workspace/configuration` must now be shaped by section for every server (for Pyright, use nested `python` / `python.analysis` keys instead of flat top-level keys).
- Headless: removed the `notification` envelope type — render the ready/idle state from the `idle` envelope instead.
- SubAgent `Complete` dropped the `blockers_remaining` field; report non-blocking caveats via `remaining_limitations` and real blockers via Escalate or the `blocked` mailbox path.

### Features

- Pyright LSP auto-discovers project-local `.venv`/`venv`/`env` interpreters (Unix/WSL and Windows layouts) and normalizes relative interpreter paths against the LSP root.
- New session-scoped `SaveArtifact` / `ReadArtifact` tools and structured SubAgent completion handoff (files changed, verification run, remaining limitations, risks, follow-ups, artifact references).
- Loop `verify` assessments inject a dedicated `LOOP VERIFY` notice with explicit verification guidance; documented `/loop on [target]`.

### Improvements & Fixes

- Automatic (threshold-driven) compaction now continues working on the compacted context instead of going idle; manual `/compact` still returns to idle.
- More accurate session preview/title after compaction, using explicit metadata instead of inferring from text. (Sessions compacted by older versions may still show polluted titles until re-compacted.)
- Tool cards render as terminal-safe plain text (escaped ANSI, no false Markdown rendering); fixed background artifacts around emoji/ZWJ graphemes and inconsistent padding.
- Fixed `ee`/fork editing so images restored from session history are reloaded and re-sent; mouse drag-selection copy keeps the final character.
- Fixed long-session transcript clipping/drift that could hide the last cards or misalign mouse selection.
- Local-path tools (`Read`/`Write`/`Edit`/`Delete`/`Grep`/`Glob`) show project-relative paths when possible.
- Logs switched to plain `golog` output; the previous pseudo-structured `level=... msg=... key=value` format is no longer emitted.
- Additional Ghostty/cmux stale-display fixes after rapid scroll/resize/focus changes.

## 0.1.0 - 2026-04-29

- Initial public release of Chord.
- A terminal coding agent with a local-first runtime, Vim-like navigation, session management, model/provider configuration, tool execution, LSP integration, image input, and headless control support.
- Cross-platform release builds for macOS, Linux, and Windows.
