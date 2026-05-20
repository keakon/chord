# Troubleshooting

This page focuses on common user-facing issues around installation, config, auth, sessions, extensions, and performance.

## Startup failures

Check first:

- Whether your Go version meets the requirement
- Whether you are using the correct source entry point: `go run ./cmd/chord/`
- Whether `config.yaml` is missing or malformed
- Whether `auth.yaml` has obvious YAML formatting errors

If `config.yaml` is missing, run `chord` once in an interactive terminal to launch the setup wizard. If stdin is redirected but Chord can still open the controlling TTY, the wizard runs there. If there is no controlling TTY, Chord exits immediately with an initialization error. If `config.yaml` exists but is malformed, fix the file first; the wizard only runs for missing configs.

You can run:

```bash
go test ./cmd/chord/...
```

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

UI note:

- the right-side RATE LIMIT panel shows the last Codex rate-limit snapshot (e.g. `5h: 42% 2h30m`). When a reset timestamp is reached, the countdown may disappear briefly while Chord refreshes usage; depending on provider semantics (rolling windows), usage may drop gradually rather than jumping straight to 0%.
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

### DeepSeek / OpenAI-compatible thinking-mode 400s

If you use a `chat-completions` provider such as DeepSeek and see errors like:

- `The reasoning_content in the thinking mode must be passed back to the API.`
- `Invalid assistant message: content or tool_calls must be set`

this usually means the provider requires thinking/reasoning content from the previous tool round to be included again in the follow-up request, with a strict assistant message shape. If the same error keeps repeating, keep the corresponding session dump / trace for diagnosis.

## MCP never becomes ready

Check first:

- whether the MCP URL is reachable
- whether the config name is correct
- whether local mode is simply still initializing MCP asynchronously

Note: a brief gray pending state right after startup does not necessarily indicate an error.

## No diagnostics after writing files

If you configured LSP but do not see diagnostics after writing files:

- check whether the corresponding language server is installed locally
- check whether the `lsp` config format is correct
- confirm the target file type matches `file_types`

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

Session resume itself is fully supported. Internal transport cleanup does not affect the ability to continue or resume sessions. If you see unexpected behavior after restore, capture logs from the current build for diagnosis.

## TUI cards show strange colors or broken layout when viewing logs / dumps / shell output

If a tool card, local shell result, question dialog, or confirmation summary shows unexpected colors, background leaks, or broken wrapping while viewing diagnostic dumps or raw command output:

- retry the same `Read`, `Shell`, `WebFetch`, or local shell action
- if you still see corruption, save the original file/output and a screenshot together

Chord displays ANSI-rich external text literally inside these UI surfaces instead of re-executing embedded terminal escape/control sequences. This includes bare carriage-return progress/control text, preventing diagnostic dumps and other raw terminal output from corrupting surrounding card rendering while still letting you inspect the original sequences. Generic tool results are also treated as plain text even when they contain Markdown-looking headings, lists, tables, or code fences; this avoids accidental reformatting of logs, diffs, JSON/YAML, and fetched pages.

## Screen corruption after switching tabs or refocusing the terminal

If the TUI occasionally shows stale rows, horizontal line artifacts, or partially broken tool cards right after switching tabs or returning focus to the terminal window:

- if the screen is already corrupted, lightly resizing the terminal or switching away and back again can force a full redraw
- if it still reproduces, capture a diagnostics bundle and a screenshot together

Chord protects redraws for two focus-restore cases: updates that arrive immediately after focus returns, and transcript/layout changes that happened while the terminal was backgrounded. When background changes are detected, Chord waits for focus to settle and forces a host redraw with a fallback pass, so terminals like Ghostty, cmux, or iTerm2 get reliable recovery even when host surface invalidation outlasts the first redraw. Diagnostics bundles include background-dirty state so remaining cases can be compared against the final screen buffer.

If you see corruption right after focus restore while the UI is streaming output, treat it as a host redraw/replay issue and capture the diagnostics bundle plus screenshot. Avoid working around it by changing component padding or ANSI handling.

Note: if you see fragments like `;250m pyright` during a corruption episode, this is typically not LSP text but the tail of a truncated ANSI/OSC control sequence. Chord routes window-title updates through the framework's `WindowTitle` API instead of writing directly to stdout, avoiding interleaving with renderer output.

## Bottom transcript rows are unreachable in long sessions

If the last transcript rows appear clipped, the final card seems to touch the input separator, or scrolling to the bottom still leaves part of the latest conversation hidden:

- pay special attention to whether the issue starts after long-running background jobs or durable status updates in a long session
- if it still reproduces, capture a screenshot and logs so the transcript state can be compared with the rendered bottom rows

Chord handles two transcript-height accounting risks:

- Late updates to older status cards could leave the viewport shorter than the real transcript.
- Background cache dropping could miscompute line offsets, causing scroll drift that grew over time.

## Edit reports `file ... has not been read in this conversation`

Chord requires `Edit` to have a tracked `Read` of the same file earlier in the conversation. This avoids stale blind edits and makes retries more reliable.

If you see this error:

- run `Read` on the target file first;
- copy `old_string` from the raw source portion only, not the displayed line-number gutter;
- re-read the smallest unique 2-4 line block before retrying if any earlier edit or external tool may have changed the file.

## Edit reports `changed on disk since the last read` even when the previous Edit succeeded

This error comes from Chord's in-process optimistic file locking. It means Chord believes the file no longer matches the last content hash it recorded for this agent.

Common causes:

- the file was modified by another process (editor/formatter) between `Read` and `Edit`;
- a speculative tool run was discarded/rolled back and the finalized call raced it;
- the provider sent tool arguments as a JSON string (wrapped arguments). Chord unwraps tool arguments consistently; if a wrapped `path` is not tracked correctly, capture logs and the session JSONL.

If this persists, capture the session JSONL and current file diff so the tool-call ordering and tracked paths can be inspected.

## Edit reports `old_string not found` even though the file already contains the expected new content

When troubleshooting a dev build with streaming tool execution enabled, you may see `Edit` report `old_string not found` while the target file already contains the expected content. This usually means a speculative `Write` / `Edit` / `Delete` executed before the LLM finalized, was later discarded due to args drift, filtering, or rollback, and the finalized path then tried to re-run before the speculative file change had finished rolling back.

Chord synchronously rolls back completed speculative file changes before allowing the finalized execution path to retry. If you still see this:

- look for `args_drift`, `filtered`, `rollback`, `length_recovery` in the logs as speculative discard reasons
- confirm the finalized `Edit` did not reuse a stale `old_string` from an earlier file snapshot
- keep the session JSONL and current file diff to inspect speculative discard/rollback ordering

## Performance issues

If scrolling, streaming output, or large message rendering feels noticeably slow:

- reduce the current session context size
- streaming assistant/thinking output keeps only stable structure (blank-line-separated paragraphs and closed fences) on the markdown path; long single paragraphs stay on the cheaper plain-text path until they settle
- compare behavior in different terminals

- for render hotspot analysis, maintainers can profile `go test ./internal/tui -run '^$' -bench 'BenchmarkRenderAssistantStreamingLongTextCardProfile' -cpuprofile cpu.out -memprofile mem.out` and inspect the remaining cost in block rendering vs viewport slicing

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

Current builds write logs in golog's plain text format, for example `[I 2026-05-02 12:00:00 file:123 pwd=/path/to/workspace pid=1234 sid=20260502015258426] message key=value`. Treat key-value fragments as human-readable text, not as a stable structured logging schema; older `level=... msg=...` pseudo-structured lines are no longer emitted by the runtime logger.

Override with `--logs-dir <path>` or `CHORD_LOGS_DIR=<path>`. To reproduce and collect logs quickly:

```bash
chord --logs-dir ./chord-logs
```

## Related

- [Quickstart](./quickstart.md)
- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Headless](./headless.md)
