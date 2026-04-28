#!/usr/bin/env bash
set -euo pipefail

# Benchmarks and regression checks for the TUI hot paths that regressed during
# scroll/streaming profiling work. This script is intended for local runs and CI
# smoke checks; it combines correctness tests, alloc guards, and a small set of
# stable micro-benchmarks, including status-bar cache hit/dirty/miss paths.
#
# Usage:
#   ./scripts/bench_tui_regression.sh
#   ./scripts/bench_tui_regression.sh ./old.txt ./new.txt   # optional benchstat compare

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

TEST_PATTERN='TestInfoPanel|TestSeparator|TestViewportVisibleWindowBlockIDsUsesCachedStartsAndSpans|TestViewportVisibleWindowBlockIDsAllocsGuard|TestFindMatchesAtWidthAllocsGuard|TestStreamingAssistantCheapPathAllocsGuard|TestModelViewCachedAllocsGuard|TestScheduleStreamFlush|TestRenderToCachePreservesAllPlainTextCells|TestEnsureScreenBufferReusesExistingBuffer|TestStreamingAssistantUsesCheapWrapPath|TestHasVisibleInlineImageRequiresVisibleRenderedImage|TestViewShowsRestoringSessionPlaceholderDuringStartupRestore'
BENCH_PATTERN='BenchmarkRenderAssistantCard|BenchmarkRenderAssistantStreamingCard|BenchmarkRenderToolCallCard|BenchmarkViewportVisibleWindowBlockIDs|BenchmarkFindMatchesAtWidth|BenchmarkModelViewCached|BenchmarkRenderStatusBarModelPillCacheHit|BenchmarkRenderStatusBarAgentSnapshotDirty|BenchmarkRenderStatusBarSessionSummaryDirty'

printf '==> Running targeted TUI regression tests\n'
go test ./internal/tui -run "$TEST_PATTERN"

printf '\n==> Running TUI benchmarks\n'
go test ./internal/tui -run '^$' -bench "$BENCH_PATTERN" -benchmem | tee /tmp/chord-tui-bench.txt

if [[ $# -eq 2 ]]; then
  if command -v benchstat >/dev/null 2>&1; then
    printf '\n==> benchstat comparison\n'
    benchstat "$1" "$2"
  else
    printf '\nbenchstat not found; install with:\n  go install golang.org/x/perf/cmd/benchstat@latest\n'
  fi
fi
