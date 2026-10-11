package message

import (
	"encoding/json"
	"testing"
)

func TestQuestionDecisionIdentityUsesTheDurableFact(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fact     string
		kind     string
		decision bool
	}{
		{name: "answer", fact: `{"operation":"answer","result":{"outcome":"answered"}}`, decision: true},
		{name: "revision", fact: `{"operation":"revise","result":{"outcome":"answered"}}`, decision: true},
		{name: "default", fact: `{"operation":"expire","result":{"outcome":"defaulted"}}`, decision: true},
		{name: "typed decision", kind: KindQuestionResult, fact: `{"result":{"outcome":"answered"}}`, decision: true},
		{name: "withdrawal", fact: `{"operation":"withdraw_requirement","source_id":"input-1","result":{"outcome":"cancelled"}}`},
		{name: "replacement", fact: `{"operation":"replace_requirement","source_id":"input-2","result":{"outcome":"superseded"}}`},
		{name: "local state", kind: KindQuestionState, fact: `{"result":{"outcome":"answered"}}`},
		{name: "creation", fact: `{"operation":"create"}`},
		{name: "null result", fact: `{"result":null}`},
		{name: "malformed result", fact: `{"result":"answered"}`},
		{name: "invalid JSON", fact: `{`},
		{name: "ordinary input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := Message{Role: RoleUser, Kind: tc.kind, Content: "Input text", Question: json.RawMessage(tc.fact)}
			if got := IsQuestionDecision(msg); got != tc.decision {
				t.Fatalf("decision = %v, want %v", got, tc.decision)
			}
			wantUser := !tc.decision && tc.kind != KindQuestionState
			if got := IsUserAuthored(msg); got != wantUser {
				t.Fatalf("user-authored = %v, want %v", got, wantUser)
			}
		})
	}
}
