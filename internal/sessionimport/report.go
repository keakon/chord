package sessionimport

import (
	"fmt"
	"time"
)

// ImportReport captures a detailed report of a session import conversion.
// It is written to <sessionDir>/import-report.json.
//
// Phase 1 intentionally focuses on text-mode imports (no structured tools),
// so the schema is oriented around warnings and downgrade statistics.
// It is safe to extend this struct in later phases.
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
