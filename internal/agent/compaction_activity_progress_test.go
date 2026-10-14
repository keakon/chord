package agent

import (
	"context"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestCompactionProgressReporterUsesTransportProgressAndAccumulatesAttempts(t *testing.T) {
	reporter := newCompactionProgressReporter(nil)
	if reporter.update(message.StreamDelta{Type: message.StreamDeltaText, Text: "abc"}) {
		t.Fatal("content delta should not count as transport progress")
	}
	if reporter.bytes != 0 || reporter.events != 0 {
		t.Fatalf("content progress = %d/%d, want 0/0", reporter.bytes, reporter.events)
	}

	if !reporter.update(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: 12, Events: 2}}) {
		t.Fatal("transport progress should update the reporter")
	}
	if reporter.bytes != 12 || reporter.events != 2 {
		t.Fatalf("transport progress = %d/%d, want 12/2", reporter.bytes, reporter.events)
	}
	if reporter.update(message.StreamDelta{Type: message.StreamDeltaText, Text: "must not double count"}) {
		t.Fatal("content delta should be ignored after transport progress starts")
	}

	reporter.update(message.StreamDelta{
		Type:   message.StreamDeltaStatus,
		Status: &message.StatusDelta{Type: "retrying_key"},
	})
	if !reporter.update(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: 4, Events: 1}}) {
		t.Fatal("a restarted attempt should update accumulated progress")
	}
	if reporter.bytes != 16 || reporter.events != 3 {
		t.Fatalf("accumulated retry progress = %d/%d, want 16/3", reporter.bytes, reporter.events)
	}
}

func TestCompactionProgressReporterForwardsRetryAndKeyDeltas(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	reporter := newCompactionProgressReporter(a)

	retryErr := &llm.APIError{StatusCode: 429, Message: "rate limited"}
	if reporter.update(message.StreamDelta{Type: message.StreamDeltaRetryError, Err: retryErr, Provider: "sample", Model: "test-model", MaskedKey: "k1"}) {
		t.Fatal("a retry error is not transport progress")
	}
	if reporter.update(message.StreamDelta{Type: message.StreamDeltaKeyExpired, Email: "user@example.com", AccountID: "acc-1"}) {
		t.Fatal("a key-state delta is not transport progress")
	}

	sawError := false
	sawToast := false
	for _, event := range drainAgentEvents(a.outputCh) {
		switch e := event.(type) {
		case ErrorEvent:
			if e.Err != retryErr || e.Provider != "sample" || e.Model != "test-model" || e.Key != "k1" || !e.Silent {
				t.Fatalf("forwarded error event = %+v, want the retry error metadata", e)
			}
			sawError = true
		case ToastEvent:
			if e.Category == "oauth_account_expired" {
				sawToast = true
			}
		}
	}
	if !sawError {
		t.Fatal("compaction callback dropped the retry error instead of forwarding it")
	}
	if !sawToast {
		t.Fatal("compaction callback dropped the permanent invalidation overlay")
	}
}

func TestCompactionSummaryWaitsForLLMGovernor(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.governor = newResourceGovernor(config.OrchestrationConfig{MaxActiveLLMRequests: 1})
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	// A non-empty response keeps the client's empty-response retry loop out of
	// the way: this test pins governor waiting, not retry behavior.
	client := llm.NewClient(providerCfg, stubProvider{response: &message.Response{Content: "summary", StopReason: "stop"}}, "test-model", 1024, "")

	release, err := a.governor.acquireLLM(context.Background(), client.PrimaryModelRef())
	if err != nil {
		t.Fatalf("acquire test LLM slot: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		_, _, err := a.callCompactionSummary(context.Background(), client, "sample/test-model", "prompt", nil)
		result <- err
	}()

	select {
	case err := <-result:
		release()
		t.Fatalf("compaction summary bypassed the LLM governor: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()

	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("compaction summary did not proceed after the LLM slot was released")
	}
}

func TestCompactionProgressReporterUsesRequestProgressThrottle(t *testing.T) {
	reporter := newCompactionProgressReporter(nil)
	now := time.Unix(100, 0)

	reporter.update(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: 10, Events: 1}})
	if !reporter.emitIfDue(now) {
		t.Fatal("first progress update should emit")
	}
	reporter.update(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: 20, Events: 2}})
	if reporter.emitIfDue(now.Add(50 * time.Millisecond)) {
		t.Fatal("progress before the shared minimum interval should not emit")
	}
	if !reporter.emitIfDue(now.Add(requestProgressEmitMinInterval)) {
		t.Fatal("progress at the shared minimum interval should emit")
	}
}

func TestCompactionKeepAliveEmitsActivityAndStops(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.beginCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle}, 0, nil)
	defer a.resetCompactionState()

	k := newCompactionKeepAlive(a)
	defer k.Stop()

	// A single beat must surface as a compacting activity event so external
	// control planes observing activity envelopes keep refreshing during a
	// silent compaction request.
	k.tick()
	found := false
	for _, event := range drainAgentEvents(a.outputCh) {
		activity, ok := event.(AgentActivityEvent)
		if !ok {
			continue
		}
		if activity.AgentID != "main" || activity.Type != ActivityCompacting || activity.Detail != "context" {
			t.Fatalf("keep-alive activity = %+v, want main/compacting/context", activity)
		}
		found = true
		break
	}
	if !found {
		t.Fatal("keep-alive tick emitted no compacting activity event")
	}

	// Stop must be idempotent and release the run goroutine.
	k.Stop()
	select {
	case <-k.done:
	default:
		t.Fatal("keep-alive run goroutine still running after Stop")
	}
	k.Stop()
}

func TestCompactionKeepAliveYieldsToForegroundActivity(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.beginCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle}, 0, nil)
	defer a.resetCompactionState()

	k := newCompactionKeepAlive(a)
	defer k.Stop()

	// A live foreground request owns the shared main activity slot: the
	// heartbeat must not clobber it mid-stream.
	a.emitActivity("main", ActivityStreaming, "")
	drainAgentEvents(a.outputCh)
	k.tick()
	for _, event := range drainAgentEvents(a.outputCh) {
		if activity, ok := event.(AgentActivityEvent); ok && activity.Type == ActivityCompacting {
			t.Fatalf("keep-alive clobbered foreground activity: %+v", activity)
		}
	}

	// Once the main LLM request settles, the slot is free again and the next
	// heartbeat surfaces compacting.
	a.mainSlotForeground.Store(false)
	k.tick()
	found := false
	for _, event := range drainAgentEvents(a.outputCh) {
		if activity, ok := event.(AgentActivityEvent); ok && activity.Type == ActivityCompacting {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("keep-alive tick after request settle emitted no compacting activity event")
	}
}

func TestIdleHandsMainSlotBackToRunningCompaction(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.beginCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle}, 0, nil)
	defer a.resetCompactionState()

	// Foreground work ends while compaction is still running: the idle must be
	// immediately followed by a compacting handback instead of flashing idle.
	a.emitActivity("main", ActivityExecuting, "1 tools")
	a.emitActivity("main", ActivityIdle, "")
	var sawIdle, sawHandback bool
	for _, event := range drainAgentEvents(a.outputCh) {
		activity, ok := event.(AgentActivityEvent)
		if !ok {
			continue
		}
		if activity.Type == ActivityIdle {
			sawIdle = true
		}
		if sawIdle && activity.Type == ActivityCompacting {
			sawHandback = true
		}
	}
	if !sawIdle || !sawHandback {
		t.Fatalf("idle handback missing: sawIdle=%v sawHandback=%v", sawIdle, sawHandback)
	}

	// After compaction completes (running state cleared), idle stays idle.
	a.emitActivity("main", ActivityExecuting, "1 tools")
	a.resetCompactionState()
	a.emitActivity("main", ActivityIdle, "")
	for _, event := range drainAgentEvents(a.outputCh) {
		if activity, ok := event.(AgentActivityEvent); ok && activity.Type == ActivityCompacting {
			t.Fatalf("terminal idle must not hand the slot back to compaction: %+v", activity)
		}
	}
}

func TestCompactionHeartbeatWaitsForResponseHandler(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.beginCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerManual, continuationPlan{kind: compactionResumeIdle}, 0, nil)
	defer a.resetCompactionState()
	a.newTurn("")
	a.mainLLMRequestInFlight.Store(true)
	a.emitActivity("main", ActivityStreaming, "")
	drainAgentEvents(a.outputCh)
	k := newCompactionKeepAlive(a)
	defer k.Stop()

	// Returning from the provider no longer releases the foreground slot. Until
	// the event loop handles the response, a compaction heartbeat must stay out
	// of the shared activity lane.
	k.tick()
	for _, event := range drainAgentEvents(a.outputCh) {
		if activity, ok := event.(AgentActivityEvent); ok && activity.Type == ActivityCompacting {
			t.Fatalf("heartbeat claimed slot before response handler: %+v", activity)
		}
	}

	a.handleLLMResponse(Event{
		Type:   EventLLMResponse,
		TurnID: a.turn.ID,
		Payload: &LLMResponsePayload{
			Content:    "done",
			StopReason: "stop",
		},
	})

	found := false
	for _, event := range drainAgentEvents(a.outputCh) {
		if activity, ok := event.(AgentActivityEvent); ok && activity.Type == ActivityCompacting {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("response handler did not hand the slot back to active compaction")
	}
}

func TestCompactionProgressReporterInfersAttemptFromTransportReset(t *testing.T) {
	reporter := newCompactionProgressReporter(nil)
	reporter.update(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: 12, Events: 2}})
	reporter.update(message.StreamDelta{Progress: &message.StreamProgressDelta{Bytes: 4, Events: 1}})

	if reporter.bytes != 16 || reporter.events != 3 {
		t.Fatalf("inferred retry progress = %d/%d, want 16/3", reporter.bytes, reporter.events)
	}
}

func TestGenericCompactionEmitsTransportProgress(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"compact-model": {Limit: config.ModelLimit{Context: 16384, Output: 2048}},
		},
	}, []string{"test-key"})
	provider := &countingCompactionProvider{
		progress: []message.StreamProgressDelta{{Bytes: 128, Events: 2}, {Bytes: 256, Events: 4}},
		response: &message.Response{Content: validCompactionSummaryForTest("history-1.md")},
	}
	client := llm.NewClient(providerCfg, provider, "compact-model", 2048, "")

	if _, _, err := a.callCompactionSummary(t.Context(), client, "sample/compact-model", "compact this history", newCompactionProgressReporter(a)); err != nil {
		t.Fatalf("callCompactionSummary() error = %v", err)
	}

	var progress []CompactionStatusEvent
	for _, event := range drainAgentEvents(a.outputCh) {
		if event, ok := event.(CompactionStatusEvent); ok && event.Status == "progress" {
			progress = append(progress, event)
		}
	}
	if len(progress) < 2 {
		t.Fatalf("generic compaction progress events = %#v, want at least two updates", progress)
	}
	last := progress[len(progress)-1]
	if last.Bytes != 256 || last.Events != 4 {
		t.Fatalf("last generic compaction progress = %#v, want bytes=256 events=4", last)
	}
}
