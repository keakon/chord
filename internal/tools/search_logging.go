package tools

import (
	"github.com/keakon/golog/log"
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
	log.Warnf("slow search tool tool=%v search_path=%v pattern=%v filter=%v %s=%v match_count=%v truncated=%v duration_ms=%v", toolName, searchPath, pattern, filter, scannedCountKey, scannedCount, matchCount, truncated, duration.Milliseconds())
}
