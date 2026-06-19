#!/usr/bin/env bash
set -euo pipefail

# Benchmarks and regression checks for the TUI hot paths that regressed during
# scroll/streaming profiling work. This script is intended for local runs and CI
# smoke checks; it combines correctness tests, alloc guards, and a small set of
# stable micro-benchmarks, including status-bar cache hit/dirty/miss paths.
#
# Usage:
#   ./scripts/bench_tui_regression.sh                 # smoke subset: default 1x benchtime
#   ./scripts/bench_tui_regression.sh ./old.txt ./new.txt   # optional benchstat compare
#   CHORD_BENCH_FULL=1 CHORD_BENCH_TIME=1s ./scripts/bench_tui_regression.sh # stable local comparison

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

TEST_PATTERN='TestInfoPanel|TestSeparator|TestViewportVisibleWindowBlockIDsUsesCachedStartsAndSpans|TestViewportVisibleWindowBlockIDsAllocsGuard|TestFindMatchesAtWidthAllocsGuard|TestStreamingAssistantCheapPathAllocsGuard|TestModelViewCachedAllocsGuard|TestScheduleStreamFlush|TestStreamTextDeltasReuseCachedViewUntilFlush|TestRenderToCachePreservesAllPlainTextCells|TestEnsureScreenBufferReusesExistingBuffer|TestStreamingAssistantUsesCheapWrapPath|TestHasVisibleInlineImageRequiresVisibleRenderedImage|TestViewShowsRestoringSessionPlaceholderDuringStartupRestore'
SMOKE_BENCH_PATTERN='^(BenchmarkRenderAssistantStreamingCard|BenchmarkRenderAssistantStreamingLongTextCard|BenchmarkStreamTextDeltaBurstDeferredView|BenchmarkStreamTextDeltaBurstCadenceFlush|BenchmarkStreamThinkingDeltaBurstDeferredView|BenchmarkToolCallUpdateArgsStreamingCadence|BenchmarkRenderToolCallCard|BenchmarkViewportVisibleWindowBlockIDs|BenchmarkViewportRenderLargeTranscriptScrollWindow|BenchmarkFindMatchesAtWidth|BenchmarkModelViewCached|BenchmarkRenderStatusBarModelPillCacheHit|BenchmarkOverlayListRenderCacheMiss|BenchmarkOverlayTableRenderCacheMiss)$'
FULL_BENCH_PATTERN='^(BenchmarkRenderAssistantCard|BenchmarkRenderAssistantCardCachedWarm|BenchmarkRenderAssistantStreamingCard|BenchmarkRenderAssistantStreamingTextCard|BenchmarkRenderAssistantStreamingLongTextCard|BenchmarkRenderAssistantStreamingLongTextCardCachedWarm|BenchmarkStreamTextDeltaBurstDeferredView|BenchmarkStreamTextDeltaBurstCadenceFlush|BenchmarkStreamThinkingDeltaBurstDeferredView|BenchmarkToolCallUpdateArgsStreamingCadence|BenchmarkRenderToolCallCard|BenchmarkViewportVisibleWindowBlockIDs|BenchmarkViewportRenderLargeTranscriptAtBottom|BenchmarkViewportRenderLargeTranscriptScrollWindow|BenchmarkApplyWheelScrollDeltaLargeTranscript|BenchmarkDeferredStartupTranscriptJumpOrdinalWindowSwitch|BenchmarkDeferredStartupTranscriptJumpTopBottomWindowSwitch|BenchmarkFindMatchesAtWidth|BenchmarkModelViewCached|BenchmarkRenderStatusBarModelPillCacheHit|BenchmarkRenderStatusBarAgentSnapshotDirty|BenchmarkRenderStatusBarSessionSummaryDirty|BenchmarkRenderConfirmDialogOpen|BenchmarkRenderQuestionDialogOpen|BenchmarkModelViewAtMentionPopupOpen|BenchmarkRenderDirectoryOpen|BenchmarkRenderSessionSelectDialogOpen|BenchmarkRenderUsageStatsDialogOpen|BenchmarkOverlayListRenderCacheHit|BenchmarkOverlayListRenderCacheMiss|BenchmarkOverlayTableRenderCacheHit|BenchmarkOverlayTableRenderCacheMiss)$'
FRONTIER_BENCH_PATTERN='^(BenchmarkFindStreamingSettledFrontierAppendSnapshots|BenchmarkStreamingFrontierScannerAppendSnapshots)$'

if [[ "${CHORD_BENCH_FULL:-}" == "1" ]]; then
  BENCH_PATTERN="$FULL_BENCH_PATTERN"
  : "${CHORD_BENCH_TIME:=1s}"
else
  BENCH_PATTERN="$SMOKE_BENCH_PATTERN"
  : "${CHORD_BENCH_TIME:=1x}"
fi

printf '==> Running targeted TUI regression tests\n'
go test ./internal/tui -run "$TEST_PATTERN"

printf '\n==> Running TUI benchmarks\n'
bench_args=(-run '^$' -bench "$BENCH_PATTERN" -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/tui "${bench_args[@]}" | tee /tmp/chord-tui-bench.txt

printf '\n==> Running streaming frontier benchmarks\n'
frontier_bench_args=(-run '^$' -bench "$FRONTIER_BENCH_PATTERN" -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  frontier_bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/tui/markdownutil "${frontier_bench_args[@]}" | tee -a /tmp/chord-tui-bench.txt

if [[ $# -eq 2 ]]; then
  if command -v benchstat >/dev/null 2>&1; then
    printf '\n==> benchstat comparison\n'
    benchstat "$1" "$2"
  else
    printf '\nbenchstat not found; install with:\n  go install golang.org/x/perf/cmd/benchstat@latest\n'
  fi
fi
