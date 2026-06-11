# Performance optimization guide

Chord is optimized for long interactive sessions: large transcripts, streaming model output, scrolling, and background agent activity. This guide summarizes the current optimization strategy and the checks to run before changing performance-sensitive paths.

This page is intentionally user-facing. It explains the public behavior and verification commands, not internal implementation notes or private debugging records.

## Goals

Chord's performance work is guided by four goals:

1. Keep the TUI responsive while the model streams text or thinking output.
2. Keep CPU bounded during long responses instead of doing work per token.
3. Keep memory stable when loading or navigating large sessions.
4. Preserve smooth scrolling and keyboard input even when background work is active.

## Main optimization areas

### 1. Stream event batching

Streaming text arrives in small chunks. Handling every chunk as a separate TUI wakeup is expensive even when rendering is cached. Chord batches stream text at two layers:

- the agent runtime coalesces provider deltas before publishing stream events;
- the TUI event subscription adds a short micro-batch window for `StreamTextEvent`, so several paced text deltas can be handled by one `Update` cycle.

Non-streaming events do not open this TUI micro-batch window by themselves. If one arrives immediately after a stream-text delta, it may be returned with that in-flight batch after the short window; state transitions, idle events, errors, and user-visible boundaries still remain responsive.

Verification:

```bash
go test ./internal/tui -run 'TestWaitForAgentEvent'
go test ./internal/tui -run '^$' -bench 'BenchmarkWaitForAgentEvent.*MicroBatch' -benchmem
```

The paced stream benchmark reports `events/batch` and `batches/event`. Higher `events/batch` and lower `batches/event` mean fewer TUI wakeups for the same stream.

### 2. Stream rendering cadence

Streaming output is not fully rendered on every delta. Chord marks stream content dirty, defers the expensive view refresh, and flushes visible content on a cadence. Content-boundary events can still force a refresh when a new block, layout boundary, rollback, or other visible structural change must be shown promptly.

The important rule is to avoid turning ordinary token or newline deltas into immediate redraws. Otherwise long responses can make CPU track token frequency instead of frame cadence.

Useful checks:

```bash
go test ./internal/tui -run 'Test.*Stream.*Flush|Test.*Stream.*Boundary|Test.*Streaming'
go test ./internal/tui -run '^$' -bench 'BenchmarkStream.*' -benchmem
```

### 3. Streaming cheap path

Assistant and thinking blocks use a cheaper path while they are still streaming:

- stable prefixes can be cached and reused;
- the actively changing tail avoids full Markdown rendering where possible;
- final formatting happens when the content settles or when a real boundary requires it.

This prevents long responses from repeatedly re-rendering the entire Markdown document for every small append.

Useful checks:

```bash
go test ./internal/tui -run '^$' -bench 'BenchmarkRenderAssistantStreaming|BenchmarkStreamThinking' -benchmem
go test ./internal/tui/markdownutil -run '^$' -bench 'BenchmarkFindStreamingSettledFrontier|BenchmarkStreamingFrontierScanner' -benchmem
```

### 4. View and draw caching

The TUI keeps frame-level caches for expensive rendered regions such as the main viewport, info panel, status bar, and replay suffixes used for host redraw workarounds. Cache keys must include all inputs that affect visible output; missing inputs cause stale UI, while overly broad invalidation causes excess CPU.

Useful checks:

```bash
go test ./internal/tui -run '^$' -bench 'BenchmarkModelViewCached|BenchmarkRender.*' -benchmem
```

### 5. Status bar and animation cadence

Status indicators, spinners, elapsed timers, and progress displays are intentionally cadence-limited. Animation ticks should be started through the shared animation scheduling path so stale ticks can be ignored and duplicate tick chains do not accumulate.

If CPU is high while no visible content changes, inspect animation/status tick paths first.

Useful checks:

```bash
go test ./internal/tui -run 'Test.*Activity|Test.*Animation|Test.*Status'
```

### 6. Scroll and viewport batching

Mouse wheel input and touchpad gestures can generate many events. Chord batches scroll deltas, applies them on a short cadence, and avoids replaying images or rebuilding viewport measurements for every wheel event.

Large sessions also depend on viewport metadata caches and cold/off-screen block handling so only the visible window stays hot.

Useful checks:

```bash
go test ./internal/tui -run 'Test.*Scroll|Test.*Viewport'
go test ./internal/tui -run '^$' -bench 'BenchmarkViewportVisibleWindowBlockIDs|BenchmarkFindMatchesAtWidth' -benchmem
```

### 7. Startup and large-session memory

When resuming large sessions, Chord avoids eagerly heating the entire transcript. The current window is loaded for interaction, while metadata supports search, jump, directory, and later hydration. Idle background work can preheat nearby metadata and release off-screen caches.

Useful checks:

```bash
go test ./internal/tui -run 'Test.*Deferred|Test.*Startup|Test.*Transcript'
```

### 8. Request and context cost

Not all performance work is UI-side. Chord also reduces model-side cost by pruning stale tool outputs at request time, preserving structured summaries, and compacting long-running conversations before they hit the model limit. These optimizations reduce latency, token usage, and provider cost without deleting durable session history.

See [Configuration & Auth](./configuration.md#request-level-context-reduction) for the available context reduction settings.

## Recommended validation before performance-sensitive changes

For a small TUI streaming or rendering change:

```bash
go test ./internal/tui
go test ./internal/tui -run '^$' -bench 'BenchmarkWaitForAgentEvent.*MicroBatch|BenchmarkStream.*|BenchmarkModelViewCached|BenchmarkRender.*' -benchmem
```

For broader changes, compare before/after benchmark output with `benchstat`:

```bash
go test ./internal/tui -run '^$' -bench 'BenchmarkWaitForAgentEvent.*MicroBatch|BenchmarkStream.*|BenchmarkModelViewCached|BenchmarkRender.*|BenchmarkViewportVisibleWindowBlockIDs|BenchmarkFindMatchesAtWidth' -benchmem > /tmp/chord-bench-new.txt
benchstat /tmp/chord-bench-old.txt /tmp/chord-bench-new.txt
```

When benchmark results do not explain real CPU usage, collect a CPU profile during the problematic interaction:

```bash
CHORD_PPROF_PORT=6060 chord
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=15
```

## Interpreting common hotspots

- `bubbletea.(*Program).render`, terminal write calls, or renderer cell output: redraws are too frequent, or stream events are too fragmented.
- Markdown / glamour / goldmark functions during active streaming: the cheap streaming path is being bypassed.
- viewport measurement, line wrapping, or ANSI width functions: visible-window caches or line-count caches may be invalidating too often.
- animation or status tick handlers while nothing changes: stale or duplicate tick chains may be running.

## Practical tuning trade-offs

- Larger stream micro-batch windows reduce CPU but can make text feel less immediate.
- Longer content flush cadence reduces redraws but makes streamed output appear chunkier.
- More aggressive cache retention improves redraw speed but increases memory.
- More aggressive off-screen cache eviction lowers memory but can make first access to old transcript regions slower.

Prefer the smallest tuning change that fixes the measured bottleneck, and keep benchmarks close to the path being optimized.
