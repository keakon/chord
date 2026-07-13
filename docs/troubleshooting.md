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

### Large OAuth account pools start slowly

When `auth.yaml` contains hundreds or thousands of OpenAI / ChatGPT OAuth accounts, Chord loads credential metadata in the background. Missing metadata alone should not block startup.

- Personal Plus/Pro accounts may carry only `user_id` and no `chatgpt_account_id`. They can still be used for ordinary requests, but Chord omits `ChatGPT-Account-ID` and skips account-id-dependent Codex usage / rate-limit polling for them.
- `account_user_id mismatch` or `account_id mismatch` logs mean metadata explicitly configured in `auth.yaml` conflicts with what the token itself exposes. Fix or remove that credential.

When manually converting Codex/sub2api exports, keep any available `email`, `account_id`, and `account_user_id`. Use `chord doctor models` for deliberate account diagnostics.

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

- Press `Ctrl+E` in the TUI to open the error panel, which shows all retry errors including 429s with the provider, model, and masked `key=...` label.
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

Set `official_api: true` for endpoints that follow official API error semantics. Chord then treats HTTP 400 as a terminal request error. For aggregating or proxy gateways that may wrap upstream failures as HTTP 400, set `official_api: false` or omit the field so unknown 400s can use the normal retry and fallback path. `preset: codex` providers are treated as official automatically.

If requests remain in `connecting` and then retry, test the endpoint directly, check proxy settings, and inspect the error panel. Chord applies a connection timeout so one unavailable key or gateway does not wait indefinitely.

### DeepSeek / OpenAI-compatible thinking-mode 400s

If you use a `chat-completions` provider such as DeepSeek and see errors like:

- `The reasoning_content in the thinking mode must be passed back to the API.`
- `Invalid assistant message: content or tool_calls must be set`

this usually means the provider requires thinking/reasoning content from the previous tool round to be included again in the follow-up request, with a strict assistant message shape. If the same error keeps repeating, keep the corresponding session dump / trace for diagnosis.

Enable `compat.reasoning_continuity.mode: openai_visible` on the affected model
or provider. This option only replays assistant `reasoning_content`; add any
provider-specific thinking flags through `compat.request_overrides.body`.

For GLM Preserved Thinking, that body override must include
`thinking.type: enabled` and `thinking.clear_thinking: false`. For DeepSeek it
only needs `thinking.type: enabled`. In both cases, replayed
`reasoning_content` must remain complete, unchanged, and in order.

### Codex WebSocket 400 "No tool call found for function call output"

Chord normally recovers from this WebSocket conversation-state mismatch automatically by retrying with the full local conversation. If the error repeats, export a diagnostics bundle and include the session ID in your report.

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
- Ruff quick diagnostics do not update the LSP sidebar; they appear only in `edit`, `patch`, or `write` tool results and clearly note that full Python semantic diagnostics were skipped.

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

Chord automatically repairs incomplete turns caused by an interrupted process before resuming. If restored model/provider state or conversation order looks wrong, export a diagnostics bundle from a current build and include the session ID.

## TUI cards show strange colors or broken layout when viewing logs / dumps / shell output

If a tool card, local shell result, question dialog, or confirmation summary shows unexpected colors, background leaks, or broken wrapping while viewing diagnostic dumps or raw command output:

- retry the same `read`, `shell`, `web_fetch`, or local shell action
- if you still see corruption, save the original file/output and a screenshot together

Chord displays external tool output as terminal-safe plain text. If the same content consistently breaks layout, attach the original text and a screenshot so the rendering case can be reproduced.

## Output-triggered TUI render panic / process killed

If the outer launcher only shows:

```text
Error: program was killed: program experienced a panic
```

and the session must be resumed with `--resume`, first inspect the end of `~/.local/state/chord/logs/chord.log` for the Go panic stack. `main.jsonl` usually does not contain this panic text because the failure happened in the TUI render path, not as a persisted conversation message.

If the stack contains these frames, treat it as a TUI markdown/ANSI rendering issue first instead of blaming the model or the last tool result directly:

```text
github.com/charmbracelet/x/ansi.(*Parser).Advance
charm.land/lipgloss/v2.(*WrapWriter).Write
charm.land/glamour/v2/ansi.(*HeadingElement).Finish
github.com/keakon/chord/internal/tui.renderMarkdownContent
```

When checking dumps, note:

- If the last shell/tool result before the crash was already written to `main.jsonl`, that tool output was usually not lost.
- If Chord was waiting on an LLM SSE stream at the time of the crash, the corresponding `dumps/llm/*.json` may contain only `request_body`, partial `sse_chunks`, and `reading SSE stream: context canceled`. That means the LLM response was dumped only up to the interruption, with no complete final text.
- `context canceled` is usually a consequence of process shutdown, not necessarily the root cause.

Attach the panic stack, diagnostics bundle, terminal name/version, and the content being rendered to the issue report.

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

## Bottom transcript rows are unreachable in long sessions

If the last transcript rows appear clipped, the final card seems to touch the input separator, or scrolling to the bottom still leaves part of the latest conversation hidden:

- pay special attention to whether the issue starts after long-running background jobs or durable status updates in a long session
- if it still reproduces, capture a screenshot and logs so the transcript state can be compared with the rendered bottom rows

## A file-edit tool warns that the file changed since it was observed

This warning means the file changed after the agent last read it. Chord validates edits against the current contents; `write` and `delete` may also create a backup before continuing.

Common causes:

- the file was modified by another process (editor/formatter) between `read` and `edit`/`patch`;
- another agent or Chord process changed the file;
- the file changed during a formatter, generator, or build step.

Re-run `read` before retrying. If Chord creates a backup, the tool result includes its path under the current session directory. See [Edit tools](./edit-tools.md) for edit and patch matching behavior.

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
- compare behavior in different terminals

See [Performance](./performance.md) for how rendering and streaming are optimized and how to capture a CPU profile for a bug report.

## Compaction not triggering / triggering too often

**Symptom**: context usage is high but compaction never runs; or the opposite — frequent compaction disrupts your workflow.

What to check:

1. Verify `context.compaction.threshold` is set and greater than 0 (0 disables automatic compaction).
2. Check the `Context` percentage in the TUI footer or info panel. It is based on the **usable input budget**, not the total context window, so it may be lower than expected (see [Configuration — Context compaction](./configuration.md#context-compaction)).
3. If `context.compaction.reserved` is set, compaction triggers at a lower absolute token count because the reserve is subtracted before applying `threshold`; if compaction is too frequent, check whether reserved is too large.
4. `/compact --no` temporarily disables automatic compaction for the current session. Restart the session or run `/compact` to re-enable.
5. If your gateway returns missing or zero usage, enable `log_level: debug` and look for `estimated_input_tokens` and `effective_input_tokens` in automatic-compaction logs.

Note: loop mode does not disable automatic compaction. It only disables request-level context reduction for newly added messages.

## Reduction trimming important content

**Symptom**: the model seems to "forget" earlier tool output, but the session file on disk still contains it.

What to check:

1. This is normal behavior for context reduction: stale tool output is trimmed from each LLM request prompt, but **never modifies** session files on disk.
2. If you frequently need to revisit earlier read/search results, raise `read_like_age_turns` and `read_like_output_bytes`.
3. If build/test logs remain important context, raise `shell_success_bytes`.
4. For more conservative trimming behavior, raise all `*_age_turns` and `*_bytes` values.

See [Configuration — Context reduction](./configuration.md#context-reduction).

## Requests rejected: "context length" / "input too large"

**Symptom**: the provider returns an error like "context length exceeded" or "input too large".

What to check:

1. Verify that `limit.input` and `limit.context` are correctly configured for your model. If the provider publishes a separate input cap, you must also configure `limit.input`.
2. Check if `context.compaction.threshold` is too high, causing automatic compaction to fire too late.
3. Increase `context.compaction.reserved` to trigger compaction earlier, avoiding rejected requests.
4. If this happens frequently, use `/compact` to manually compact immediately, or lower `threshold`.
5. With `log_level: debug`, search the logs for `oversize` to confirm whether oversize recovery (compact then retry) was triggered. If automatic compaction is disabled, Chord stops and reports that all attempted candidate models exceeded the current context instead of retrying indefinitely.

## When to check logs

Check logs first when you encounter:

- provider request failures where the terminal only shows a summarized error
- context compaction not triggering / context limit issues
- MCP / LSP initialization errors
- hook execution results that differ from expectations
- incomplete headless integration events

Default logs directory: `${XDG_STATE_HOME:-~/.local/state}/chord/logs/`. The current log file is `chord.log`; rotated files are `chord.log.1` and `chord.log.2`.

Override with `--logs-dir <path>` or `CHORD_LOGS_DIR=<path>`. To reproduce and collect logs quickly:

```bash
chord --logs-dir ./chord-logs
```

## Related

- [Quickstart](./quickstart.md)
- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Headless](./headless.md)
