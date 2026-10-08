package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestExtractThinkingTranslationBlocksSkipsHiddenResponsesReasoning(t *testing.T) {
	hidden := message.Message{
		Role:             message.RoleAssistant,
		ReasoningContent: "private reasoning sample",
		Provenance:       &message.MessageProvenance{WireFamily: message.WireFamilyResponses},
	}
	if blocks := extractThinkingTranslationBlocks(hidden); len(blocks) != 0 {
		t.Fatalf("hidden Responses reasoning entered translation: %+v", blocks)
	}

	plain := message.Message{Role: message.RoleAssistant, ReasoningContent: "visible reasoning sample"}
	blocks := extractThinkingTranslationBlocks(plain)
	if len(blocks) != 1 || blocks[0].Original != "visible reasoning sample" {
		t.Fatalf("plain reasoning blocks = %+v, want one translated block", blocks)
	}
}
