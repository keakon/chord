# Troubleshooting

This page focuses on common user-facing issues around installation, config, auth, sessions, extensions, and performance.

## Startup failures

Check first:

- Whether your Go version meets the requirement
- Whether you are using the correct source entry point: `go run ./cmd/chord/`
- Whether `config.yaml` is missing or malformed
- Whether `auth.yaml` has obvious YAML formatting errors

If `config.yaml` is missing, run `chord` once in an interactive terminal to launch the setup wizard. If stdin is redirected but Chord can still open the controlling TTY, the wizard runs there. If there is no controlling TTY, Chord exits immediately with an initialization error. If `config.yaml` exists but is malformed, fix the file first; the wizard only runs for missing configs.

If you are using a built binary, rerun it and inspect terminal error output.

## 401 / 403 / auth failures

Check:

- Whether provider names in `auth.yaml` match `config.yaml`
- Whether the API key is valid
- Whether OAuth providers have `preset: codex`

You can run:

```bash
chord doctor models
```

To narrow the check to a known model or model pool:

```bash
chord doctor models --model openai/gpt-5.5@high
chord doctor models --pool thinking
```

## 429 / quota exhausted

Common causes:

- the key has reached its quota
- provider rate limiting
- concurrency or high-frequency requests triggering rate limits

Recommendations:

- switch to another key
- reduce concurrency or retries
- check for accidental looped calls

To diagnose which keys or models are hitting rate limits or errors:

- Press `Ctrl+E` in the TUI to open the error panel, which shows all retry errors including 429s with the provider, model, and key suffix.
- Check the error panel to see the pattern: if one specific key is repeatedly hitting 429, that key is rate-limited; if multiple keys on the same provider fail with 503, the provider itself may be degraded.

UI note:

- the right-side RATE LIMIT panel shows the last Codex rate-limit snapshot (e.g. `5h: 42% 2h30m`). When a reset timestamp is reached, the countdown may disappear briefly while Chord refreshes usage; depending on provider semantics (rolling windows), usage may drop gradually rather than jumping straight to 0%.
- Codex OAuth runtime state is also reloaded from `auth.state.json` when another Chord process updates that file, so quota snapshots, reset timers, account metadata, and account status changes should appear without restarting the current session.
- if the RATE LIMIT panel looks stale, enable debug logs with `log_level: debug` and check `chord.log` for lines like `responses codex ws: rate_limits event ...` (received) or `responses codex ws: rate_limits event ignored ...` (unrecognized / parse failure).

## TUI started, but requests fail

Check:

- whether the current provider / model exists
- whether the network can reach the API
- whether proxy configuration is effective

For example:

```bash
curl -I https://api.anthropic.com
curl -I https://api.openai.com/v1
```

### OpenAI-compatible 400s and timeouts

For provider endpoints that use official API semantics, HTTP 400 normally means the request is invalid and Chord stops instead of retrying the same bad payload. Set `official_api: true` on those providers. Set `official_api: false` or omit it for aggregating or proxy gateways when you want Chord to treat unknown 400s as potentially recoverable gateway errors. Providers with `preset: codex` are treated as official automatically.

For non-official OpenAI-compatible gateways, Chord does not trust HTTP 400 by itself as proof of a bad request. Many gateways collapse upstream overload, rate-limit, or provider failures into 400, so Chord treats non-official 400s as retryable by default: the current key is put into a short cooldown of at most 1 minute, then Chord can try another key, model, or retry round. Only explicit request-shape signals, such as structured `invalid_request` / `invalid_parameter` codes or clear messages like missing required parameters, stop immediately.

If a connection cannot be established or no first token arrives before timeout, Chord marks the current key as recovering so the next retry prefers another healthy key.

For Responses HTTP providers, the initial `connecting` phase is also bounded. If the upstream or gateway never starts the HTTP response, Chord fails that attempt after about 25 seconds instead of waiting indefinitely, allowing the normal retry / fallback path to continue. This limit applies to waiting for response headers; once a healthy stream has started, normal streaming is still governed by stream-idle timeout rather than a fixed total request cap.

### DeepSeek / OpenAI-compatible thinking-mode 400s

If you use a `chat-completions` provider such as DeepSeek and see errors like:

- `The reasoning_content in the thinking mode must be passed back to the API.`
- `Invalid assistant message: content or tool_calls must be set`

this usually means the provider requires thinking/reasoning content from the previous tool round to be included again in the follow-up request, with a strict assistant message shape. If the same error keeps repeating, keep the corresponding session dump / trace for diagnosis.

### Codex WebSocket 400 "No tool call found for function call output"

The Codex WebSocket transport sends incremental requests keyed by `previous_response_id`. The server keeps its own view of the conversation under that id, and if our locally constructed input drifts from that view (for example after a request-signature change between turns), the server can reject the next turn with `400 No tool call found for function call output with call_id …` even though the matching `function_call` and `function_call_output` are present together in the input we sent.

When Chord sees this kind of 400 it now clears the WebSocket chain state (`previous_response_id`, baseline, signature) and immediately retries the same request on the same WebSocket as a full send without `previous_response_id`. That is equivalent to starting a fresh server-side conversation seeded by the local input, and resolves the mismatch in-place without burning an HTTP round trip. The retry is only tried when the original 400 is identified as a chain-state mismatch; if the retry still fails the input itself is malformed and HTTP fallback would fail identically, so the error is returned without further fallback.

Most users do not need to do anything — the recovery is automatic. If you see this error repeating across many turns, capture the trace and the session id so the conversation contents can be examined.

## MCP never becomes ready

Check first:

- whether the MCP URL is reachable
- whether the config name is correct
- whether local mode is simply still initializing MCP asynchronously

Note: a brief gray pending state right after startup does not necessarily indicate an error.

## LSP / MCP rows turn gray after the session is idle

If the right-side environment panel shows LSP or MCP rows in gray after the agent has been idle for a while, that does **not** necessarily mean the integration failed.

Chord can unload idle LSP and MCP runtime resources to reduce background cost. In that state:

- the row is shown in a dim gray idle state instead of an error color;
- this means the resource was intentionally unloaded while the session was idle;
- on the next real request / busy cycle, Chord restores the runtime dependency before rebuilding the request surface.

Treat it as a problem only when the row stays red, keeps showing a real connection/configuration error, or the next request fails to restore it.

## No diagnostics after writing files

If you configured LSP but do not see diagnostics after writing files:

- check whether the corresponding language server is installed locally
- check whether the `lsp` config format is correct
- confirm the target file type matches `file_types`
- check whether `diagnostics.enabled: false` turned off post-tool diagnostics

For Python specifically:

- Small files use `diagnostics.python.semantic_backend` (usually `lsp.pyright`). Make sure `diagnostics.python.semantic_backend.server` matches the server key under `lsp`.
- Large Python files use Ruff quick diagnostics when `ruff` is on `PATH`.
- If a large Python file reports diagnostics skipped because Ruff is unavailable, install Ruff or set `diagnostics.python.large_file.run_semantic_when_quick_unavailable: true` to force Pyright on large files too.
- Ruff quick diagnostics do not update the LSP sidebar; they appear only in `edit` / `write` tool results and clearly note that full Python semantic diagnostics were skipped.

Recommended Python skeleton:

```yaml
lsp:
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]

diagnostics:
  python:
    quick_backend:
      type: command
      command: ruff
```

For the full recommended config, see [Configuration — Post-tool diagnostics](./configuration.md#post-tool-diagnostics).

## Session resume issues

If `--continue` or `--resume` does not appear to work as expected:

- confirm the current directory belongs to the same project as the original session
- try explicitly using `--resume <session-id>`
- check whether restore is only slow rather than actually lost

## Session resume / restore behavior notes

For `--continue`, `--resume`, new-session, fork-session, and plan-execution flows:

- make sure the current directory belongs to the same project as the original session
- prefer `--resume <session-id>` when you need an exact target
- if model/provider state looks wrong after restore, keep the relevant session logs and traces for diagnosis
- if you suspect regressions around Codex/OpenAI session boundaries, capture logs from a current build so traces reflect the post-cleanup transport lifecycle

When a session is resumed Chord also repairs structurally broken turns before the transcript is sent to a provider:

- a trailing assistant message with `stop_reason=interrupted` (process killed mid-stream) is dropped, so the next user/system turn drives a fresh assistant reply
- every assistant `tool_call` whose matching tool result was never persisted gets a synthetic `error` tool message (`ToolStatus=error`) appended in its place, so providers that require strict `function_call ↔ function_call_output` pairing (OpenAI Responses, Anthropic `tool_use`/`tool_result`) accept the input

The repair is structural only — text and `ToolStatus` of already-persisted tool messages are not rewritten. Tool messages whose payload happens to contain words like "denied" or "cancelled" are no longer reinterpreted as failures.

Session resume itself is fully supported. Internal transport cleanup does not affect the ability to continue or resume sessions. If you see unexpected behavior after restore, capture logs from the current build for diagnosis.

## TUI cards show strange colors or broken layout when viewing logs / dumps / shell output

If a tool card, local shell result, question dialog, or confirmation summary shows unexpected colors, background leaks, or broken wrapping while viewing diagnostic dumps or raw command output:

- retry the same `read`, `shell`, `web_fetch`, or local shell action
- if you still see corruption, save the original file/output and a screenshot together

Chord displays ANSI-rich external text literally inside these UI surfaces instead of re-executing embedded terminal escape/control sequences. This includes bare carriage-return progress/control text, preventing diagnostic dumps and other raw terminal output from corrupting surrounding card rendering while still letting you inspect the original sequences. Generic tool results are also treated as plain text even when they contain Markdown-looking headings, lists, tables, or code fences; this avoids accidental reformatting of logs, diffs, JSON/YAML, and fetched pages.

## Screen corruption after switching tabs or refocusing the terminal

If the TUI occasionally shows stale rows, horizontal line artifacts, or partially broken tool cards right after switching tabs or returning focus to the terminal window:

- if the screen is already corrupted, lightly resizing the terminal or switching away and back again can force a full redraw
- if it still reproduces, capture a diagnostics bundle and a screenshot together

If you see corruption right after focus restore while the UI is streaming output, capture a diagnostics bundle plus a screenshot so maintainers can compare Chord's rendered frame with the terminal's visible output.

Note: fragments like `;250m pyright` during a corruption episode are usually not LSP text but the tail of a truncated ANSI/OSC control sequence.

### Repeated separator lines / stale border artifacts

If the main symptom is repeated horizontal lines, duplicated input/status separators, stale card borders, or old sidebar borders:

1. Take a screenshot before forcing a redraw. Include the full terminal window, especially the input area, status bar, and right sidebar.
2. Export a diagnostics bundle (`Ctrl+G`) immediately, before resizing the terminal — the bundle captures the frame Chord most recently rendered, so it lets maintainers tell a Chord-drawn duplicate from a stale terminal artifact.
3. Attach both to your report, plus your terminal emulator name and version.

Two quick local observations also help narrow it down:

- If the extra line disappears when the terminal is made one or two columns narrower, mention that — it points at right-edge wrap behavior.
- If the artifact appears right after image preview, paste image, or diagnostics export, mention that too.

Chord disables terminal hard-scroll optimizations because those sequences can leave stale rows in Chord's sticky transcript layout, so most remaining reports come down to terminal-specific redraw behavior; the screenshot-plus-bundle pair is what makes them diagnosable.

## Bottom transcript rows are unreachable in long sessions

If the last transcript rows appear clipped, the final card seems to touch the input separator, or scrolling to the bottom still leaves part of the latest conversation hidden:

- pay special attention to whether the issue starts after long-running background jobs or durable status updates in a long session
- if it still reproduces, capture a screenshot and logs so the transcript state can be compared with the rendered bottom rows

## A file-edit tool warns that the file changed since it was observed

This warning comes from Chord's in-process file tracking. It means the current file no longer matches the last content hash recorded for this agent. Chord no longer rejects every stale file edit: `edit` matches old/new text against the current file contents, `patch` validates hunks against the current file contents, while `write` and `delete` back up risky non-empty pre-write contents before continuing.

Common causes:

- the file was modified by another process (editor/formatter) between `read` and `edit`/`patch`;
- a speculative tool run was discarded/rolled back and the finalized call raced it;
- the provider sent tool arguments as a JSON string (wrapped arguments). Chord unwraps tool arguments consistently; if a wrapped path is not tracked correctly, capture logs and the session JSONL.

If a backup was created, the tool result includes its path under the current session directory. Empty files and non-risky continuous agent-owned edits do not create backups. Backups are capped at 10 per path, 200 per session, 10 MiB per file, and 50 MiB per session; if a required backup would exceed those limits or otherwise fail, the edit can still proceed but the tool result says no backup was created and why. Backups are removed when the session directory is deleted.

## Patch reports `hunk not found` or `matched multiple locations`

`patch` matches hunks line-by-line and applies the first match after the current search position. It can tolerate common whitespace and Unicode punctuation differences, but repeated blocks still need enough nearby context to make the intended location clear.

If you see this:

- re-run `read` on the file and rebuild the patch from the latest content;
- if the success output says a hunk `matched multiple locations`, use the candidate line numbers in the note to `read` around the intended occurrence and add nearby unchanged lines to the `@@` hunk before retrying future related edits;
- if the error says the hunk was not found, re-copy the target block from the latest `read` output and make sure context/removal lines match the current indentation; if the hunk came from old numbered output, remove any copied line-number prefix first;
- split a broad patch into smaller single-file patches or smaller hunks;
- do not run external `apply_patch` through `shell`; use Chord's native `patch` tool so permissions, stale tracking, diffs, LSP, and rollback stay connected.

## Performance issues

If scrolling, streaming output, or large message rendering feels noticeably slow:

- reduce the current session context size (`/compact`, or a new session for unrelated work)
- streaming assistant/thinking output keeps only stable structure (blank-line-separated paragraphs and closed fences) on the markdown path; long single paragraphs stay on the cheaper plain-text path until they settle
- compare behavior in different terminals

See [Performance](./performance.md) for how rendering and streaming are optimized and how to capture a CPU profile for a bug report.

## Compaction not triggering / triggering too often

**Symptom**: context usage is high but compaction never runs; or the opposite — frequent compaction disrupts your workflow.

What to check:

1. Verify `context.compaction.threshold` is set and greater than 0 (0 disables automatic compaction).
2. Check the `Context` percentage in the TUI footer or info panel. It is based on the **usable input budget**, not the total context window, so it may be lower than expected (see [Configuration — Context compaction](./configuration.md#context-compaction)).
3. If `context.compaction.reserved` is set, compaction triggers at a lower absolute token count because the reserve is subtracted before applying `threshold`; if compaction is too frequent, check whether reserved is too large.
4. `/compact --no` temporarily disables automatic compaction for the current session. Restart the session or run `/compact` to re-enable.

Note: loop mode does not disable automatic compaction. It only disables request-level context reduction for newly added messages.

## Reduction trimming important content

**Symptom**: the model seems to "forget" earlier tool output, but the session file on disk still contains it.

What to check:

1. This is normal behavior for context reduction: stale tool output is trimmed from each LLM request prompt, but **never modifies** session files on disk.
2. If you frequently need to revisit earlier read/search results, raise `read_like_age_turns` and `read_like_output_bytes`.
3. If build/test logs are important context, raise `shell_success_bytes`.
4. For more conservative trimming behavior, raise all `*_age_turns` and `*_bytes` values.

See [Configuration — Context reduction](./configuration.md#context-reduction).

## Requests rejected: "context length" / "input too large"

**Symptom**: the provider returns an error like "context length exceeded" or "input too large".

What to check:

1. Verify that `limit.input` and `limit.context` are correctly configured for your model. If the provider publishes a separate input cap, you must also configure `limit.input`.
2. Check if `context.compaction.threshold` is too high, causing automatic compaction to fire too late.
3. Increase `context.compaction.reserved` to trigger compaction earlier, avoiding rejected requests.
4. If this happens frequently, use `/compact` to manually compact immediately, or lower `threshold`.
5. With `log_level: debug`, search the logs for `oversize` to confirm whether oversize recovery (compact then retry) was triggered.

## When to check logs

Check logs first when you encounter:

- provider request failures where the terminal only shows a summarized error
- context compaction not triggering / context limit issues
- MCP / LSP initialization errors
- hook execution results that differ from expectations
- incomplete headless integration events

Default logs directory: `${XDG_STATE_HOME:-~/.local/state}/chord/logs/`. The current log file is `chord.log`; rotated files are `chord.log.1` and `chord.log.2`.

Current builds write logs in golog's plain text format, for example `[I 2026-05-02 12:00:00 file:123 pwd=/path/to/workspace pid=1234 sid=20260502015258426] message key=value`. Treat key-value fragments as human-readable text, not as a stable structured logging schema.

Override with `--logs-dir <path>` or `CHORD_LOGS_DIR=<path>`. To reproduce and collect logs quickly:

```bash
chord --logs-dir ./chord-logs
```

## Related

- [Quickstart](./quickstart.md)
- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Headless](./headless.md)
