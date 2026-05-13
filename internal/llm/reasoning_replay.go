package llm

import (
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func messageAllowsReasoningReplay(msg message.Message) bool {
	if strings.TrimSpace(msg.ReasoningContent) == "" || msg.Provenance == nil {
		return false
	}
	return wireFamilyAllowsReasoningReplay(strings.TrimSpace(msg.Provenance.WireFamily))
}

func wireFamilyAllowsReasoningReplay(wireFamily string) bool {
	switch strings.TrimSpace(wireFamily) {
	case modelcompat.WireFamilyOpenAIChat, modelcompat.WireFamilyOpenAIResponses:
		return true
	default:
		return false
	}
}
