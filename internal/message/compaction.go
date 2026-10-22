package message

import (
	"strings"

	"github.com/keakon/chord/internal/texttruncate"
)

const (
	CompactionSummaryHeader = "[Context Summary]\n"
	CompactionCompressedTag = "\n\n[Context compressed]"
	CompactionEvidenceTag   = "[Context Evidence]\n"
	// CompactionTypedStateHeading introduces the machine-carryable state
	// block inside a checkpoint body: one JSON bullet the carry parser reads
	// back. The TUI may project it into a structured display without changing
	// the persisted payload.
	CompactionTypedStateHeading = "## Typed Checkpoint State"
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

	// The labels name the three lists inside a session-anchors block. The
	// superseded label states the meaning itself: a reader of the checkpoint
	// has no other legend telling it these lines are no longer in force.
	CompactionAnchorsRequestLabel     = "Original request:"
	CompactionAnchorsConstraintsLabel = "Standing constraints:"
	CompactionAnchorsSupersededLabel  = "Superseded constraints (no longer in force):"

	// CompactionAnchorRequestChars bounds the original-request anchor a
	// checkpoint carries. The line is copied forward verbatim by every later
	// compaction, so a long first request must not grow each checkpoint
	// without limit. It lives here because the bound has to agree across
	// writers: an imported anchor is matched against a freshly rendered one,
	// so a second bound elsewhere would silently stop matching.
	CompactionAnchorRequestChars = 700
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

// CompactionAnchorLine normalizes one line of a session-anchors block. The
// agent's anchor renderer and the session importer share it so an imported
// anchor is byte-identical to the one a native compaction writes for the same
// request: the next compaction lifts the block back out verbatim, and request
// projections match a recovered line against a freshly rendered one.
//
// The result is a single line carrying no Markdown bullet or heading markers
// that could re-render as structure inside the block, bounded to maxChars runes
// with middle elision so a long request keeps both of its ends.
func CompactionAnchorLine(text string, maxChars int) string {
	text = strings.TrimSpace(strings.ReplaceAll(texttruncate.Snippet(text, maxChars), "\n", " "))
	text = strings.TrimSpace(text)
	// Drop a leading list bullet ("-", "*", "+"), then any run of "#" heading
	// markers. The space between a bullet and a heading must not stop the
	// strip, otherwise "- # forged" would normalize to "# forged" and
	// re-render as a heading a model could forge inside the checkpoint region.
	text = strings.TrimSpace(strings.TrimPrefix(text, "-"))
	text = strings.TrimSpace(strings.TrimPrefix(text, "*"))
	text = strings.TrimSpace(strings.TrimPrefix(text, "+"))
	for strings.HasPrefix(text, "#") {
		text = strings.TrimSpace(strings.TrimPrefix(text, "#"))
	}
	return text
}

func IsCompactionEvidenceArtifactText(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), CompactionEvidenceTag)
}
