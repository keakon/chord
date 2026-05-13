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
	switch strings.TrimSpace(msg.Provenance.WireFamily) {
	case modelcompat.WireFamilyOpenAIChat, modelcompat.WireFamilyOpenAIResponses:
		return true
	default:
		return false
	}
}
