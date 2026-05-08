package sessionimport

import (
	"fmt"
	"time"
)

// ImportReport captures a detailed report of a session import conversion.
// It is written to <sessionDir>/import-report.json.
//
// The schema is oriented around warnings and downgrade statistics, since
// imports are text-mode only (no structured tools).
type ImportReport struct {
	Source          string    `json:"source"`
	SourcePath      string    `json:"source_path,omitempty"`
	SourceSessionID string    `json:"source_session_id,omitempty"`
	ImportedAt      time.Time `json:"imported_at"`

	ToolMode      string `json:"tool_mode,omitempty"`
	ReasoningMode string `json:"reasoning_mode,omitempty"`

	ImportedMessages int `json:"imported_messages"`
	SkippedEntries   int `json:"skipped_entries,omitempty"`

	ReasoningBlocksSkipped int `json:"reasoning_blocks_skipped,omitempty"`
	ToolEntriesRendered    int `json:"tool_entries_rendered,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

func (r *ImportReport) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}
