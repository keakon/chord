package ctxmgr

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNativeReplayAccountingExcludesDisplayMirrors(t *testing.T) {
	raw := json.RawMessage(`{"type":"text","text":"Sample native content"}`)
	base := message.Message{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Items: []json.RawMessage{raw}}}
	mirrored := base.Clone()
	mirrored.Content = "Sample native content"
	mirrored.ThinkingBlocks = []message.ThinkingBlock{{Thinking: "Sample reasoning", Signature: "sample-signature"}}
	mirrored.ToolCalls = []message.ToolCall{{ID: "call-1", Name: "sample_lookup", Args: json.RawMessage(`{"query":"sample"}`)}}
	mirrored.ResponsesOutput = []message.ResponsesOutputItem{{ID: "item-1", EncryptedContent: "sample-payload"}}
	for _, msg := range []message.Message{base, mirrored} {
		if got := EstimateMessageTokens(msg); got != len(raw)/3 {
			t.Fatalf("tokens=%d, want %d", got, len(raw)/3)
		}
		if got := EstimateMessagesBytes([]message.Message{msg}); got != len(raw) {
			t.Fatalf("bytes=%d, want %d", got, len(raw))
		}
	}
	mirrored.NativeTools.Items = nil
	if EstimateMessageTokens(mirrored) <= 1 || EstimateMessagesBytes([]message.Message{mirrored}) == 0 {
		t.Fatal("receipt without raw items lost ordinary content")
	}
}
