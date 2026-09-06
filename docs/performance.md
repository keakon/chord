# Performance

Chord is optimized for long interactive sessions: large transcripts, streaming model output, scrolling, and background agent activity. This page explains what Chord does to stay fast, what you can do when something feels slow, and what to collect for a useful bug report.

## Measured results

These measurements are scenario-specific proof points, not universal guarantees. They show where Chord's context trimming, low-overhead TUI, and predictable shutdown behavior matter most. Results vary with hardware, environment, session content, model behavior, and implementation choices.

Both benchmarks below were run against the versions named in the tables; Chord v0.6.3 was released 2026-06-05.

### Real-world coding task

We benchmarked Chord against Codex-CLI on a [real-world database system task](https://github.com/datacurve-ai/deep-swe/tree/main/tasks/pebble-durability-wait-apis): implementing durability wait APIs in Pebble. Far from simple CRUD, the task requires understanding commit/WAL sync and concurrency semantics, reasoning across write paths, event listeners, and DB lifecycle subsystems.

| Metric | Chord v0.6.3 | Codex-CLI v0.136.0 | Improvement |
|--------|--------------|---------------------|-------------|
| **Time** | **46m21s** | 61m18s | **24% faster** |
| **LLM calls** | **93** | 118 | **21% fewer** |
| **Input tokens** | **6.86M** | 18.47M | **63% fewer** |
| **Output tokens** | **25K** | 74K | **66% fewer** |
| **Cache read tokens** | **6.55M** | 17.64M | **63% fewer** |
| **Cost** | **$5.58** | $15.15 | **63% cheaper** |

Notes:

- Both runs used GPT-5.5 (xhigh).
- Time excludes environment setup and final wrap-up, but includes model interaction, code changes, and test execution.
- The task's reference solution spans 8 files and 670 changed lines; actual model output may be larger or smaller depending on tests, comments, and implementation choices.

### App startup and memory

We also measured the interactive app shell: time from launch to accepting input, normal exit time, and memory with an empty session and after loading 200 messages.

| App | Startup to input | Normal exit | Empty session memory | 200-message memory |
|-----|------------------|-------------|----------------------|--------------------|
| Chord v0.6.3 | **<1s** | **<1s** | **31.6MB** | **~40MB** |
| Codex-CLI v0.136.0 | **<1s** | ~20s | 35.8MB | ~80MB |
| Claude Code v2.1.163 | 32s | ~2s | 156.3MB | >300MB |

Notes:

- Codex-CLI waits for shutdown wrap-up and exits after about a 20-second timeout.
- Claude Code waits on startup and becomes ready for input after about a 30-second timeout.
- Memory use varies by session content and environment, so these numbers are only estimates for this measured scenario.

## What Chord optimizes

1. The TUI stays responsive while the model streams text or thinking output.
2. CPU stays bounded during long responses instead of doing work per token.
3. Memory stays stable when loading or navigating large sessions.
4. Scrolling and keyboard input stay smooth while background work is active.

## How it works

- **Stream batching** — streaming text arrives in small chunks; Chord coalesces provider deltas and handles several of them per UI update instead of waking the TUI for every chunk.
- **Render cadence** — streamed content is flushed to the screen on a cadence rather than per token. Structural changes (a new block, a layout boundary, a rollback) still refresh promptly.
- **Cheap streaming path** — while an assistant or thinking block is still streaming, only stable, settled content goes through full Markdown rendering; the actively changing tail stays on a cheaper plain-text path until it settles. Long single paragraphs therefore look plainer while they stream — that is expected, not a rendering glitch.
- **View caching** — expensive regions such as the main viewport, info panel, and status bar are cached per frame and re-rendered only when their inputs change.
- **Scroll batching** — mouse wheel and touchpad deltas are batched and applied on a short cadence; only the visible window of a large transcript stays hot, with off-screen regions kept cold.
- **Lazy session loading** — resuming a large session loads the current window for interaction first; search, jump, and directory metadata plus older transcript regions hydrate in the background.
- **Bounded search state** — transcript search caches only the current query's rendered match position instead of retaining rendered copies of every line. Search can inspect spilled cards without leaving their content or derived indexes resident in the hot window.

## Request and context cost

Not all performance work is UI-side. Chord also reduces model-side cost by pruning stale tool outputs at request time, preserving structured summaries, and compacting long-running conversations before they hit the model limit. These optimizations reduce latency, token usage, and provider cost without deleting durable session history.

See [Context management — Reduction](./context-management.md#context-reduction) for the available context reduction settings.

## Streaming tool early execution

Chord also cuts perceived latency by starting safe tools before the model's response has fully finished. As the model streams a tool call's arguments, once those arguments are complete and valid, Chord may begin executing the tool immediately instead of waiting for the provider's end-of-response signal — then shows the result as soon as it is ready. This applies to local low-side-effect tools such as `read`, `grep`, `glob`, and read-only shell commands; network calls such as `web_fetch` wait for the stream to finish, and file-changing tools only commit when the call is confirmed final (with rollback if it is discarded). Chord enables this by default for the tools it deems safe; no configuration is needed. Which tools run early and what they may touch is governed by the same permission rules as ordinary tool calls.

## If Chord feels slow

- Reduce the current session context size: run `/compact`, or start a new session for unrelated work.
- Compare behavior in a different terminal emulator — rendering cost varies notably across terminals.
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
