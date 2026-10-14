package agent

import (
	"github.com/keakon/chord/internal/message"
)

func evidencePackTokenBudget(contextLimit int) int {
	if contextLimit <= 0 {
		return CompactEvidenceMaxTokens
	}
	b := contextLimit * compactEvidencePercentNumer / compactEvidencePercentDenom
	b = max(b, compactEvidenceMinTokens)
	b = min(b, CompactEvidenceMaxTokens)
	return b
}

func splitMessagesForCompactionWithSelections(messages []message.Message, recentTail []message.Message, evidenceItems []evidenceItem) (head []message.Message, evidence []message.Message) {
	if len(messages) < 4 {
		return nil, nil
	}
	archiveEnd := len(messages)
	if len(recentTail) > 0 {
		archiveEnd = len(messages) - len(recentTail)
	}
	if archiveEnd <= 0 {
		return nil, nil
	}
	archiveHead := make([]message.Message, archiveEnd)
	copy(archiveHead, messages[:archiveEnd])
	if len(evidenceItems) == 0 {
		return archiveHead, nil
	}
	artifact := renderEvidenceArtifact(evidenceItems)
	return archiveHead, []message.Message{artifact}
}
