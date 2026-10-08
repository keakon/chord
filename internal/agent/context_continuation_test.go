package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

func TestAgentContextContinuationRewritesOnlyItsOwnTranscript(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	mainMessages := []message.Message{
		{Role: message.RoleUser, Content: "main request"},
		{Role: message.RoleAssistant, ThinkingBlocks: []message.ThinkingBlock{{Signature: "signature"}}},
		{Role: message.RoleSystem, Kind: message.KindQuestionState},
	}
	a.ctxMgr.RestoreMessages(mainMessages)
	if err := a.recoveryManager().RewriteLog(identity.MainAgentID, mainMessages); err != nil {
		t.Fatal(err)
	}
	sub := newControllableTestSubAgent(t, a, "worker-continue")
	workerMessages := []message.Message{{Role: message.RoleUser, Content: "worker request"}, mainMessages[1], mainMessages[2]}
	sub.ctxMgr.RestoreMessages(workerMessages)
	if err := a.recoveryManager().RewriteLog(sub.instanceID, workerMessages); err != nil {
		t.Fatal(err)
	}
	if err := sub.prepareContextContinuation(); err != nil {
		t.Fatal(err)
	}
	mainPersisted, err := a.recoveryManager().LoadMessages(identity.MainAgentID)
	if err != nil || !reflect.DeepEqual(mainPersisted, mainMessages) {
		t.Fatalf("worker continuation changed main history: err=%v messages=%#v", err, mainPersisted)
	}
	workerPersisted, err := a.recoveryManager().LoadMessages(sub.instanceID)
	if err != nil || len(workerPersisted) != 2 || workerPersisted[1].Kind != message.KindQuestionState {
		t.Fatalf("worker continuation lost facts: err=%v messages=%#v", err, workerPersisted)
	}
	if err := a.prepareContextContinuation(); err != nil {
		t.Fatal(err)
	}
	mainPersisted, err = a.recoveryManager().LoadMessages(identity.MainAgentID)
	if err != nil || len(mainPersisted) != 2 || mainPersisted[1].Kind != message.KindQuestionState {
		t.Fatalf("main continuation lost facts: err=%v messages=%#v", err, mainPersisted)
	}
}

func TestContextContinuationPreservesQuestionFactsAndPendingAppends(t *testing.T) {
	manager := ctxmgr.NewManager(1000, 0)
	consumption := json.RawMessage(`{"operation":"consume","transaction_id":"consume-1","consumed":["result-1"]}`)
	manager.RestoreMessages([]message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ThinkingBlocks: []message.ThinkingBlock{{Thinking: "partial", Signature: "signature"}}, Question: consumption, RequestBatch: 4},
		{Role: message.RoleUser, Kind: message.KindQuestionState, Question: json.RawMessage(`{"transaction_id":"answer-1"}`)},
		{Role: message.RoleSystem, Kind: message.KindQuestionState, Question: json.RawMessage(`{"transaction_id":"pause-1"}`)},
	})
	var persisted []message.Message
	err := prepareContextContinuation(manager, func() {
		manager.Append(message.Message{Role: message.RoleSystem, Kind: message.KindQuestionState, Question: json.RawMessage(`{"transaction_id":"resume-1"}`)})
	}, func(messages []message.Message) error {
		persisted = messages
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := manager.Snapshot()
	if len(got) != 5 || got[1].Kind != message.KindQuestionState || string(got[1].Question) != string(consumption) || got[1].RequestBatch != 4 {
		t.Fatalf("continued transcript lost facts: %#v", got)
	}
	if got[1].Role != message.RoleSystem || len(got[1].ThinkingBlocks) != 0 || got[2].Role != message.RoleUser {
		t.Fatalf("conversation payload or local user fact changed: %#v", got)
	}
	if !reflect.DeepEqual(got, persisted) {
		t.Fatal("durable and in-memory transcripts differ")
	}
}

func TestContextContinuationRewriteFailureKeepsHistory(t *testing.T) {
	manager := ctxmgr.NewManager(1000, 0)
	manager.RestoreMessages([]message.Message{
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleAssistant, ThinkingBlocks: []message.ThinkingBlock{{Thinking: "partial", Signature: "signature"}}},
		{Role: message.RoleSystem, Kind: message.KindQuestionState},
	})
	before := manager.Snapshot()
	failure := errors.New("write failed")
	err := prepareContextContinuation(manager, nil, func([]message.Message) error { return failure })
	if !errors.Is(err, failure) || !reflect.DeepEqual(before, manager.Snapshot()) {
		t.Fatalf("rewrite failure changed history: err=%v", err)
	}
}

func TestContextContinuationKeepsAnsweredAndToolTails(t *testing.T) {
	for _, tail := range []message.Message{
		{Role: message.RoleAssistant, Content: "answer", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "reasoning"}}},
		{Role: message.RoleAssistant, ThinkingBlocks: []message.ThinkingBlock{{Thinking: "reasoning"}}, ToolCalls: []message.ToolCall{{ID: "call-1"}}},
		{Role: message.RoleUser, Content: "new request"},
	} {
		manager := ctxmgr.NewManager(1000, 0)
		manager.RestoreMessages([]message.Message{tail, {Role: message.RoleSystem, Kind: message.KindQuestionState}})
		before := manager.Snapshot()
		if err := prepareContextContinuation(manager, nil, func([]message.Message) error {
			t.Fatal("rewrote a continuable tail")
			return nil
		}); err != nil || !reflect.DeepEqual(before, manager.Snapshot()) {
			t.Fatalf("tail changed: %#v err=%v", tail, err)
		}
	}
}
