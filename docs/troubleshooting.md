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
chord test-providers
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

Recent internal cleanup removed an unused legacy LLM responses-session reset path and consolidated session-boundary handling onto the active provider/session identifiers. This should be user-transparent, but if you are diagnosing session resume, plan execution, or model/key-switch behavior:

- make sure you are testing on the latest build rather than comparing against older pre-1.0 behavior
- after `--continue`, `--resume`, new-session, fork-session, or plan-execution flows, rely on current behavior rather than older assumptions about a separate manual responses-session reset step
- if you suspect regressions around Codex/OpenAI session boundaries, capture logs from a current build so traces reflect the post-cleanup transport lifecycle

This change does not remove session resume support; it only deletes obsolete internal reset plumbing that no longer affected HTTP request behavior and keeps the active WebSocket/session lifecycle tied to the current session identifier.

## TUI cards show strange colors or broken layout when viewing logs / dumps / shell output

If a tool card, local shell result, question dialog, or confirmation summary used to show unexpected colors, background leaks, or broken wrapping while viewing diagnostic dumps or raw command output:

- upgrade to a build that includes the external-text rendering fix
- retry the same `Read`, `Bash`, `WebFetch`, or local shell action
- if you still see corruption, save the original file/output and a screenshot together

Recent builds now display ANSI-rich external text literally inside these UI surfaces instead of re-executing embedded terminal escape/control sequences. This includes bare carriage-return progress/control text, preventing diagnostic dumps and other raw terminal output from corrupting surrounding card rendering while still letting you inspect the original sequences. Generic tool results are also treated as plain text even when they contain Markdown-looking headings, lists, tables, or code fences; this avoids accidental reformatting of logs, diffs, JSON/YAML, and fetched pages.

## Screen corruption after switching tabs or refocusing the terminal

If the TUI occasionally shows stale rows, horizontal line artifacts, or partially broken tool cards right after switching tabs or returning focus to the terminal window:

- upgrade to a build that includes the latest post-focus redraw fix
- if the screen is already corrupted, lightly resizing the terminal or switching away and back again can force a full redraw
- if it still reproduces on the latest build, capture a diagnostics bundle and a screenshot together

Recent builds add redraw protection for two focus-restore cases: updates that arrive immediately after focus returns, and transcript/layout changes that happened while the terminal was backgrounded. When background changes are detected, Chord now waits for focus-settle and then forces a host redraw with a delayed fallback. Diagnostics bundles also include background-dirty state and input separator coordinates so any remaining stale-display cases can be compared against the final internal screen buffer.

## Bottom transcript rows are unreachable in long sessions

If the last transcript rows appear clipped, the final card seems to touch the input separator, or scrolling to the bottom still leaves part of the latest conversation hidden:

- upgrade to a build that includes the latest TUI transcript clipping fixes
- pay special attention to whether the issue starts after long-running background jobs or durable status updates in a long session
- if it still reproduces on the latest build, capture a screenshot and logs so the transcript state can be compared with the rendered bottom rows

Recent builds fix a transcript-height accounting bug where late updates to older status cards in long sessions could leave the viewport shorter than the real transcript, making the last rows or even several final cards unreachable.

## Performance issues

If scrolling, streaming output, or large message rendering feels noticeably slow:

- reduce the current session context size
- check for unusually long outputs
- compare behavior in different terminals

If you are maintaining Chord itself, you can also use repository performance scripts and pprof.

## When to check logs

Check logs first when you encounter:

- provider request failures where the terminal only shows a summarized error
- MCP / LSP initialization errors
- hook execution results that differ from expectations
- incomplete headless integration events

Default logs directory: `${XDG_STATE_HOME:-~/.local/state}/chord/logs/`. The current log file is `chord.log`; rotated files are `chord.log.1` and `chord.log.2`.

Current builds write logs in golog's plain text format, for example `[I 2026-05-02 12:00:00 file:123] message key=value`. Treat key-value fragments as human-readable text, not as a stable structured logging schema; older `level=... msg=...` pseudo-structured lines are no longer emitted by the runtime logger.

Override with `--logs-dir <path>` or `CHORD_LOGS_DIR=<path>`. To reproduce and collect logs quickly:

```bash
chord --logs-dir ./chord-logs
```

## Related

- [Quickstart](./quickstart.md)
- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Headless](./headless.md)
