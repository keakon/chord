package message

import (
	"encoding/json"
	"testing"
)

func TestContextUsageRoundTripAndClone(t *testing.T) {
	original := Message{Role: RoleAssistant, Usage: &TokenUsage{InputTokens: 40200, Context: &ContextTokenUsage{Known: true, PromptTokens: 100, OutputTokens: 70000}}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Usage.Context.PromptTokens != 100 {
		t.Fatal("context lost")
	}
	clone := original.Clone()
	clone.Usage.Context.PromptTokens = 1
	if original.Usage.Context.PromptTokens != 100 {
		t.Fatal("clone shared context")
	}
}
