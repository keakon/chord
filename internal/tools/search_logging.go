package tools

import (
	"log/slog"
	"time"
)

const slowSearchWarnThreshold = 750 * time.Millisecond

func logSlowSearch(toolName, searchPath, pattern, filter string, startedAt time.Time, scannedCountKey string, scannedCount, matchCount int, truncated bool) {
	duration := time.Since(startedAt)
	if duration < slowSearchWarnThreshold {
		return
	}
	if scannedCountKey == "" {
		scannedCountKey = "scanned_files"
	}
	slog.Warn("slow search tool",
		"tool", toolName,
		"search_path", searchPath,
		"pattern", pattern,
		"filter", filter,
		scannedCountKey, scannedCount,
		"match_count", matchCount,
		"truncated", truncated,
		"duration_ms", duration.Milliseconds(),
	)
}
