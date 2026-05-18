package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestCallLLMOversizeStartsCompactionWhenNotRunning(t *testing.T) {
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
	if !a.IsCompactionRunning() {
		t.Fatal("expected oversize-driven compaction to be running")
	}
	if !a.compactionState.trigger.OversizeDriven {
		t.Fatal("expected oversize-driven trigger")
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
