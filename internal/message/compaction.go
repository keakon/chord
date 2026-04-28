package message

import "strings"

const (
	CompactionSummaryHeader = "[Context Summary]\n"
	CompactionCompressedTag = "\n\n[Context compressed]"
	CompactionEvidenceTag   = "[Context Evidence]\n"
	CompactionDisplayHint   = "\n\n[Context display hint]\n"
)

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
