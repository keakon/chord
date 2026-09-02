package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestFallbackBoundaryDefersForSmallerModelWindow(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{
		Context: config.ContextConfig{
			Compaction: config.CompactionConfig{Threshold: 0.8},
		},
	}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 900})
	a.newTurn()
	a.started.Store(true)

	reply := make(chan llmFallbackBoundaryResult, 1)
	a.handleLLMFallbackBoundary(Event{
		Type:   EventLLMFallbackBoundary,
		TurnID: a.turn.ID,
		Payload: &llmFallbackBoundaryPayload{
			turnID:               a.turn.ID,
			messages:             a.ctxMgr.Snapshot(),
			fallbackModelRef:     "provider/smaller-model",
			fallbackContextLimit: 500,
			fallbackInputLimit:   500,
			reply:                reply,
		},
	})
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})

	result := <-reply
	if !isFallbackModelDownshiftCompactionPending(result.err) {
		t.Fatalf("fallback boundary error = %v, want pending model-downshift compaction", result.err)
	}
	if !a.IsCompactionRunning() {
		t.Fatal("fallback boundary must start compaction before the smaller model request")
	}
	if got := a.compactionState.trigger; got != compactionTriggerModelDownshift {
		t.Fatalf("compaction trigger = %q, want %q", got, compactionTriggerModelDownshift)
	}
	if !a.compactionState.downshiftSuspended {
		t.Fatal("fallback request must be suspended behind model-downshift compaction")
	}
}

// TestFallbackBoundaryDefersForSmallerInputBudgetOnly verifies that a fallback
// whose total context window matches the current model but whose effective
// input budget is smaller is still treated as a downshift: the auto-compaction
// line tracks the input budget, so the same context crosses the new line even
// when the overall window is unchanged.
func TestFallbackBoundaryDefersForSmallerInputBudgetOnly(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{
		Context: config.ContextConfig{
			Compaction: config.CompactionConfig{Threshold: 0.8},
		},
	}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000, 960, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 900})
	a.newTurn()
	a.started.Store(true)

	reply := make(chan llmFallbackBoundaryResult, 1)
	a.handleLLMFallbackBoundary(Event{
		Type:   EventLLMFallbackBoundary,
		TurnID: a.turn.ID,
		Payload: &llmFallbackBoundaryPayload{
			turnID:               a.turn.ID,
			messages:             a.ctxMgr.Snapshot(),
			fallbackModelRef:     "provider/smaller-input-model",
			fallbackContextLimit: 1000, // same total window as the current model
			fallbackInputLimit:   500,
			reply:                reply,
		},
	})
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})

	result := <-reply
	if !isFallbackModelDownshiftCompactionPending(result.err) {
		t.Fatalf("fallback boundary error = %v, want pending model-downshift compaction", result.err)
	}
	if !a.IsCompactionRunning() {
		t.Fatal("fallback boundary must start compaction when only the input budget shrinks")
	}
	if got := a.compactionState.trigger; got != compactionTriggerModelDownshift {
		t.Fatalf("compaction trigger = %q, want %q", got, compactionTriggerModelDownshift)
	}
}

// TestFallbackBoundaryLetsLargerWindowFallbackThrough verifies that a fallback
// whose context and input budgets are both at least the current model's is
// dispatched immediately: no crossing was introduced, so deferring the request
// behind a compaction would only stall an otherwise healthy fallback.
func TestFallbackBoundaryLetsLargerWindowFallbackThrough(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{
		Context: config.ContextConfig{
			Compaction: config.CompactionConfig{Threshold: 0.8},
		},
	}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000, 960, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 900})
	a.newTurn()
	a.started.Store(true)

	reply := make(chan llmFallbackBoundaryResult, 1)
	a.handleLLMFallbackBoundary(Event{
		Type:   EventLLMFallbackBoundary,
		TurnID: a.turn.ID,
		Payload: &llmFallbackBoundaryPayload{
			turnID:               a.turn.ID,
			messages:             a.ctxMgr.Snapshot(),
			fallbackModelRef:     "provider/larger-model",
			fallbackContextLimit: 2000,
			fallbackInputLimit:   1500,
			reply:                reply,
		},
	})

	result := <-reply
	if result.err != nil {
		t.Fatalf("fallback boundary error = %v, want nil (larger window needs no compaction)", result.err)
	}
	if a.IsCompactionRunning() {
		t.Fatal("larger-window fallback must not start a compaction")
	}
}

// TestDownshiftCompactionFailureResumeBypassesFallbackGateOnce covers the
// compaction-failure escape hatch for a downshift-suspended main round: when
// the model-downshift compaction fails, resumePendingMainLLMAfterCompaction
// respawns the suspended request with the fallback-downshift gate bypassed.
// The respawned request runs through a real llm.Client, so the primary
// provider failing again reaches the fallback boundary on the event channel;
// the bypass flag must be set there so the smaller-window fallback is
// dispatched instead of being deferred behind a second compaction (which would
// never run and would leave the turn stuck).
func TestDownshiftCompactionFailureResumeBypassesFallbackGateOnce(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{
		Context: config.ContextConfig{
			Compaction: config.CompactionConfig{Threshold: 0.8},
		},
	}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(128000, 100000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 60000})
	a.newTurn()
	a.started.Store(true)
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})

	primaryCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Input: 100000, Output: 4096}},
		},
	}, []string{"primary-key"})
	fallbackCfg := llm.NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 64000, Input: 64000, Output: 4096}},
		},
	}, []string{"fallback-key"})
	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 500, Message: "primary unavailable"},
	}}}
	fallbackImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		resp: &message.Response{Content: "fallback response after compaction failure", StopReason: "stop"},
	}}}
	client := llm.NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   64000,
		InputLimit:     64000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	// Mirror the post-suspend state handleCompactionDownshiftSuspend leaves
	// behind: the downshift compaction is running with the deferred main round
	// recorded on it. No worker goroutine is started; the injected failure
	// event settles the state exactly like a real worker failure would.
	const planID = uint64(4242)
	target := compactionTarget{sessionEpoch: a.sessionEpoch, turnID: a.turn.ID, turnEpoch: a.turn.Epoch}
	a.startCompactionState(planID, target, compactionTriggerModelDownshift, continuationPlan{
		kind:      compactionResumeMainLLM,
		turnID:    a.turn.ID,
		turnEpoch: a.turn.Epoch,
	})
	a.compactionState.downshiftSuspended = true

	// Simulated worker failure: handleCompactionFailed resumes the suspended
	// round with recheckGate=false, which selects the downshift bypass path.
	a.handleCompactionFailed(Event{
		Type:   EventCompactionFailed,
		TurnID: a.turn.ID,
		Payload: &compactionFailure{
			planID: planID,
			target: target,
			err:    errors.New("simulated compaction failure"),
		},
	})

	if a.IsCompactionRunning() {
		t.Fatal("compaction state must be settled after the failure")
	}
	if !a.mainLLMRequestInFlight.Load() {
		t.Fatal("resumed main LLM round must be in flight after the compaction failure")
	}

	// The respawned request fails on the primary again and pauses at the
	// fallback boundary. Act as the event loop: drain the queue and dispatch
	// until the boundary event arrives, asserting the one-shot bypass flag so
	// the smaller fallback window is not deferred again.
	var boundary *llmFallbackBoundaryPayload
	deadline := time.After(5 * time.Second)
	for boundary == nil {
		select {
		case evt := <-a.eventCh:
			if payload, ok := evt.Payload.(*llmFallbackBoundaryPayload); ok && payload != nil {
				boundary = payload
			} else {
				a.dispatch(evt)
			}
		case <-deadline:
			t.Fatal("timed out waiting for the respawned request to reach the fallback boundary")
		}
	}
	if !boundary.fallbackDownshiftBypass {
		t.Fatal("respawned request after a failed downshift compaction must carry the fallback bypass")
	}
	if boundary.fallbackModelRef != "fallback-prov/fallback-model" {
		t.Fatalf("fallback boundary model ref = %q, want the smaller-window fallback", boundary.fallbackModelRef)
	}
	a.handleLLMFallbackBoundary(Event{
		Type:    EventLLMFallbackBoundary,
		TurnID:  a.turn.ID,
		Payload: boundary,
	})
	if a.IsCompactionRunning() {
		t.Fatal("bypassed fallback boundary must not start a second compaction")
	}

	// The bypass lets the smaller-window fallback through: it receives exactly
	// one request with the pending conversation content.
	deadline = time.After(5 * time.Second)
	for {
		requests, _ := fallbackImpl.snapshot()
		if len(requests) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for the fallback provider request (requests=%d)", len(requests))
		case <-time.After(10 * time.Millisecond):
		}
	}
	requests, _ := fallbackImpl.snapshot()
	foundUserText := false
	for _, msg := range requests[0] {
		if msg.Role == message.RoleUser && strings.Contains(msg.Content, "continue the task") {
			foundUserText = true
			break
		}
	}
	if !foundUserText {
		t.Fatalf("fallback request did not include the pending conversation content: %#v", requests[0])
	}
	primaryRequests, _ := primaryImpl.snapshot()
	if len(primaryRequests) != 1 {
		t.Fatalf("primary provider requests = %d, want exactly one attempt before the fallback", len(primaryRequests))
	}

	// The fallback response completes the resumed round through the normal
	// LLM-response path.
	deadline = time.After(5 * time.Second)
responseLoop:
	for {
		select {
		case evt := <-a.eventCh:
			if evt.Type == EventLLMResponse {
				a.handleLLMResponse(evt)
				break responseLoop
			}
			a.dispatch(evt)
		case <-deadline:
			t.Fatal("timed out waiting for the fallback response event")
		}
	}
	snapshot := a.ctxMgr.Snapshot()
	last := snapshot[len(snapshot)-1]
	if last.Role != message.RoleAssistant || last.Content != "fallback response after compaction failure" {
		t.Fatalf("last context message = %#v, want the fallback response recorded", last)
	}
	if a.turn != nil {
		t.Fatal("expected the resumed round to finish and settle the turn")
	}
}
