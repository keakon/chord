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
	SourceTitle     string    `json:"source_title,omitempty"`
	ImportedAt      time.Time `json:"imported_at"`

	ReasoningMode string `json:"reasoning_mode,omitempty"`

	ImportedMessages int `json:"imported_messages"`
	SkippedEntries   int `json:"skipped_entries,omitzero"`

	// Structured tool import statistics.
	StructuredToolCalls   int `json:"structured_tool_calls,omitzero"`
	StructuredToolResults int `json:"structured_tool_results,omitzero"`

	// Codex code-mode diagnostics: how many freeform `exec` inputs were
	// analyzed down to a single known nested call.
	CodexExecScripts int `json:"codex_exec_scripts,omitzero"`

	// Unmapped-call accounting. A tool call whose recorded values no Chord
	// tool takes stays the source call it was, under the source name and
	// arguments, so only the call side has a counter.
	KeptSourceToolCalls     int `json:"kept_source_tool_calls,omitzero"`
	MissingToolDeclarations int `json:"missing_tool_declarations,omitzero"`
	MissingToolCallIDs      int `json:"missing_tool_call_ids,omitzero"`

	// Content source resolution.
	SkippedDuplicates        int `json:"skipped_duplicates,omitzero"`
	DuplicateSourceConflicts int `json:"duplicate_source_conflicts,omitzero"`
	SkippedStatusEvents      int `json:"skipped_status_events,omitzero"`

	// Reasoning.
	ReasoningBlocksSkipped    int `json:"reasoning_blocks_skipped,omitzero"`
	SkippedAmbiguousReasoning int `json:"skipped_ambiguous_reasoning,omitzero"`

	// Usage attribution.
	UsageEventsAttached int `json:"usage_events_attached,omitzero"`
	UsageEventsSkipped  int `json:"usage_events_skipped,omitzero"`

	// Validation.
	ValidationFailures int `json:"validation_failures,omitzero"`

	// Source-specific diagnostics.
	Claude   *ClaudeImportReport   `json:"claude,omitempty"`
	OpenCode *OpenCodeImportReport `json:"opencode,omitempty"`
	Codex    *CodexImportReport    `json:"codex,omitempty"`

	// Import summary retained for CLI/report display.
	ToolEntriesRendered int `json:"tool_entries_rendered,omitzero"`

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
	CompactionCheckpoints int `json:"compaction_checkpoints,omitzero"`
	// CompactionHeadReplaced counts the messages the checkpoint replaced.
	CompactionHeadReplaced int `json:"compaction_head_replaced,omitzero"`
	// CompactionTailKept counts the recent messages kept between the
	// checkpoint and the compaction marker.
	CompactionTailKept int `json:"compaction_tail_kept,omitzero"`
	// MarkersSkipped counts compaction markers that did not become the
	// checkpoint, whether superseded by a newer one or unusable.
	MarkersSkipped int `json:"compaction_markers_skipped,omitzero"`
}

type ClaudeImportReport struct {
	NonSidechainMessages     int      `json:"non_sidechain_messages,omitzero"`
	SidechainMessagesSkipped int      `json:"sidechain_messages_skipped,omitzero"`
	SidechainAgentIDs        []string `json:"sidechain_agent_ids,omitempty"`
	MetadataEntries          int      `json:"metadata_entries,omitzero"`
	CompactBoundaries        int      `json:"compact_boundaries,omitzero"`
	Tombstones               int      `json:"tombstones,omitzero"`
	TerminalCandidates       int      `json:"terminal_candidates,omitzero"`
	SelectedSpanLength       int      `json:"selected_span_length,omitzero"`
	SelectionReason          string   `json:"selection_reason,omitempty"`
	SelectedLeafUUID         string   `json:"selected_leaf_uuid,omitempty"`
	StructuredToolCalls      int      `json:"structured_tool_calls,omitzero"`
	StructuredToolResults    int      `json:"structured_tool_results,omitzero"`
	DowngradedVisibleEntries int      `json:"downgraded_visible_entries,omitzero"`
	// WrappedUserBlocksSkipped counts the machine-written command wrapper text
	// blocks (slash-command invocations, local-command output) dropped from
	// user messages; WrappedUserLinesSkipped counts the messages that carried
	// nothing else and were skipped whole as a result.
	WrappedUserBlocksSkipped int      `json:"wrapped_user_blocks_skipped,omitzero"`
	WrappedUserLinesSkipped  int      `json:"wrapped_user_lines_skipped,omitzero"`
	Diagnostics              []string `json:"diagnostics,omitempty"`
}

// CodexImportReport records how a Codex rollout's history_base chain was
// stitched into one import. Referenced forks (thread/revert, sub-agent
// spawns) write their inherited history as a chain of pages instead of
// replaying it, so the counters and page list make the stitched provenance
// visible and checkable against the source files.
type CodexImportReport struct {
	// HistoryPagesStitched counts the rollout pages concatenated into the
	// import, including the selected page. A page chain that cannot be
	// followed completely is rejected outright, so any successful import
	// reports the full chain.
	HistoryPagesStitched int `json:"history_pages_stitched,omitzero"`
	// HistoryItemsFromBase counts the rollout records imported from pages
	// before the selected one — history the source carried over from its
	// history_base chain rather than recording in the selected page.
	HistoryItemsFromBase int `json:"history_items_from_base,omitzero"`
	// Pages lists the stitched rollout files oldest first; the last entry is
	// the selected page.
	Pages []string `json:"pages,omitempty"`
}

func (r *ImportReport) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}
