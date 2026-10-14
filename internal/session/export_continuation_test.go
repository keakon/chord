package session

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestExportContinuationUsageRoundTripAndIsolation(t *testing.T) {
	for _, known := range []bool{false, true} {
		original := &message.TokenUsage{InputTokens: 40200, OutputTokens: 70000, Context: &message.ContextTokenUsage{Known: known, PromptTokens: 100, OutputTokens: 70000}}
		exported, err := Export([]message.Message{{Role: message.RoleAssistant, Content: "sample", Usage: original}}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(exported)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ExportedSession
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		restored := decoded.ToMessages()
		if !reflect.DeepEqual(restored[0].Usage, original) {
			t.Fatal("usage lost on export/import")
		}
		exported.Messages[0].Usage.Context.PromptTokens = 1
		restored[0].Usage.Context.PromptTokens = 2
		if original.Context.PromptTokens != 100 || decoded.Messages[0].Usage.Context.PromptTokens != 100 {
			t.Fatal("shared context observation")
		}
	}
}
