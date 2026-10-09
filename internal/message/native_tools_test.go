package message

import (
	"encoding/json"
	"testing"
)

func TestNativeReceiptCloneAndRoundTrip(t *testing.T) {
	msg := Message{Role: RoleAssistant, NativeTools: &NativeToolHistory{Target: "sample/test-model", RequestIDs: []string{"request-1"}, Authorization: NativeToolAuthorization{Tool: "sample_tool", Contract: "sample.contract", Constraints: json.RawMessage(`{"scope":"sample"}`)}, Items: []json.RawMessage{json.RawMessage(`{"type":"opaque","signature":"signed"}`)}, Calls: []HostedCall{{ID: "search-1", Input: json.RawMessage(`{"query":"sample"}`), Result: json.RawMessage(`[]`)}}}}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	cloned := restored.Clone()
	cloned.NativeTools.RequestIDs[0] = "changed"
	cloned.NativeTools.Authorization.Constraints[2] = 'X'
	cloned.NativeTools.Items[0][2] = 'X'
	cloned.NativeTools.Calls[0].Input[2] = 'X'
	cloned.NativeTools.Calls[0].Result[0] = 'X'
	got, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("canonical history mutated: %s", got)
	}
}
