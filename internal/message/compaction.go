package message

import "strings"

const (
	CompactionSummaryHeader = "[Context Summary]\n"
	CompactionCompressedTag = "\n\n[Context compressed]"
	CompactionEvidenceTag   = "[Context Evidence]\n"
	// CompactionDisplayHint is a legacy marker: checkpoints written before the
	// display-hint tail was removed end with it. Nothing new appends it; parsers
	// of persisted sessions still recognize it as a region/tail boundary.
	CompactionDisplayHint = "\n\n[Context display hint]\n"

	// Session anchors are the one part of a checkpoint that is copied forward
	// verbatim instead of being regenerated. Compaction is recursive — each run
	// re-summarizes the previous checkpoint — so anything left to the
	// summarizer decays a little every time. The delimiters let the next
	// compaction lift the block back out exactly as it was written.
	CompactionAnchorsOpenTag  = "[Session Anchors]\n"
	CompactionAnchorsCloseTag = "\n[/Session Anchors]"
)

// Compaction summary modes, recorded on the checkpoint message. They differ in
// how much of the archived history survives in the checkpoint itself, which is
// what a reader needs in order to judge whether the summary can be trusted or
// the archives have to be re-read:
//
//   - ModelDriven: built deterministically from runtime facts plus the
//     continuation state the model submitted through compact_context; no
//     summarization model was called.
//   - ModelSummary: a summarization model condensed the archived head.
//   - StructuredFallback: summarization failed, so a structured digest of
//     runtime facts stands in for it.
//   - TruncateOnly: nothing could be summarized; the archives are the only
//     record of the compacted history.
const (
	CompactionSummaryModeModelDriven        = "model_driven_checkpoint"
	CompactionSummaryModeModelSummary       = "model_summary"
	CompactionSummaryModeStructuredFallback = "structured_fallback"
	CompactionSummaryModeTruncateOnly       = "truncate_only"
)

// CompactionAnchorsSection returns the verbatim body between the session-anchor
// delimiters, or "" when the content carries no anchors block.
func CompactionAnchorsSection(content string) string {
	start := strings.Index(content, CompactionAnchorsOpenTag)
	if start < 0 {
		return ""
	}
	start += len(CompactionAnchorsOpenTag)
	end := strings.Index(content[start:], CompactionAnchorsCloseTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(content[start : start+end])
}

func IsCompactionEvidenceArtifactText(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), CompactionEvidenceTag)
}
