# Performance

Chord is optimized for long interactive sessions: large transcripts, streaming model output, scrolling, and background agent activity. This page explains what Chord does to stay fast, what you can do when something feels slow, and what to collect for a useful bug report.

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
