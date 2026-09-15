package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestFallbackRequestIncludesQueuedUserInput(t *testing.T) {
	a := newReadyTestMainAgent(t)
	primaryCfg := llm.NewProviderConfig("primary", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-a": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"primary-key"})
	fallbackCfg := llm.NewProviderConfig("fallback", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model-b": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"fallback-key"})
	primaryImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			err:              &llm.APIError{StatusCode: 500, Message: "primary unavailable"},
			holdAfterStreams: true,
		}},
	}
	fallbackImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			resp:             &message.Response{Content: "fallback response"},
			holdAfterStreams: true,
		}},
	}
	client := llm.NewClient(primaryCfg, primaryImpl, "model-a", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "model-b",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})
	a.swapLLMClientWithRef(client, "model-a", 128000, "primary/model-a")

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(runCtx) }()

	a.SendUserMessage("initial request")
	select {
	case <-primaryImpl.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for primary request")
	}

	a.SendUserMessage("queued follow-up")
	close(primaryImpl.releaseCh)

	select {
	case <-fallbackImpl.streamedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fallback request")
	}

	primaryRequests, _ := primaryImpl.snapshot()
	if len(primaryRequests) != 1 {
		t.Fatalf("primary requests = %d, want 1", len(primaryRequests))
	}
	if requestHasUserText(primaryRequests[0], "queued follow-up") {
		t.Fatalf("primary request unexpectedly included queued input: %#v", primaryRequests[0])
	}
	fallbackRequests, _ := fallbackImpl.snapshot()
	if len(fallbackRequests) != 1 {
		t.Fatalf("fallback requests = %d, want 1", len(fallbackRequests))
	}
	if !requestHasUserText(fallbackRequests[0], "queued follow-up") {
		t.Fatalf("fallback request did not include queued input: %#v", fallbackRequests[0])
	}
	if !requestHasUserText(a.ctxMgr.Snapshot(), "queued follow-up") {
		t.Fatal("queued input was not committed to conversation context")
	}

	close(fallbackImpl.releaseCh)
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out stopping main agent")
	}
}

func requestHasUserText(messages []message.Message, want string) bool {
	for _, msg := range messages {
		if msg.Role == message.RoleUser && msg.Content == want {
			return true
		}
	}
	return false
}

func TestCallLLMOversizeRequestsEventLoopCompaction(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Reserved: 16000}}}
	a.newTurn()
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(400000, 272000, 16000, 0.8)

	providerCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 4096}},
		},
	}, []string{"primary-key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}}}}
	client := llm.NewClient(providerCfg, provider, "primary-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "primary-model", 400000, "primary-prov/primary-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("callLLM err = nil, want pending compaction error")
	}
	if !IsContextLengthExceededPendingCompaction(err) {
		t.Fatalf("err = %v, want pending compaction error", err)
	}
	if a.IsCompactionRunning() {
		t.Fatal("provider goroutine must not mutate compaction state")
	}
	a.handleCompactionOversizeSuspend(Event{
		Type:   EventCompactionOversizeSuspend,
		TurnID: a.turn.ID,
		Payload: &pendingMainLLMCall{
			turnID:            a.turn.ID,
			turnEpoch:         a.turn.Epoch,
			sessionEpoch:      a.sessionEpoch,
			continuation:      compactionResumeMainLLM,
			oversizeSuspended: true,
		},
	})
	if !a.IsCompactionRunning() || a.compactionState.trigger != compactionTriggerOversize {
		t.Fatalf("event loop did not start oversize compaction: %+v", a.compactionState)
	}
	if got := a.ctxMgr.GetUsableInputBudget(); got != 256000 {
		t.Fatalf("usable input budget = %d, want 256000", got)
	}
	deadline := time.After(2 * time.Second)
	foundInfo := false
	for {
		select {
		case evt := <-a.Events():
			switch e := evt.(type) {
			case InfoEvent:
				if strings.Contains(e.Message, "compacting context before retry") {
					foundInfo = true
				}
			case ToastEvent:
				if strings.Contains(e.Message, "Fallback chain exhausted") {
					t.Fatalf("unexpected fallback exhausted toast during oversize recovery: %+v", e)
				}
			}
			if foundInfo {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for oversize compaction info event")
		}
	}
}

func TestOversizeCompactionRequestHonorsRetryLimitOnEventLoop(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.newTurn()
	a.turn.OversizeRecoveryCount = maxOversizeRecoveryAttempts
	a.mainLLMRequestInFlight.Store(true)

	a.handleCompactionOversizeSuspend(Event{
		Type:   EventCompactionOversizeSuspend,
		TurnID: a.turn.ID,
		Payload: &pendingMainLLMCall{
			turnID:            a.turn.ID,
			turnEpoch:         a.turn.Epoch,
			sessionEpoch:      a.sessionEpoch,
			continuation:      compactionResumeMainLLM,
			oversizeSuspended: true,
		},
	})

	if a.IsCompactionRunning() {
		t.Fatal("retry-limited oversize request started compaction")
	}
	if a.turn != nil {
		t.Fatal("retry-limited oversize request did not settle the turn")
	}
	if a.mainLLMRequestInFlight.Load() {
		t.Fatal("retry-limited oversize request left request in flight")
	}
}

func TestCallLLMOversizeStopsWhenAutoCompactionDisabled(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.newTurn()
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(400000, 272000, 16000, 0)

	primaryCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 4096}},
		},
	}, []string{"primary-key"})
	fallbackCfg := llm.NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 4096}},
		},
	}, []string{"fallback-key"})
	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}}}}
	fallbackImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}}}}

	client := llm.NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   400000,
		InputLimit:     272000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 400000, "primary-prov/primary-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("callLLM err = nil, want context-length error")
	}
	msg := err.Error()
	for _, want := range []string{"automatic context compaction is not enabled", "/compact", "reduce the active context"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
	if a.IsCompactionRunning() {
		t.Fatal("did not expect compaction to start when automatic compaction is disabled")
	}
	for len(a.Events()) > 0 {
		if toast, ok := (<-a.Events()).(ToastEvent); ok && strings.Contains(toast.Message, "Fallback chain exhausted") {
			t.Fatalf("unexpected fallback exhausted toast: %+v", toast)
		}
	}
}

func TestCallLLMMixedFallbackErrorsDoNotStartOversizeCompaction(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{Context: config.ContextConfig{Compaction: config.CompactionConfig{Reserved: 16000}}}
	a.newTurn()
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(400000, 272000, 16000, 0.8)

	primaryCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 4096}},
		},
	}, []string{"primary-key"})
	fallbackCfg := llm.NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 400000, Input: 272000, Output: 4096}},
		},
	}, []string{"fallback-key"})
	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 402, Message: "quota exhausted"}}}}
	fallbackImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "input is too long"}}}}

	client := llm.NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   400000,
		InputLimit:     272000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 400000, "primary-prov/primary-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("callLLM err = nil, want mixed fallback error")
	}
	if IsContextLengthExceededPendingCompaction(err) {
		t.Fatalf("err = %v, did not expect pending compaction for mixed fallback errors", err)
	}
	if a.IsCompactionRunning() {
		t.Fatal("did not expect oversize compaction to start for mixed fallback errors")
	}
	foundToast := false
	for len(a.Events()) > 0 {
		if toast, ok := (<-a.Events()).(ToastEvent); ok && strings.Contains(toast.Message, "Fallback chain exhausted") {
			foundToast = true
		}
	}
	if !foundToast {
		t.Fatal("expected ordinary fallback exhausted toast for mixed fallback errors")
	}
}

func TestCallLLMShowsFallbackToastOnFirstThinkingToken(t *testing.T) {
	a := newReadyTestMainAgent(t)

	primaryCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"primary-key"})
	fallbackCfg := llm.NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"fallback-key"})

	primaryImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			err: &llm.APIError{StatusCode: 413, Message: "context too long"},
		}},
	}
	fallbackImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "thinking", Text: "Analyzing"},
				{Type: "thinking_end"},
			},
			resp: &message.Response{
				ThinkingBlocks: []message.ThinkingBlock{{Thinking: "Analyzing", Signature: "sig"}},
				StopReason:     "stop",
			},
			holdAfterStreams: true,
		}},
	}

	client := llm.NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetVariant("xhigh")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model@xhigh")

	done := make(chan error, 1)
	go func() {
		_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
		done <- err
	}()

	<-fallbackImpl.streamedCh
	toast := waitForToastEvent(t, a.Events(), "fallback model")
	if !strings.Contains(toast.Message, "Current model context exceeded") {
		t.Fatalf("toast = %+v, want explicit context-exceeded fallback message", toast)
	}
	if !strings.Contains(toast.Message, "fallback-prov/fallback-model") {
		t.Fatalf("toast = %+v, want fallback model ref", toast)
	}
	if strings.Contains(toast.Message, "fallback-prov/fallback-model@xhigh") {
		t.Fatalf("toast = %+v, should not leak selected variant to fallback model", toast)
	}

	close(fallbackImpl.releaseCh)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("callLLM: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callLLM to finish")
	}
}

func TestCallLLMDoesNotShowFallbackToastWhenFallbackNeverStreams(t *testing.T) {
	a := newReadyTestMainAgent(t)

	primaryCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"primary-key"})
	fallbackCfg := llm.NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"fallback-key"})

	primaryImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{
			{err: &llm.APIError{StatusCode: 502, Message: "bad gateway"}}, // triggers fallback
			{
				streams: []message.StreamDelta{
					{Type: "thinking", Text: "ok"},
					{Type: "thinking_end"},
				},
				resp: &message.Response{
					ThinkingBlocks: []message.ThinkingBlock{{Thinking: "ok", Signature: "sig"}},
					StopReason:     "stop",
				},
				holdAfterStreams: true,
			},
		},
	}
	fallbackImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			// No streams -> no key_confirmed -> fallback toast must NOT be shown.
			err: io.ErrUnexpectedEOF,
		}},
	}

	client := llm.NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	done := make(chan error, 1)
	go func() {
		_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
		done <- err
	}()

	<-primaryImpl.streamedCh

	// Drain agent events briefly and assert no fallback toast is emitted.
	deadline := time.After(250 * time.Millisecond)
	for {
		select {
		case evt := <-a.Events():
			if toast, ok := evt.(ToastEvent); ok && strings.Contains(toast.Message, "Switched to fallback model") {
				t.Fatalf("unexpected fallback toast: %+v", toast)
			}
		case <-deadline:
			close(primaryImpl.releaseCh)
			goto waitDone
		}
	}

waitDone:
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("callLLM: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callLLM to finish")
	}
}
func TestCallLLMNoFallbackToastForSameModelNameDifferentProvider(t *testing.T) {
	a := newReadyTestMainAgent(t)

	primaryCfg := llm.NewProviderConfig("prov-a", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"glm-5.1": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"key-a"})
	fallbackCfg := llm.NewProviderConfig("prov-b", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"glm-5.1": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"key-b"})

	primaryImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			err: &llm.APIError{StatusCode: 500, Message: "internal error"},
		}},
	}
	fallbackImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "text", Text: "Hello"},
			},
			resp: &message.Response{
				Content:    "Hello",
				StopReason: "stop",
			},
			holdAfterStreams: true,
		}},
	}

	client := llm.NewClient(primaryCfg, primaryImpl, "glm-5.1", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "glm-5.1",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	a.swapLLMClientWithRef(client, "glm-5.1", 128000, "prov-a/glm-5.1")

	done := make(chan error, 1)
	go func() {
		_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
		done <- err
	}()

	<-fallbackImpl.streamedCh

	// No "Switched to fallback model" toast should appear because the model
	// name is the same; only the provider changed.
	timeout := time.After(500 * time.Millisecond)
drainLoop:
	for {
		select {
		case evt := <-a.Events():
			if toast, ok := evt.(ToastEvent); ok {
				if strings.Contains(toast.Message, "Switched to fallback model") {
					t.Fatalf("unexpected fallback toast: %q", toast.Message)
				}
			}
		case <-timeout:
			break drainLoop
		}
	}

	close(fallbackImpl.releaseCh)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("callLLM: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callLLM to finish")
	}
}

// TestCallLLMDifferentModelNamesShowsFallbackToast verifies that switching
// to a genuinely different model name (e.g. "prov-a/glm-5.1" → "prov-b/gpt-5.5")
// still emits the "Switched to fallback model" toast as before.
func TestCallLLMDifferentModelNamesShowsFallbackToast(t *testing.T) {
	a := newReadyTestMainAgent(t)

	primaryCfg := llm.NewProviderConfig("prov-a", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"glm-5.1": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"key-a"})
	fallbackCfg := llm.NewProviderConfig("prov-b", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"gpt-5.5": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"key-b"})

	primaryImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			err: &llm.APIError{StatusCode: 500, Message: "internal error"},
		}},
	}
	fallbackImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "thinking", Text: "Thinking"},
				{Type: "thinking_end"},
			},
			resp: &message.Response{
				ThinkingBlocks: []message.ThinkingBlock{{Thinking: "Thinking", Signature: "sig"}},
				StopReason:     "stop",
			},
			holdAfterStreams: true,
		}},
	}

	client := llm.NewClient(primaryCfg, primaryImpl, "glm-5.1", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "gpt-5.5",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	a.swapLLMClientWithRef(client, "glm-5.1", 128000, "prov-a/glm-5.1")

	done := make(chan error, 1)
	go func() {
		_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
		done <- err
	}()

	<-fallbackImpl.streamedCh
	toast := waitForToastEvent(t, a.Events(), "Switched to fallback model")
	if !strings.Contains(toast.Message, "prov-b/gpt-5.5") {
		t.Fatalf("toast = %q, want fallback model ref prov-b/gpt-5.5", toast.Message)
	}

	close(fallbackImpl.releaseCh)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("callLLM: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callLLM to finish")
	}
}

// TestCallLLMNoFallbackExhaustedToastOnCancel verifies that a "Fallback chain
// exhausted" error toast is NOT emitted when the LLM call fails due to context
// cancellation (e.g. user pressing ESC to cancel), even if the CallStatus
// reports FallbackTriggered && FallbackExhausted from a previous round that
// completed before the cancel took effect.
func TestCallLLMNoFallbackExhaustedToastOnCancel(t *testing.T) {
	a := newReadyTestMainAgent(t)

	primaryCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"key-1"})
	fallbackCfg := llm.NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"fallback-key"})

	// Both primary and fallback fail with retriable errors, then on the
	// next round the context is cancelled during backoff.
	primaryImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{
			{err: &llm.APIError{StatusCode: 429, Message: "rate limited"}},
		},
	}
	fallbackImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{
			{err: &llm.APIError{StatusCode: 429, Message: "rate limited"}},
		},
	}

	client := llm.NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})

	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	// Use a cancellable context so we can cancel mid-retry-backoff.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := a.callLLM(ctx, []message.Message{{Role: "user", Content: "hi"}})
		done <- err
	}()

	// Wait briefly for the first round to complete (current pool head + next pool entry both
	// fail, FallbackExhausted is set), then cancel the context so the retry
	// backoff aborts.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from cancelled callLLM")
		}
		if !strings.Contains(err.Error(), "aborted") && !strings.Contains(err.Error(), "cancelled") &&
			!strings.Contains(err.Error(), "canceled") {
			t.Fatalf("expected cancel-related error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for callLLM to finish after cancel")
	}

	// Drain events and verify NO "Fallback chain exhausted" toast was emitted.
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case evt := <-a.Events():
			if toast, ok := evt.(ToastEvent); ok &&
				strings.Contains(toast.Message, "Fallback chain exhausted") {
				t.Fatalf("unexpected fallback exhausted toast: %+v", toast)
			}
		case <-timeout:
			// No more events — good, the toast was not emitted.
			return
		}
	}
}

func TestCallLLMSurfacesUpstreamStreamFailureActionably(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.SetProviderModelRef("primary-prov/primary-model")

	primaryCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"primary-key"})
	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{Origin: llm.APIErrorOriginSSEEvent, Code: "upstream_connection_error", Message: "Upstream response stream was interrupted"},
	}}}

	client := llm.NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetStreamRetryRounds(1)
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("callLLM err = nil, want actionable upstream outage error")
	}
	if !strings.Contains(err.Error(), "upstream-side outage") {
		t.Fatalf("error = %v, want actionable upstream outage message", err)
	}
	seen, _ := primaryImpl.snapshot()
	if got := len(seen); got != 1 {
		t.Fatalf("provider calls = %d, want 1 (upstream outage must not retry or probe)", got)
	}
}

func TestCallLLMFailedFallbackPersistsLastRunningModel(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.SetProviderModelRef("primary-prov/primary-model")

	primaryCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"primary-key"})
	fallbackCfg := llm.NewProviderConfig("fallback-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"fallback-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"fallback-key"})

	primaryImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 429, Message: "rate limited"},
	}}}
	fallbackImpl := &blockingStreamProvider{calls: []scriptedStreamCall{{
		err: &llm.APIError{StatusCode: 429, Message: "fallback rate limited"},
	}}}

	client := llm.NewClient(primaryCfg, primaryImpl, "primary-model", 4096, "sys")
	client.SetStreamRetryRounds(1)
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: fallbackCfg,
		ProviderImpl:   fallbackImpl,
		ModelID:        "fallback-model",
		MaxTokens:      4096,
		ContextLimit:   128000,
	}})
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("callLLM err = nil, want fallback failure")
	}
	if got := a.RunningModelRef(); got != "fallback-prov/fallback-model" {
		t.Fatalf("RunningModelRef() after failed fallback = %q, want fallback-prov/fallback-model", got)
	}

	var sawRunningModelChange bool
	var sawExhaustedToast bool
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case evt := <-a.Events():
			switch e := evt.(type) {
			case RunningModelChangedEvent:
				if e.ProviderModelRef == "primary-prov/primary-model" && e.RunningModelRef == "fallback-prov/fallback-model" {
					sawRunningModelChange = true
				}
			case ToastEvent:
				if strings.Contains(e.Message, "Fallback chain exhausted") {
					sawExhaustedToast = true
				}
			}
		case <-deadline:
			if !sawRunningModelChange {
				t.Fatal("missing RunningModelChangedEvent for failed fallback")
			}
			if !sawExhaustedToast {
				t.Fatal("missing fallback exhausted toast for failed fallback")
			}
			return
		}
	}
}

// TestFallbackBoundaryDefersReductionToTheCaller pins where the rebuild runs.
// handleLLMFallbackBoundary is an event-loop handler: it owns the pending user
// queue and the downshifted budgets, so the decision has to be taken there, but
// re-running request preparation there stalls the whole UI event loop for the
// length of a full reduction pass over the session. The handler reports the
// decision and the requesting goroutine does the work.
func TestFallbackBoundaryDefersReductionToTheCaller(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background()}
	a.ctxMgr = ctxmgr.NewManager(128000, 4096)
	a.newTurn()
	toolOutput := strings.Repeat("processed record without a recognizable shape\n", 400)
	messages := []message.Message{
		{Role: message.RoleUser, Content: "run it"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "c1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"make"}`)}}},
		{Role: message.RoleTool, ToolCallID: "c1", Content: toolOutput, ToolStatus: "success"},
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleUser, Content: "u2"},
		{Role: message.RoleUser, Content: "u3"},
		{Role: message.RoleUser, Content: "u4"},
	}
	payload := &llmFallbackBoundaryPayload{
		turnID:                  a.turn.ID,
		messages:                messages,
		primaryContextLimit:     128000,
		primaryInputLimit:       96000,
		primaryModelRef:         "provider/model-1",
		fallbackModelRef:        "provider/model-2",
		fallbackContextLimit:    64000,
		fallbackInputLimit:      48000,
		fallbackDownshiftBypass: true,
		reply:                   make(chan llmFallbackBoundaryResult, 1),
	}
	a.handleLLMFallbackBoundary(Event{Type: EventLLMFallbackBoundary, TurnID: payload.turnID, Payload: payload})

	result := <-payload.reply
	if result.err != nil {
		t.Fatalf("boundary returned an error: %v", result.err)
	}
	if !result.rebuild {
		t.Fatal("a narrower fallback budget must ask for a rebuild")
	}
	if len(result.messages) != len(messages) || result.messages[2].Content != toolOutput {
		t.Fatalf("the event-loop handler reduced the surface itself: %q",
			compactTextSnippet(result.messages[2].Content, 160))
	}
}

func TestFallbackRequiresFreshAdmission(t *testing.T) {
	tests := []struct {
		name string
		got  llmFallbackBoundaryPayload
		want bool
	}{
		{
			name: "smaller context",
			got: llmFallbackBoundaryPayload{
				primaryContextLimit:  128000,
				primaryInputLimit:    96000,
				fallbackContextLimit: 64000,
				fallbackInputLimit:   48000,
			},
			want: true,
		},
		{
			name: "smaller input",
			got: llmFallbackBoundaryPayload{
				primaryContextLimit:  128000,
				primaryInputLimit:    96000,
				fallbackContextLimit: 128000,
				fallbackInputLimit:   64000,
			},
			want: true,
		},
		{
			// Every fallback is by definition a different model, so a rebuild
			// keyed on that is a rebuild on every fallback. The token estimate
			// is model-agnostic and reduction is budget-driven: with the same
			// budget the rebuild reproduces the same bytes.
			name: "different model on the same budget",
			got: llmFallbackBoundaryPayload{
				primaryModelRef:      "provider/model-1",
				primaryContextLimit:  128000,
				primaryInputLimit:    96000,
				fallbackModelRef:     "provider/model-2",
				fallbackContextLimit: 128000,
				fallbackInputLimit:   96000,
			},
			want: false,
		},
		{
			// An unset fallback input limit means the whole window feeds the
			// prompt; it must be normalized rather than read as "no limit
			// information", which is how the downshift check reads it.
			name: "unset fallback input limit narrower than the primary input budget",
			got: llmFallbackBoundaryPayload{
				primaryContextLimit:  128000,
				primaryInputLimit:    96000,
				fallbackContextLimit: 128000,
			},
			want: false,
		},
		{
			name: "equivalent budgets",
			got: llmFallbackBoundaryPayload{
				primaryContextLimit:  128000,
				primaryInputLimit:    96000,
				fallbackContextLimit: 128000,
				fallbackInputLimit:   96000,
			},
			want: false,
		},
		{
			name: "unknown fallback budget",
			got: llmFallbackBoundaryPayload{
				primaryContextLimit: 128000,
				primaryInputLimit:   96000,
			},
			want: false,
		},
		{
			name: "unknown fallback budget with a different model",
			got: llmFallbackBoundaryPayload{
				primaryModelRef:     "provider/model-1",
				primaryContextLimit: 128000,
				primaryInputLimit:   96000,
				fallbackModelRef:    "provider/model-2",
			},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fallbackRequiresFreshAdmission(&test.got); got != test.want {
				t.Fatalf("fallbackRequiresFreshAdmission() = %v, want %v", got, test.want)
			}
		})
	}
}

func fallbackBoundaryMessages(t *testing.T, a *MainAgent, messages []message.Message, tailOverlayCount int) []message.Message {
	t.Helper()
	payload := &llmFallbackBoundaryPayload{
		turnID:               a.turn.ID,
		messages:             messages,
		tailOverlayCount:     tailOverlayCount,
		primaryModelRef:      "provider/model-1",
		primaryContextLimit:  128000,
		primaryInputLimit:    96000,
		fallbackModelRef:     "provider/model-2",
		fallbackContextLimit: 128000,
		fallbackInputLimit:   96000,
		reply:                make(chan llmFallbackBoundaryResult, 1),
	}
	a.handleLLMFallbackBoundary(Event{Type: EventLLMFallbackBoundary, TurnID: payload.turnID, Payload: payload})
	result := <-payload.reply
	if result.err != nil {
		t.Fatalf("boundary err = %v", result.err)
	}
	if result.rebuild {
		t.Fatal("rebuild = true, want equivalent budgets without a rebuild")
	}
	return result.messages
}

func requestHasBackgroundResult(messages []message.Message, want string) bool {
	for _, msg := range messages {
		if msg.Kind == message.KindBackgroundResult && strings.Contains(msg.Content, want) {
			return true
		}
	}
	return false
}

// TestFallbackBoundaryIncludesQueuedMailboxResult pins the mailbox/retry parity:
// a JOB RESULT waiting in the mailbox inbox when a fallback retry dispatches
// must ride that retry, exactly like a queued user message does.
func TestFallbackBoundaryIncludesQueuedMailboxResult(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "hi"})

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, "job-1", "Run production build")})
	if got := len(a.ctxMgr.Snapshot()); got != 1 {
		t.Fatalf("ctx snapshot = %d messages, want only the initial user message before the retry", got)
	}

	messages := []message.Message{{Role: message.RoleUser, Content: "hi"}}
	got := fallbackBoundaryMessages(t, a, messages, 0)
	if !requestHasBackgroundResult(got, "Run production build") {
		t.Fatalf("fallback messages = %#v, want the queued JOB RESULT", got)
	}
	if !requestHasBackgroundResult(a.ctxMgr.Snapshot(), "Run production build") {
		t.Fatal("queued JOB RESULT was not committed to conversation context")
	}
	if len(a.pendingSubAgentMailboxes) != 0 {
		t.Fatalf("pendingSubAgentMailboxes = %d, want the staged batch consumed by the retry", len(a.pendingSubAgentMailboxes))
	}

	// A second boundary with no new arrivals must not duplicate the result.
	again := fallbackBoundaryMessages(t, a, got, 0)
	count := 0
	for _, msg := range again {
		if msg.Kind == message.KindBackgroundResult && strings.Contains(msg.Content, "Run production build") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("background result copies = %d, want exactly 1 after a retry with no new arrivals", count)
	}
}

// TestFallbackBoundaryMergesUserInputBeforeMailboxResult pins the durable order
// for a retry that picks up both inputs at once: the queued user follow-up
// comes first, then the JOB RESULT, matching the turn-continuation order.
func TestFallbackBoundaryMergesUserInputBeforeMailboxResult(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "hi"})

	a.handleJobFinished(Event{Type: EventJobFinished, SourceID: a.instanceID, Payload: backgroundResultPayload(a.instanceID, "job-2", "Upload release bundle")})
	a.handleUserMessage(Event{Payload: "queued user follow-up"})

	messages := []message.Message{
		{Role: message.RoleUser, Content: "hi"},
		{Role: message.RoleUser, Content: "transient tail", Kind: message.KindTurnOverlay},
	}
	got := fallbackBoundaryMessages(t, a, messages, 1)
	userIdx := -1
	jobIdx := -1
	for i, msg := range got {
		if msg.Role == message.RoleUser && msg.Content == "queued user follow-up" {
			userIdx = i
		}
		if msg.Kind == message.KindBackgroundResult && strings.Contains(msg.Content, "Upload release bundle") {
			jobIdx = i
		}
	}
	if userIdx < 0 {
		t.Fatalf("fallback messages = %#v, want the queued user follow-up", got)
	}
	if jobIdx < 0 {
		t.Fatalf("fallback messages = %#v, want the queued JOB RESULT", got)
	}
	if userIdx > jobIdx {
		t.Fatalf("user idx = %d, job idx = %d, want user input before the JOB RESULT", userIdx, jobIdx)
	}
	if last := got[len(got)-1]; last.Kind != message.KindTurnOverlay {
		t.Fatalf("last message = %#v, want the transient tail to stay last", last)
	}
	snapshot := a.ctxMgr.Snapshot()
	snapUserIdx := -1
	snapJobIdx := -1
	for i, msg := range snapshot {
		if msg.Role == message.RoleUser && msg.Content == "queued user follow-up" {
			snapUserIdx = i
		}
		if msg.Kind == message.KindBackgroundResult && strings.Contains(msg.Content, "Upload release bundle") {
			snapJobIdx = i
		}
	}
	if snapUserIdx < 0 || snapJobIdx < 0 || snapUserIdx > snapJobIdx {
		t.Fatalf("ctx snapshot = %#v, want user input before the JOB RESULT", snapshot)
	}
}
