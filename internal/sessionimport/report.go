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

	ReasoningMode string `json:"reasoning_mode,omitempty"`

	ImportedMessages int `json:"imported_messages"`
	SkippedEntries   int `json:"skipped_entries,omitempty"`

	// Structured tool import statistics.
	StructuredToolCalls   int `json:"structured_tool_calls,omitempty"`
	StructuredToolResults int `json:"structured_tool_results,omitempty"`

	// Codex code-mode diagnostics: how many freeform `exec` inputs were
	// analyzed down to a single known nested call.
	CodexExecScripts int `json:"codex_exec_scripts,omitempty"`

	// Unmapped-call accounting. A tool call whose recorded values no Chord
	// tool takes stays the source call it was, under the source name and
	// arguments, so only the call side has a counter.
	KeptSourceToolCalls     int `json:"kept_source_tool_calls,omitempty"`
	MissingToolDeclarations int `json:"missing_tool_declarations,omitempty"`
	MissingToolCallIDs      int `json:"missing_tool_call_ids,omitempty"`

	// Content source resolution.
	SkippedDuplicates        int `json:"skipped_duplicates,omitempty"`
	DuplicateSourceConflicts int `json:"duplicate_source_conflicts,omitempty"`
	SkippedStatusEvents      int `json:"skipped_status_events,omitempty"`

	// Reasoning.
	ReasoningBlocksSkipped    int `json:"reasoning_blocks_skipped,omitempty"`
	SkippedAmbiguousReasoning int `json:"skipped_ambiguous_reasoning,omitempty"`

	// Usage attribution.
	UsageEventsAttached int `json:"usage_events_attached,omitempty"`
	UsageEventsSkipped  int `json:"usage_events_skipped,omitempty"`

	// Validation.
	ValidationFailures int `json:"validation_failures,omitempty"`

	// Source-specific diagnostics.
	Claude   *ClaudeImportReport   `json:"claude,omitempty"`
	OpenCode *OpenCodeImportReport `json:"opencode,omitempty"`

	// Import summary retained for CLI/report display.
	ToolEntriesRendered int `json:"tool_entries_rendered,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

// OpenCodeImportReport records how an OpenCode export's compactions were
// mapped. A completed compaction is imported as the context the source model
// saw — a checkpoint plus the recent messages it kept — so the history the
// source had already folded away is replaced rather than imported beside its
// own summary. The counters make that replacement visible and checkable
// against the source export, which keeps the full history.
type OpenCodeImportReport struct {
	// CompactionCheckpoints counts the checkpoints mapped from the source's
	// compactions. At most one shapes an import: the newest usable one.
	CompactionCheckpoints int `json:"compaction_checkpoints,omitempty"`
	// CompactionHeadReplaced counts the messages the checkpoint replaced.
	CompactionHeadReplaced int `json:"compaction_head_replaced,omitempty"`
	// CompactionTailKept counts the recent messages kept between the
	// checkpoint and the compaction marker.
	CompactionTailKept int `json:"compaction_tail_kept,omitempty"`
	// MarkersSkipped counts compaction markers that did not become the
	// checkpoint, whether superseded by a newer one or unusable.
	MarkersSkipped int `json:"compaction_markers_skipped,omitempty"`
}

type ClaudeImportReport struct {
	NonSidechainMessages     int      `json:"non_sidechain_messages,omitempty"`
	SidechainMessagesSkipped int      `json:"sidechain_messages_skipped,omitempty"`
	SidechainAgentIDs        []string `json:"sidechain_agent_ids,omitempty"`
	MetadataEntries          int      `json:"metadata_entries,omitempty"`
	CompactBoundaries        int      `json:"compact_boundaries,omitempty"`
	Tombstones               int      `json:"tombstones,omitempty"`
	TerminalCandidates       int      `json:"terminal_candidates,omitempty"`
	SelectedSpanLength       int      `json:"selected_span_length,omitempty"`
	SelectionReason          string   `json:"selection_reason,omitempty"`
	SelectedLeafUUID         string   `json:"selected_leaf_uuid,omitempty"`
	StructuredToolCalls      int      `json:"structured_tool_calls,omitempty"`
	StructuredToolResults    int      `json:"structured_tool_results,omitempty"`
	DowngradedVisibleEntries int      `json:"downgraded_visible_entries,omitempty"`
	Diagnostics              []string `json:"diagnostics,omitempty"`
}

func (r *ImportReport) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}
