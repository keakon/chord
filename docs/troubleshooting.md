# Troubleshooting

This page focuses on common user-facing issues around installation, config, auth, sessions, extensions, and performance.

## Startup failures

Check first:

- Whether your Go version meets the requirement
- Whether you are using the correct source entry point: `go run ./cmd/chord/`
- Whether `config.yaml` / `auth.yaml` have obvious YAML formatting errors

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

This change does not remove session resume support; it only deletes obsolete internal reset plumbing that no longer affected HTTP request behavior and keeps the active WebSocket/session lifecycle tied to the current session identifier.

## TUI cards show strange colors or broken layout when viewing logs / dumps / shell output

If a tool card, local shell result, question dialog, or confirmation summary used to show unexpected colors, background leaks, or broken wrapping while viewing diagnostic dumps or raw command output:

- upgrade to a build that includes the external-text rendering fix
- retry the same `Read`, `Shell`, `WebFetch`, or local shell action
- if you still see corruption, save the original file/output and a screenshot together

Recent builds now display ANSI-rich external text literally inside these UI surfaces instead of re-executing embedded terminal escape/control sequences. This includes bare carriage-return progress/control text, preventing diagnostic dumps and other raw terminal output from corrupting surrounding card rendering while still letting you inspect the original sequences. Generic tool results are also treated as plain text even when they contain Markdown-looking headings, lists, tables, or code fences; this avoids accidental reformatting of logs, diffs, JSON/YAML, and fetched pages.

## Screen corruption after switching tabs or refocusing the terminal

If the TUI occasionally shows stale rows, horizontal line artifacts, or partially broken tool cards right after switching tabs or returning focus to the terminal window:

- upgrade to a build that includes the latest post-focus redraw fix
- if the screen is already corrupted, lightly resizing the terminal or switching away and back again can force a full redraw
- if it still reproduces on the latest build, capture a diagnostics bundle and a screenshot together

Recent builds add redraw protection for two focus-restore cases: updates that arrive immediately after focus returns, and transcript/layout changes that happened while the terminal was backgrounded. When background changes are detected, Chord now waits for focus-settle, forces a strong host redraw, and explicitly arms a later fallback redraw for the same focus cycle. That late `post-focus-settle-fallback` pass stays armed even if the earlier `post-focus-settle-redraw` already ran, so Ghostty/cmux/iTerm2 still get one more recovery pass when the host surface invalidation outlasts the first redraw. Diagnostics bundles also include background-dirty state and the fallback-arming event so any remaining stale-display cases can be compared against the final internal screen buffer.

If you see corruption right after focus restore while the UI is streaming output, make sure you are on a build where the host-redraw replay marker is durable across multiple `View()` calls. Bubble Tea can call `View()` several times before its renderer ticker actually flushes a frame; older builds consumed the no-op replay marker on the first `View()`, so a later cached/deferred frame could still be byte-identical after a host-side `ClearScreen` and leave stale cells behind. Newer builds keep a generation-specific no-op replay suffix active until the next host redraw generation, without storing it in cached views.

If diagnostics show normal `block.Render()`, `viewport.Render()`, and final internal `screen_buffer` output while the real terminal remains corrupted, treat it as a host redraw/replay issue first. Avoid working around it by changing tool-card padding, ANSI reset handling, or card background helpers; include the diagnostics bundle and screenshot so `last_host_redraw` and `host_redraw_generation replay_nonce` can be compared with the visible stale cells.

Note: if you see fragments like `;250m pyright` during a corruption episode, this is typically not LSP text but the tail of a truncated terminal control sequence (ANSI/OSC). Newer builds route terminal window-title updates through Bubble Tea's `View().WindowTitle` instead of writing OSC sequences directly to stdout, avoiding interleaving with renderer output.

## Bottom transcript rows are unreachable in long sessions

If the last transcript rows appear clipped, the final card seems to touch the input separator, or scrolling to the bottom still leaves part of the latest conversation hidden:

- upgrade to a build that includes the latest TUI transcript clipping fixes
- pay special attention to whether the issue starts after long-running background jobs or durable status updates in a long session
- if it still reproduces on the latest build, capture a screenshot and logs so the transcript state can be compared with the rendered bottom rows

Recent builds fix two transcript-height accounting bugs:

- Late updates to older status cards in long sessions could leave the viewport shorter than the real transcript, making the last rows or even several final cards unreachable.
- Background idle-sweep cache dropping could miscompute offscreen line offsets when turn-spacing lines were present, causing scroll/selection drift that grew over time.

## Edit reports `file ... has not been read in this conversation`

Recent builds require `Edit` to have a tracked `Read` of the same file earlier in the conversation. This avoids stale blind edits and makes retries more reliable.

If you see this error:

- run `Read` on the target file first;
- copy `old_string` from the raw source portion only, not the displayed line-number gutter;
- re-read the smallest unique 2-4 line block before retrying if any earlier edit or external tool may have changed the file.

## Edit reports `changed on disk since the last read` even when the previous Edit succeeded

This error comes from Chord's in-process optimistic file locking. It means Chord believes the file no longer matches the last content hash it recorded for this agent.

Common causes:

- the file was modified by another process (editor/formatter) between `Read` and `Edit`;
- a speculative tool run was discarded/rolled back and the finalized call raced it;
- the provider sent tool arguments as a JSON string (wrapped arguments). Recent builds unwrap tool arguments consistently, but if you are on an older build, a wrapped `path` may not be tracked correctly and can trigger false staleness errors.

If this persists on the latest build, capture the session JSONL and current file diff so the tool-call ordering and tracked paths can be inspected.

## Performance issues

If scrolling, streaming output, or large message rendering feels noticeably slow:

- reduce the current session context size
- streaming assistant/thinking output keeps only stable structure (blank-line-separated paragraphs and closed fences) on the markdown path; long single paragraphs stay on the cheaper plain-text path until they settle
- compare behavior in different terminals

- for render hotspot analysis, maintainers can profile `go test ./internal/tui -run '^$' -bench 'BenchmarkRenderAssistantStreamingLongTextCardProfile' -cpuprofile cpu.out -memprofile mem.out` and inspect the remaining cost in block rendering vs viewport slicing

## When to check logs

Check logs first when you encounter:

- provider request failures where the terminal only shows a summarized error
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
