# Performance

Chord is optimized for long interactive sessions: large transcripts, streaming model output, scrolling, and background agent activity.

## Measured results

These measurements come from specific scenarios. They show where Chord's context trimming, low-overhead TUI, and lean memory use matter most; results vary with hardware, environment, session content, model behavior, and implementation choices.

Each benchmark below was run against the versions named in its table.

### Real-world coding task

We ran a [DeepSWE v1.1 task](https://deepswe.datacurve.ai/data/v1.1/tasks/httpx-streaming-json-iteration) that adds streaming JSON iteration to `httpx`, across six agent harnesses. Chord finished first and cheapest: 6m37s and $0.052, with the next-best run taking 1.5× as long and costing 1.5× as much.

The task requires handling structured JSON streaming per media type (`application/json`, `application/*+json`, NDJSON, and JSON text sequences), plus stream consumption, decoding errors, and content-type parameters.

| Harness | Time | LLM calls | Input tokens | Output tokens | Cache read tokens | Cost |
|---------|------|-----------|--------------|---------------|-------------------|------|
| **Chord v0.8.1** | **6m37s** | **49** | **54,530** | **58,559** | **2,961,280** | **$0.052** |
| deepseek-harness 0.1.5-rc.1 | 9m50s (1.5×) | 93 | 58,569 | 80,314 | 7,357,440 | $0.079 (1.5×) |
| pi 0.85.1 | 10m22s (1.6×) | 101 | 79,189 | 94,733 | 5,871,488 | $0.086 (1.7×) |
| codex 0.154.0 | 17m01s (2.6×) | 121 | 72,363 | 135,310 | 15,787,264 | $0.139 (2.7×) |
| mini-swe-agent 2.4.6 | 18m29s (2.8×) | 158 | 161,893 | 77,320 | 18,206,592 | $0.125 (2.4×) |
| claude code 2.1.272 | 22m25s (3.4×) | 143 | 106,546 | 214,126 | 7,024,768 | $0.166 (3.2×) |

Multipliers are relative to Chord.

Cost follows the token mix rather than the token total: cache reads cost 50× less than uncached input and 200× less than output, so Chord's 2.96M cache-read tokens add about a cent while its 58,559 output tokens account for two-thirds of the $0.052 bill. Chord also used the fewest input tokens, output tokens, and model calls of all six runs.

Notes:

- All six runs used deepseek-v4.1-flash.
- Cost is estimated from each run's token totals at that model's listed prices per 1M tokens: $0.15 input, $0.60 output, $0.003 cache read.
- Time excludes environment setup and final wrap-up, but includes model interaction, code changes, and test execution.

### App memory

We also measured the interactive app shell's memory: with an empty session and after loading 200 messages.

| Harness | Empty session memory | 200-message memory |
|---------|----------------------|--------------------|
| Chord v0.8.1 | 30MB | 39MB |
| Codex-CLI v0.154.0 | 27MB | 47MB |
| Claude Code v2.1.273 | 143MB | 216MB |

Notes:

- The memory numbers were measured on macOS 15.3.2 (arm64).
- Memory use varies by session content and environment, so these numbers are only estimates for this measured scenario.

## What Chord optimizes

1. The TUI stays responsive while the model streams text or thinking output.
2. CPU stays bounded during long responses instead of doing work per token.
3. Memory stays stable when loading or navigating large sessions.
4. Scrolling and keyboard input stay smooth while background work is active.

## How it works

- **Stream batching**: streaming text arrives in small chunks; Chord coalesces provider deltas and handles several of them per UI update instead of waking the TUI for every chunk.
- **Render cadence**: streamed content is flushed to the screen on a cadence rather than per token. Structural changes (a new block, a layout boundary, a rollback) still refresh promptly.
- **Cheap streaming path**: while an assistant or thinking block is still streaming, only stable, settled content goes through full Markdown rendering; the actively changing tail stays on a cheaper plain-text path until it settles. Long single paragraphs therefore look plainer while they stream; that is expected behavior.
- **View caching**: expensive regions such as the main viewport, info panel, and status bar are cached per frame and re-rendered only when their inputs change.
- **Scroll batching**: mouse wheel and touchpad deltas are batched and applied on a short cadence; only the visible window of a large transcript stays hot, with off-screen regions kept cold.
- **Lazy session loading**: resuming a large session loads the current window for interaction first; search, jump, and directory metadata plus older transcript regions hydrate in the background.
- **Bounded search state**: transcript search caches only the current query's rendered match position instead of retaining rendered copies of every line. Search can inspect spilled cards without leaving their content or derived indexes resident in the hot window.

## Request and context cost

Not all performance work is UI-side. Chord also reduces model-side cost by pruning stale tool outputs at request time, preserving structured summaries, and compacting long-running conversations before they hit the model limit. These optimizations reduce latency, token usage, and provider cost without deleting durable session history.

See [Context management: Reduction](./context-management.md#context-reduction) for the available context reduction settings.

## Streaming tool early execution

Chord also cuts perceived latency by starting safe tools before the model's response has fully finished. As the model streams a tool call's arguments, once those arguments are complete and valid, Chord may begin executing the tool immediately instead of waiting for the provider's end-of-response signal, then shows the result as soon as it is ready. This applies to local low-side-effect tools such as `read`, `grep`, `glob`, and read-only shell commands; network calls such as `web_fetch` wait for the stream to finish, and file-changing tools only commit when the call is confirmed final (with rollback if it is discarded). Chord enables this by default for the tools it deems safe; no configuration is needed. Which tools run early and what they may touch is governed by the same permission rules as ordinary tool calls.

## If Chord feels slow

- Reduce the current session context size: run `/compact`, or start a new session for unrelated work.
- Compare behavior in a different terminal emulator, because rendering cost varies notably across terminals.
- First access to very old transcript regions in a huge session can be slower than the hot window; subsequent access is cached.

If a specific interaction stays slow, capture a CPU profile while reproducing it and attach the profile plus a diagnostics bundle (`Ctrl+G`) to your report:

```bash
CHORD_PPROF_PORT=6060 chord
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=15
```

## For contributors

Benchmark suites, regression checks, hotspot interpretation, and tuning trade-offs for performance-sensitive changes are documented in [CONTRIBUTING](https://github.com/keakon/chord/blob/main/CONTRIBUTING.md#performance-sensitive-changes). `./scripts/bench_tui_regression.sh` is the canonical validation entry point.

## Related

- [Configuration & Auth](./configuration.md)
- [Troubleshooting](./troubleshooting.md)
