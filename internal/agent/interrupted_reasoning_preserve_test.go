package agent

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

// These tests lock in the interrupted-turn reasoning preservation fix: when a
// stream is interrupted after a reasoning item was finalized but before
// response.completed, the partial assistant message saved to history must carry
// that reasoning item. Replaying the message without its preceding reasoning
// item violates the Responses API pairing constraint and produces a 400.

// The in-memory shape is only half the contract: every request re-normalizes
// history for its target, and normalization drops native Responses items whose
// provenance does not vouch for them. An interrupted message that reaches the
// wire without its reasoning item is the exact 400 this fix exists to prevent,
// so the saved message is asserted through NormalizeForTarget as well.
func TestSavedInterruptedReasoningSurvivesNormalizeForTarget(t *testing.T) {
	a := newReadyTestMainAgent(t)
	providerCfg := llm.NewProviderConfig("openai", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{
			"gpt-6-astra": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	client := llm.NewClient(providerCfg, stubProvider{}, "gpt-6-astra", 4096, "sys")
	a.swapLLMClientWithRef(client, "gpt-6-astra", 128000, "openai/gpt-6-astra")

	turn := &Turn{}
	turn.appendPartialText("partial body")
	turn.appendPartialResponsesOutput(message.ResponsesOutputItem{Type: "reasoning", ID: "rs_1", EncryptedContent: "opaque-blob"})
	if !a.savePartialAssistantMsgForTurn(turn) {
		t.Fatal("expected a partial assistant message to be saved")
	}
	saved := a.ctxMgr.Snapshot()
	if saved[len(saved)-1].Provenance == nil {
		t.Fatal("interrupted message has no provenance; normalization will strip its reasoning")
	}

	target := modelcompat.TargetModel{
		ProviderID:              "openai",
		WireFamily:              modelcompat.WireFamilyOpenAIResponses,
		ModelID:                 "gpt-6-astra",
		ToolResultEncoding:      modelcompat.ToolResultEncodingOpenAIToolRole,
		SupportsStructuredTools: true,
	}
	out, report := modelcompat.NormalizeForTarget(saved, target, modelcompat.NormalizeOptions{StructuredTools: true})
	last := out[len(out)-1]
	if len(last.ResponsesOutput) != 2 || last.ResponsesOutput[0].Type != "reasoning" {
		t.Fatalf("normalized ResponsesOutput = %+v (report %+v), want [reasoning, message]", last.ResponsesOutput, report)
	}
	if last.ResponsesOutput[0].EncryptedContent != "opaque-blob" {
		t.Fatalf("reasoning payload = %q, want it carried through normalization", last.ResponsesOutput[0].EncryptedContent)
	}
}

func TestSavePartialAssistantMsgForTurnPreservesReasoning(t *testing.T) {
	cm := ctxmgr.NewManager(10000, 0)
	a := &MainAgent{ctxMgr: cm}
	turn := &Turn{}
	turn.appendPartialText("partial body")
	turn.appendPartialResponsesOutput(message.ResponsesOutputItem{Type: "reasoning", ID: "rs_1", EncryptedContent: "opaque-blob"})

	if !a.savePartialAssistantMsgForTurn(turn) {
		t.Fatal("expected a partial assistant message to be saved")
	}
	msgs := cm.Snapshot()
	if len(msgs) != 1 {
		t.Fatalf("saved message count = %d, want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.StopReason != "interrupted" {
		t.Fatalf("stop reason = %q, want interrupted", msg.StopReason)
	}
	if len(msg.ResponsesOutput) != 2 {
		t.Fatalf("ResponsesOutput = %+v, want [reasoning, message]", msg.ResponsesOutput)
	}
	r, m := msg.ResponsesOutput[0], msg.ResponsesOutput[1]
	if r.Type != "reasoning" || r.ID != "rs_1" || r.EncryptedContent != "opaque-blob" {
		t.Fatalf("reasoning item = %+v, want reasoning/rs_1/opaque-blob", r)
	}
	if m.Type != "message" || m.Role != "assistant" {
		t.Fatalf("message item = %+v, want assistant message", m)
	}
	if len(m.Content) != 1 || m.Content[0].Type != "output_text" || m.Content[0].Text != "partial body" {
		t.Fatalf("message item content = %+v, want output_text with partial body", m.Content)
	}
}

func TestSavePartialAssistantMsgForTurnWithoutTextDropsReasoning(t *testing.T) {
	// Reasoning with no streamed text has no message to pair with; persisting
	// the reasoning alone would leave an orphan reasoning item. Mirror the
	// existing no-text guard and save nothing.
	cm := ctxmgr.NewManager(10000, 0)
	a := &MainAgent{ctxMgr: cm}
	turn := &Turn{}
	turn.appendPartialResponsesOutput(message.ResponsesOutputItem{Type: "reasoning", ID: "rs_1", EncryptedContent: "opaque-blob"})

	if a.savePartialAssistantMsgForTurn(turn) {
		t.Fatal("expected no save when no partial text was streamed")
	}
	if msgs := cm.Snapshot(); len(msgs) != 0 {
		t.Fatalf("saved message count = %d, want 0", len(msgs))
	}
}

func TestSubAgentPreserveInterruptedPartialPreservesReasoning(t *testing.T) {
	cm := ctxmgr.NewManager(10000, 0)
	sub := &SubAgent{turn: &Turn{}, ctxMgr: cm}
	sub.turn.appendPartialText("  partial body  ")
	sub.turn.appendPartialResponsesOutput(message.ResponsesOutputItem{Type: "reasoning", ID: "rs_1", EncryptedContent: "opaque-blob"})

	sub.preserveInterruptedPartial()
	msgs := cm.Snapshot()
	if len(msgs) != 1 {
		t.Fatalf("saved message count = %d, want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.StopReason != "interrupted" {
		t.Fatalf("stop reason = %q, want interrupted", msg.StopReason)
	}
	if len(msg.ResponsesOutput) != 2 {
		t.Fatalf("ResponsesOutput = %+v, want [reasoning, message]", msg.ResponsesOutput)
	}
	r, m := msg.ResponsesOutput[0], msg.ResponsesOutput[1]
	if r.Type != "reasoning" || r.EncryptedContent != "opaque-blob" {
		t.Fatalf("reasoning item = %+v, want reasoning/opaque-blob", r)
	}
	if m.Type != "message" || len(m.Content) != 1 || m.Content[0].Text != "partial body" {
		t.Fatalf("message item = %+v, want output_text with trimmed partial body", m.Content)
	}
}

func TestRollbackDrainsAccumulatedReasoningItems(t *testing.T) {
	// A rollback abandons the current streaming attempt and retries from the
	// beginning; the abandoned attempt's accumulated reasoning must not survive
	// into the replacement attempt's partial accumulator.
	turn := &Turn{}
	turn.appendPartialResponsesOutput(message.ResponsesOutputItem{Type: "reasoning", ID: "rs_1", EncryptedContent: "a"})
	turn.appendPartialResponsesOutput(message.ResponsesOutputItem{Type: "reasoning", ID: "rs_2", EncryptedContent: "b"})
	reducer := streamToolDeltaReducer{
		turn:                   turn,
		emit:                   func(evt AgentEvent) {},
		drainPartialOnRollback: true,
	}

	if !reducer.Handle(message.StreamDelta{Type: message.StreamDeltaRollback, Rollback: &message.RollbackDelta{Reason: "provider_retry"}}) {
		t.Fatal("rollback delta was not handled")
	}
	if got := turn.drainPartialResponsesOutput(); len(got) != 0 {
		t.Fatalf("reasoning items after rollback = %d, want drained", len(got))
	}
}

func TestInterruptedAssistantResponsesOutputShape(t *testing.T) {
	reasoning := []message.ResponsesOutputItem{
		{Type: "reasoning", ID: "rs_1", EncryptedContent: "opaque-blob"},
	}
	out := interruptedAssistantResponsesOutput(reasoning, "partial body")
	if len(out) != 2 {
		t.Fatalf("output len = %d, want 2 (reasoning + message)", len(out))
	}
	if out[0].Type != "reasoning" {
		t.Fatalf("output[0] = %+v, want reasoning first", out[0])
	}
	if out[1].Type != "message" || out[1].Role != "assistant" {
		t.Fatalf("output[1] = %+v, want assistant message last", out[1])
	}
	if len(out[1].Content) != 1 || out[1].Content[0].Text != "partial body" {
		t.Fatalf("output[1] content = %+v, want output_text partial body", out[1].Content)
	}

	// No reasoning items: no native payload at all. The partial text already
	// replays from Content, and any ResponsesOutput would raise the replay
	// floor to synthesized for every later request in the session.
	plain := interruptedAssistantResponsesOutput(nil, "partial body")
	if plain != nil {
		t.Fatalf("reasoning-less output = %+v, want nil", plain)
	}
}

func TestTurnAppendDrainPartialResponsesOutput(t *testing.T) {
	var turn Turn
	turn.appendPartialResponsesOutput(message.ResponsesOutputItem{Type: "reasoning", ID: "rs_1"})
	turn.appendPartialResponsesOutput(message.ResponsesOutputItem{Type: "reasoning", ID: "rs_2"})
	drained := turn.drainPartialResponsesOutput()
	if len(drained) != 2 || drained[0].ID != "rs_1" || drained[1].ID != "rs_2" {
		t.Fatalf("drained = %+v, want [rs_1 rs_2] in order", drained)
	}
	if again := turn.drainPartialResponsesOutput(); len(again) != 0 {
		t.Fatalf("second drain = %+v, want empty", again)
	}
	// nil receiver is safe, mirroring drainPartialText.
	var nilTurn *Turn
	if nilTurn.drainPartialResponsesOutput() != nil {
		t.Fatal("nil turn drain must return nil")
	}
	nilTurn.appendPartialResponsesOutput(message.ResponsesOutputItem{Type: "reasoning"})
}
