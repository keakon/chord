package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type recordingLengthRecoveryProvider struct {
	lastTuning llm.RequestTuning
}

func (p *recordingLengthRecoveryProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	tuning llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	p.lastTuning = tuning
	return &message.Response{Content: "ok", StopReason: "stop"}, nil
}

func (p *recordingLengthRecoveryProvider) Complete(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning llm.RequestTuning,
) (*message.Response, error) {
	return p.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, nil)
}

func TestHandleLLMResponseTruncatedMalformedStartsLengthRecovery(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()

	payload := &LLMResponsePayload{
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "Bash",
			Args: json.RawMessage(`{"error":"malformed tool call arguments from model"}`),
		}},
		StopReason: "length",
	}

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})

	if a.turn == nil {
		t.Fatal("turn unexpectedly cleared")
	}
	if !a.turn.InLengthRecovery {
		t.Fatal("expected turn to enter length recovery")
	}
	if a.turn.LengthRecoveryCount != 1 {
		t.Fatalf("LengthRecoveryCount = %d, want 1", a.turn.LengthRecoveryCount)
	}
	if a.turn.MalformedCount != 0 {
		t.Fatalf("MalformedCount = %d, want 0 during recovery path", a.turn.MalformedCount)
	}
	if got := a.turn.LastTruncatedToolName; got != "Bash" {
		t.Fatalf("LastTruncatedToolName = %q, want Bash", got)
	}
	if got := a.pendingRecoveryPrompt; got == "" {
		t.Fatal("expected recovery prompt to be set as turn overlay")
	} else if want := `tool "Bash"`; !strings.Contains(got, want) {
		t.Fatalf("recovery prompt %q does not contain %q", got, want)
	}
}

func TestHandleLLMResponseTruncatedMalformedAbortMentionsCompactWhenCompactionUnavailable(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.turn.LengthRecoveryCount = maxLengthRecoveryAttempts
	a.turn.MalformedCount = maxMalformedToolCalls - 1
	a.turn.LengthRecoveryAutoCompactAttempted = true
	a.autoCompactRequested.Store(true)

	payload := &LLMResponsePayload{
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "Bash",
			Args: json.RawMessage(`{"error":"malformed tool call arguments from model"}`),
		}},
		StopReason: "length",
	}

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})

	var gotErr error
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if errEvt, ok := evt.(ErrorEvent); ok {
			gotErr = errEvt.Err
			break
		}
	}
	if gotErr == nil {
		t.Fatal("expected error event")
	}
	if !strings.Contains(gotErr.Error(), "/compact") {
		t.Fatalf("error %q does not mention /compact", gotErr)
	}
	if a.turn != nil {
		t.Fatal("expected turn to be cleared after abort")
	}
}

func TestHandleLLMResponseTruncatedMalformedSchedulesCompactionBeforeAbort(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.turn.LengthRecoveryCount = maxLengthRecoveryAttempts
	a.turn.MalformedCount = maxMalformedToolCalls - 1

	payload := &LLMResponsePayload{
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "Bash",
			Args: json.RawMessage(`{"error":"malformed tool call arguments from model"}`),
		}},
		StopReason: "length",
	}

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})

	if a.turn == nil {
		t.Fatal("expected turn to remain active while compaction is pending")
	}
	if !a.turn.LengthRecoveryAutoCompactAttempted {
		t.Fatal("expected length recovery auto compaction to be marked attempted")
	}
	if !a.IsCompactionRunning() {
		t.Fatal("expected compaction running state after scheduling recovery compaction")
	}
}

func TestResumePendingMainLLMAfterCompactionLengthRecoveryReinjectsRecoveryPrompt(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	provider := &recordingLengthRecoveryProvider{}
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, provider, "test-model", 1024, "")
	a.newTurn()
	a.turn.InLengthRecovery = true
	a.turn.LastTruncatedToolName = "Bash"
	a.pendingUserMessages = []pendingUserMessage{{Content: "queued after compaction"}}

	pending := &pendingMainLLMCall{
		turnID:       a.turn.ID,
		turnEpoch:    a.turn.Epoch,
		sessionEpoch: a.sessionEpoch,
		continuation: compactionResumeLengthRecovery,
	}

	a.resumePendingMainLLMAfterCompaction(pending, true)

	// Recovery prompt should be set as request-scoped overlay (not in ctxMgr).
	if got := a.pendingRecoveryPrompt; got == "" {
		t.Fatal("expected recovery prompt to be set as turn overlay after compaction resume")
	} else if want := `tool "Bash"`; !strings.Contains(got, want) {
		t.Fatalf("recovery prompt %q does not contain %q", got, want)
	}
	if _, err := a.llmClient.CompleteStream(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("CompleteStream after compaction resume: %v", err)
	}
	if provider.lastTuning.OpenAI.ParallelToolCalls == nil || *provider.lastTuning.OpenAI.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls override after compaction resume = %#v, want false", provider.lastTuning.OpenAI.ParallelToolCalls)
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 after length-recovery resume merge", got)
	}
	snapshot := a.ctxMgr.Snapshot()
	foundQueued := false
	for _, msg := range snapshot {
		if msg.Role == "user" && msg.Content == "queued after compaction" {
			foundQueued = true
			break
		}
	}
	if !foundQueued {
		t.Fatal("expected queued user message to be merged before length-recovery resume")
	}
}

func TestHandleLLMResponseValidResponseClearsLengthRecoveryState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.turn.InLengthRecovery = true
	a.turn.LengthRecoveryCount = 2
	a.turn.LastTruncatedToolName = "Bash"

	payload := &LLMResponsePayload{
		Content:    "done",
		StopReason: "stop",
	}

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})

	if a.turn != nil {
		t.Fatal("expected turn to finish on valid idle response")
	}
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) == 0 {
		t.Fatal("expected assistant response to be appended to context")
	}
	last := snapshot[len(snapshot)-1]
	if last.Role != "assistant" || last.Content != "done" {
		t.Fatalf("last message = %#v, want assistant done", last)
	}
}

func TestHandleLLMResponseLengthRecoveryRejectsMultipleToolCalls(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.turn.InLengthRecovery = true
	a.turn.LengthRecoveryCount = 1

	payload := &LLMResponsePayload{
		ToolCalls: []message.ToolCall{
			{ID: "call-1", Name: "Read", Args: json.RawMessage(`{"path":"a","offset":0,"limit":1}`)},
			{ID: "call-2", Name: "Read", Args: json.RawMessage(`{"path":"b","offset":0,"limit":1}`)},
		},
		StopReason: "stop",
	}

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})

	if a.turn == nil {
		t.Fatal("turn unexpectedly cleared")
	}
	if a.turn.InLengthRecovery {
		t.Fatal("expected recovery state to clear after fallback retry path")
	}
	if a.turn.LengthRecoveryCount != 0 {
		t.Fatalf("LengthRecoveryCount = %d, want 0 after fallback retry path", a.turn.LengthRecoveryCount)
	}
	if a.turn.MalformedCount != 0 {
		t.Fatalf("MalformedCount = %d, want 0 because stop_reason=stop does not enter truncation retry path", a.turn.MalformedCount)
	}
}

// TestResumePendingMainLLMAfterCompactionLengthRecoveryFailureAborts verifies
// that when compaction fails for a length-recovery continuation, the turn is
// aborted with /compact guidance instead of retrying recovery.
func TestResumePendingMainLLMAfterCompactionLengthRecoveryFailureAborts(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, &recordingLengthRecoveryProvider{}, "test-model", 1024, "")
	a.newTurn()
	a.turn.InLengthRecovery = true
	a.turn.LastTruncatedToolName = "Bash"

	pending := &pendingMainLLMCall{
		turnID:       a.turn.ID,
		turnEpoch:    a.turn.Epoch,
		sessionEpoch: a.sessionEpoch,
		continuation: compactionResumeLengthRecovery,
	}

	// recheckGate=false means compaction failed.
	a.resumePendingMainLLMAfterCompaction(pending, false)

	if a.turn != nil {
		t.Fatal("expected turn to be cleared after compaction failure abort")
	}
	if a.pendingRecoveryPrompt != "" {
		t.Fatal("expected no recovery prompt overlay after compaction failure abort")
	}
	// Check that ErrorEvent was emitted with /compact hint.
	var foundErr bool
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if errEvt, ok := evt.(ErrorEvent); ok {
			if strings.Contains(errEvt.Err.Error(), "/compact") || strings.Contains(errEvt.Err.Error(), "new conversation") {
				foundErr = true
				break
			}
		}
	}
	if !foundErr {
		t.Fatal("expected error event mentioning /compact or new conversation after compaction failure")
	}
}

// TestScheduleCompactionForLengthRecoveryUsesLengthRecoveryTrigger verifies
// that scheduleCompactionForLengthRecovery sets the LengthRecovery trigger.
func TestScheduleCompactionForLengthRecoveryUsesLengthRecoveryTrigger(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()

	if !a.scheduleCompactionForLengthRecovery() {
		t.Fatal("expected scheduleCompactionForLengthRecovery to return true")
	}
	if !a.turn.LengthRecoveryAutoCompactAttempted {
		t.Fatal("expected LengthRecoveryAutoCompactAttempted to be set")
	}
	if !a.IsCompactionRunning() {
		t.Fatal("expected compaction to be running")
	}
	if a.compactionState.trigger.LengthRecovery != true {
		t.Fatal("expected LengthRecovery trigger to be true")
	}
	if a.compactionState.trigger.UsageDriven != false {
		t.Fatal("expected UsageDriven trigger to be false for length recovery compaction")
	}
}

// TestScheduleCompactionForLengthRecoveryOnlyOncePerTurn verifies that the
// one-shot bit prevents auto compaction from firing more than once per turn.
func TestScheduleCompactionForLengthRecoveryOnlyOncePerTurn(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()

	if !a.scheduleCompactionForLengthRecovery() {
		t.Fatal("expected first scheduleCompactionForLengthRecovery to succeed")
	}
	// Second attempt should be blocked by one-shot bit.
	if a.scheduleCompactionForLengthRecovery() {
		t.Fatal("expected second scheduleCompactionForLengthRecovery to be blocked")
	}
}

// TestRecoveryCompactionFailureDoesNotAdvanceUsageBreaker verifies that a
// failed length-recovery compaction does not affect the usage-driven failure
// counter / suppression state.
func TestRecoveryCompactionFailureDoesNotAdvanceUsageBreaker(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	turnID := a.turn.ID
	turnEpoch := a.turn.Epoch

	// Simulate starting a compaction with LengthRecovery trigger.
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch, turnID: turnID, turnEpoch: turnEpoch},
		compactionTrigger{LengthRecovery: true}, continuationPlan{
			kind:      compactionResumeLengthRecovery,
			turnID:    turnID,
			turnEpoch: turnEpoch,
		})
	a.turn.LengthRecoveryAutoCompactAttempted = true

	// Simulate compaction failure directly. This will abort the turn
	// (recheckGate=false for length recovery -> abort path).
	a.handleCompactionFailed(Event{
		Type:   EventCompactionFailed,
		TurnID: turnID,
		Payload: &compactionFailure{
			err:    fmt.Errorf("simulated compaction failure"),
			planID: 1,
			target: compactionTarget{sessionEpoch: a.sessionEpoch, turnID: turnID, turnEpoch: turnEpoch},
		},
	})

	// The usage-driven breaker should not be affected.
	if a.autoCompactFailureState.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures = %d, want 0 (length recovery failure should not advance usage breaker)",
			a.autoCompactFailureState.ConsecutiveFailures)
	}
	if a.autoCompactFailureState.SuppressedUntilTurn != 0 {
		t.Fatal("expected no suppression after length recovery failure")
	}
	// Turn should be aborted.
	if a.turn != nil {
		t.Fatal("expected turn to be aborted after length recovery compaction failure")
	}
}

// TestRecoveryPromptNotInCompactionCheckpoint verifies that the recovery prompt
// overlay is consumed (one-shot) by buildTurnOverlayMessages and does not
// persist across LLM rounds.
func TestRecoveryPromptNotInCompactionCheckpoint(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	// Set a recovery prompt overlay.
	a.pendingRecoveryPrompt = "System note: recovery prompt for test."

	// buildTurnOverlayMessages should consume it.
	overlays := a.buildTurnOverlayMessages()
	if len(overlays) == 0 {
		t.Fatal("expected at least one overlay containing recovery prompt")
	}
	foundRecovery := false
	for _, o := range overlays {
		if strings.Contains(o.Content, "recovery prompt for test") {
			foundRecovery = true
			break
		}
	}
	if !foundRecovery {
		t.Fatal("expected recovery prompt in overlays")
	}

	// Second call should not include the recovery prompt (one-shot).
	overlays2 := a.buildTurnOverlayMessages()
	for _, o := range overlays2 {
		if strings.Contains(o.Content, "recovery prompt for test") {
			t.Fatal("recovery prompt should be consumed after first buildTurnOverlayMessages call")
		}
	}
}
