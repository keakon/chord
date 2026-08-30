package message

import "strings"

const (
	CompactionSummaryHeader = "[Context Summary]\n"
	CompactionCompressedTag = "\n\n[Context compressed]"
	CompactionEvidenceTag   = "[Context Evidence]\n"
	CompactionDisplayHint   = "\n\n[Context display hint]\n"

	// Session anchors are the one part of a checkpoint that is copied forward
	// verbatim instead of being regenerated. Compaction is recursive — each run
	// re-summarizes the previous checkpoint — so anything left to the
	// summarizer decays a little every time. The delimiters let the next
	// compaction lift the block back out exactly as it was written.
	CompactionAnchorsOpenTag  = "[Session Anchors]\n"
	CompactionAnchorsCloseTag = "\n[/Session Anchors]"
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

func MergeCompactionSummaryAndEvidence(summaryContent, evidenceContent string) string {
	summaryContent = strings.TrimRight(summaryContent, "\n")
	evidenceContent = strings.TrimSpace(evidenceContent)
	if summaryContent == "" || evidenceContent == "" {
		return summaryContent
	}
	if strings.Contains(summaryContent, CompactionEvidenceTag) {
		return summaryContent
	}
	if idx := strings.Index(summaryContent, CompactionDisplayHint); idx >= 0 {
		return strings.TrimRight(summaryContent[:idx], "\n") + "\n\n" + evidenceContent + summaryContent[idx:]
	}
	return summaryContent + "\n\n" + evidenceContent
}
