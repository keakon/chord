package sessionimport

import (
	"fmt"
	"time"
)

// ImportReport captures a detailed report of a session import conversion.
// It is written to <sessionDir>/import-report.json.
type ImportReport struct {
	Source          string    `json:"source"`
	SourcePath      string    `json:"source_path,omitempty"`
	SourceSessionID string    `json:"source_session_id,omitempty"`
	ImportedAt      time.Time `json:"imported_at"`

	ToolMode      string `json:"tool_mode,omitempty"`
	ReasoningMode string `json:"reasoning_mode,omitempty"`

	ImportedMessages int `json:"imported_messages"`
	SkippedEntries   int `json:"skipped_entries,omitempty"`

	// Structured tool import statistics.
	StructuredToolCalls   int `json:"structured_tool_calls,omitempty"`
	StructuredToolResults int `json:"structured_tool_results,omitempty"`

	// Downgrade/fallback statistics.
	UnsupportedToolCalls    int `json:"unsupported_tool_calls,omitempty"`
	UnsupportedToolResults  int `json:"unsupported_tool_results,omitempty"`
	MissingToolDeclarations int `json:"missing_tool_declarations,omitempty"`

	// Content source deduplication.
	SkippedDuplicates int `json:"skipped_duplicates,omitempty"`

	// Reasoning.
	ReasoningBlocksSkipped int `json:"reasoning_blocks_skipped,omitempty"`

	// Legacy fields kept for backwards compatibility with existing tooling.
	ToolEntriesRendered int `json:"tool_entries_rendered,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

func (r *ImportReport) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}
