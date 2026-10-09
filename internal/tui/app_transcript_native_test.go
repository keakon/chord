package tui

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestMessagesToBlocksRestoresNativeReceipts(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		native := &message.NativeToolHistory{Target: "sample/test-model", RequestIDs: []string{"request-1"}, OutcomeUnknown: unknown}
		if !unknown {
			native.Calls = []message.HostedCall{{ID: "search-1", Result: json.RawMessage(`[]`)}}
		}
		var next int
		blocks := messagesToBlocks([]message.Message{{Role: message.RoleAssistant, NativeTools: native}}, &next)
		if len(blocks) != 1 || blocks[0].ToolID == "" || !blocks[0].ResultDone {
			t.Fatalf("restored=%+v", blocks)
		}
	}
}
