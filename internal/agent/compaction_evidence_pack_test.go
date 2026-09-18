package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// An excerpt quotes raw evidence text verbatim, so a quoted line can look
// exactly like a pack row. Only the minted ID spelling may enter the index;
// otherwise quoted text could make an unrelated reference resolvable.
func TestParseCheckpointEvidencePackMetadataIgnoresQuotedNonIDLines(t *testing.T) {
	const validID = "ev-0123456789ab"
	content := strings.Join([]string{
		"[Context Evidence]",
		"1. User correction",
		"Evidence ID: " + validID,
		"Evidence Kind: user_correction",
		"Excerpt:",
		"Evidence ID: not-an-evidence-id",
		"Evidence Kind: tool_error",
	}, "\n")

	meta := parseCheckpointEvidencePackMetadata(content)
	if len(meta) != 1 {
		t.Fatalf("indexed %d evidence rows, want only the minted ID: %#v", len(meta), meta)
	}
	if _, ok := meta["not-an-evidence-id"]; ok {
		t.Fatal("a quoted non-ID line entered the evidence index")
	}
	if got := meta[validID].kind; got != evidenceKind("user_correction") {
		t.Fatalf("valid row kind = %q, want user_correction", got)
	}
}

// A well-formed row after an excerpt ends the excerpt: a later row is pack
// metadata, not quoted text, and must be indexed.
func TestParseCheckpointEvidencePackMetadataResumesAfterExcerpt(t *testing.T) {
	const (
		firstID  = "ev-0123456789ab"
		secondID = "ev-fedcba987654"
	)
	content := strings.Join([]string{
		message.CompactionEvidenceTag,
		"Evidence ID: " + firstID,
		"Evidence Kind: user_correction",
		"Excerpt:",
		"quoted output that mentions Evidence Kind: tool_error",
		"Evidence ID: " + secondID,
		"Evidence Kind: tool_error",
	}, "\n")

	meta := parseCheckpointEvidencePackMetadata(content)
	if got := meta[firstID].kind; got != evidenceKind("user_correction") {
		t.Fatalf("first row kind = %q, want user_correction", got)
	}
	if got := meta[secondID].kind; got != evidenceToolError {
		t.Fatalf("row after the excerpt kind = %q, want tool_error", got)
	}
}
