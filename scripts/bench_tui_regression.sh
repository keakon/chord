#!/usr/bin/env bash
set -euo pipefail

# Benchmarks and regression checks for runtime hot paths. This script is
# intended for local runs and CI smoke checks; it combines correctness tests,
# alloc guards, and a small set of stable micro-benchmarks, including TUI,
# streaming, context-reduction, session, recovery, and patch-planning paths.
#
# Usage:
#   ./scripts/bench_tui_regression.sh                 # smoke subset: default 1x benchtime
#   ./scripts/bench_tui_regression.sh ./old.txt ./new.txt   # optional benchstat compare
#   CHORD_BENCH_FULL=1 CHORD_BENCH_TIME=1s ./scripts/bench_tui_regression.sh # stable local comparison
#
# Paced flow benchmarks run with a fixed iteration count (1x in smoke, 100x in
# full), so raising CHORD_BENCH_TIME never inflates their wall-clock cost.

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

TEST_PATTERN='TestInfoPanel|TestSeparator|TestViewportVisibleWindowBlockIDsUsesCachedStartsAndSpans|TestViewportVisibleWindowBlockIDsAllocsGuard|TestFindMatchesAtWidthAllocsGuard|TestStreamingAssistantCheapPathAllocsGuard|TestModelViewCachedAllocsGuard|TestScheduleStreamFlush|TestStreamTextDeltasReuseCachedViewUntilFlush|TestRenderToCachePreservesAllPlainTextCells|TestEnsureScreenBufferReusesExistingBuffer|TestStreamingAssistantUsesCheapWrapPath|TestHasVisibleInlineImageRequiresVisibleRenderedImage|TestViewShowsRestoringSessionPlaceholderDuringStartupRestore|TestToggleBlockBumpsRenderVersion|TestToggleNoOpDoesNotBumpRenderVersion|TestSpaceToggleInvalidatesMainRenderCache|TestSplitStyleRenderParity'
SMOKE_BENCH_PATTERN='^(BenchmarkRenderAssistantStreamingCard|BenchmarkRenderAssistantStreamingLongTextCard|BenchmarkRenderToolCallCard|BenchmarkRenderEditDiffSinglePair|BenchmarkRenderEditDiffUnevenBlock|BenchmarkRenderEditDiffLargeUnevenBlock|BenchmarkViewportVisibleWindowBlockIDs|BenchmarkViewportRenderLargeTranscriptScrollWindow|BenchmarkRenderBashLargeCardCollapsed|BenchmarkRenderBashLargeCardExpandedCold|BenchmarkRenderBashLargeCardExpandedWarmWindow|BenchmarkViewportRenderLargeExpandedToolWindow|BenchmarkViewportRenderLargeExpandedToolAfterSpill|BenchmarkFindMatchesAtWidth|BenchmarkModelViewCached|BenchmarkMessagesToBlocksLargeSession|BenchmarkRenderStatusBarModelPillCacheHit|BenchmarkRenderInfoPanelMaxFilesScroll|BenchmarkOverlayListRenderCacheMiss|BenchmarkOverlayTableRenderCacheMiss|BenchmarkRenderThinkingStreamingIncrementalLarge|BenchmarkViewportDropOffScreenCachesLargeTranscript|BenchmarkCodeHighlighterPerFileSection)$'
FULL_BENCH_PATTERN='^(BenchmarkRenderAssistantCard|BenchmarkRenderAssistantCardCachedWarm|BenchmarkRenderAssistantStreamingCard|BenchmarkRenderAssistantStreamingTextCard|BenchmarkRenderAssistantStreamingLongTextCard|BenchmarkRenderAssistantStreamingLongTextCardCachedWarm|BenchmarkRenderToolCallCard|BenchmarkRenderEditDiffSinglePair|BenchmarkRenderEditDiffUnevenBlock|BenchmarkRenderEditDiffLargeUnevenBlock|BenchmarkViewportVisibleWindowBlockIDs|BenchmarkViewportRenderLargeTranscriptAtBottom|BenchmarkViewportRenderLargeTranscriptScrollWindow|BenchmarkRenderBashLargeCardCollapsed|BenchmarkRenderBashLargeCardExpandedCold|BenchmarkRenderBashLargeCardExpandedWarmWindow|BenchmarkViewportRenderLargeExpandedToolWindow|BenchmarkViewportRenderLargeExpandedToolAfterSpill|BenchmarkApplyWheelScrollDeltaLargeTranscript|BenchmarkDeferredStartupTranscriptJumpOrdinalWindowSwitch|BenchmarkDeferredStartupTranscriptJumpTopBottomWindowSwitch|BenchmarkFindMatchesAtWidth|BenchmarkModelViewCached|BenchmarkMessagesToBlocksLargeSession|BenchmarkRenderStatusBarModelPillCacheHit|BenchmarkRenderInfoPanelMaxFilesScroll|BenchmarkRenderInfoPanelMaxFilesContentMiss|BenchmarkRenderStatusBarAgentSnapshotDirty|BenchmarkRenderStatusBarSessionSummaryDirty|BenchmarkRenderConfirmDialogOpen|BenchmarkRenderQuestionDialogOpen|BenchmarkModelViewAtMentionPopupOpen|BenchmarkRenderDirectoryOpen|BenchmarkRenderSessionSelectDialogOpen|BenchmarkRenderUsageStatsDialogOpen|BenchmarkOverlayListRenderCacheHit|BenchmarkOverlayListRenderCacheMiss|BenchmarkOverlayTableRenderCacheHit|BenchmarkOverlayTableRenderCacheMiss|BenchmarkRenderThinkingStreamingIncrementalLarge)$'
# Flow benchmarks that rebuild their model inside b.StopTimer(): each round's wall
# clock is dominated by the untimed setup, so a time-based benchtime would balloon
# the iteration count and run for minutes. Keep them on a fixed count.
PACED_BENCH_PATTERN='^(BenchmarkStreamTextDeltaBurstDeferredView|BenchmarkStreamTextDeltaBurstCadenceFlush|BenchmarkStreamThinkingDeltaBurstDeferredView|BenchmarkToolCallUpdateArgsStreamingCadence)$'
FRONTIER_BENCH_PATTERN='^(BenchmarkFindStreamingSettledFrontierAppendSnapshots|BenchmarkStreamingFrontierScannerAppendSnapshots)$'
SSE_BENCH_PATTERN='^(BenchmarkSSEParseWithCallbackCumulative|BenchmarkSSEParseWithCallbackIncremental|BenchmarkSSEParseWithCollector)$'
TRUNCATE_BENCH_PATTERN='^BenchmarkTruncateStringHeadTail$'
SESSION_BENCH_PATTERN='^(BenchmarkImportFromBytesLargeSession|BenchmarkExportedSessionToMessagesLargeSession)$'
RECOVERY_BENCH_PATTERN='^(BenchmarkLoadMessagesLargeSession.*|BenchmarkLoadMessagesBySize)$'
TOOLS_BENCH_PATTERN='^BenchmarkBuildApplyPatchPlan(LargeFile|MultiFile)$'

# Paced flow benchmarks always use a fixed iteration count (never CHORD_BENCH_TIME):
# 1x keeps the smoke scan cheap, 100x gives full mode an average worth comparing.
if [[ "${CHORD_BENCH_FULL:-}" == "1" ]]; then
  BENCH_PATTERN="$FULL_BENCH_PATTERN"
  : "${CHORD_BENCH_TIME:=1s}"
  PACED_BENCH_TIME=100x
else
  BENCH_PATTERN="$SMOKE_BENCH_PATTERN"
  : "${CHORD_BENCH_TIME:=1x}"
  PACED_BENCH_TIME=1x
fi

printf '==> Running targeted TUI regression tests\n'
go test ./internal/tui -run "$TEST_PATTERN"

printf '\n==> Running TUI benchmarks\n'
bench_args=(-run '^$' -bench "$BENCH_PATTERN" -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/tui "${bench_args[@]}" | tee /tmp/chord-tui-bench.txt

printf '\n==> Running TUI paced benchmarks\n'
go test ./internal/tui -run '^$' -bench "$PACED_BENCH_PATTERN" -benchmem -benchtime "$PACED_BENCH_TIME" | tee -a /tmp/chord-tui-bench.txt

printf '\n==> Running streaming frontier benchmarks\n'
frontier_bench_args=(-run '^$' -bench "$FRONTIER_BENCH_PATTERN" -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  frontier_bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/tui/markdownutil "${frontier_bench_args[@]}" | tee -a /tmp/chord-tui-bench.txt

printf '\n==> Running SSE benchmarks\n'
sse_bench_args=(-run '^$' -bench "$SSE_BENCH_PATTERN" -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  sse_bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/llm "${sse_bench_args[@]}" | tee -a /tmp/chord-tui-bench.txt

printf '\n==> Running text truncation benchmarks\n'
truncate_bench_args=(-run '^$' -bench "$TRUNCATE_BENCH_PATTERN" -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  truncate_bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/llm "${truncate_bench_args[@]}" | tee -a /tmp/chord-tui-bench.txt

printf '\n==> Running context reduction benchmarks\n'
context_bench_args=(-run '^$' -bench '^BenchmarkPrepareMessagesForLLM' -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  context_bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/agent "${context_bench_args[@]}" | tee -a /tmp/chord-tui-bench.txt

printf '\n==> Running session loading benchmarks\n'
session_bench_args=(-run '^$' -bench "$SESSION_BENCH_PATTERN" -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  session_bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/session "${session_bench_args[@]}" | tee -a /tmp/chord-tui-bench.txt

printf '\n==> Running session recovery benchmarks\n'
recovery_bench_args=(-run '^$' -bench "$RECOVERY_BENCH_PATTERN" -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  recovery_bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/recovery "${recovery_bench_args[@]}" | tee -a /tmp/chord-tui-bench.txt

printf '\n==> Running apply_patch planning benchmarks\n'
tools_bench_args=(-run '^$' -bench "$TOOLS_BENCH_PATTERN" -benchmem)
if [[ -n "${CHORD_BENCH_TIME:-}" ]]; then
  tools_bench_args+=(-benchtime "${CHORD_BENCH_TIME}")
fi
go test ./internal/tools "${tools_bench_args[@]}" | tee -a /tmp/chord-tui-bench.txt

if [[ $# -eq 2 ]]; then
  if command -v benchstat >/dev/null 2>&1; then
    printf '\n==> benchstat comparison\n'
    benchstat "$1" "$2"
  else
    printf '\nbenchstat not found; install with:\n  go install golang.org/x/perf/cmd/benchstat@latest\n'
  fi
fi
