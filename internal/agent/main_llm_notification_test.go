package agent

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

type scriptedStreamCall struct {
	resp             *message.Response
	err              error
	streams          []message.StreamDelta
	holdAfterStreams bool
}

type blockingStreamProvider struct {
	mu         sync.Mutex
	calls      []scriptedStreamCall
	streamedCh chan struct{}
	releaseCh  chan struct{}
}

func (p *blockingStreamProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	cb llm.StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	if len(p.calls) == 0 {
		p.mu.Unlock()
		return nil, io.ErrUnexpectedEOF
	}
	next := p.calls[0]
	p.calls = p.calls[1:]
	p.mu.Unlock()

	if cb != nil {
		for _, delta := range next.streams {
			cb(delta)
		}
	}
	if next.holdAfterStreams && p.streamedCh != nil {
		close(p.streamedCh)
		<-p.releaseCh
	}
	if next.err != nil {
		return nil, next.err
	}
	if next.resp != nil {
		return next.resp, nil
	}
	return &message.Response{}, nil
}

func (p *blockingStreamProvider) Complete(
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

func newReadyTestMainAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	return a
}

func waitForToastEvent(t *testing.T, ch <-chan AgentEvent, want string) ToastEvent {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case evt := <-ch:
			toast, ok := evt.(ToastEvent)
			if !ok {
				continue
			}
			if strings.Contains(toast.Message, want) {
				return toast
			}
		case <-timeout:
			t.Fatalf("timed out waiting for toast containing %q", want)
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
	toast := waitForToastEvent(t, a.Events(), "Switched to fallback model")
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

func TestCallLLMShowsKeySwitchToastOnFirstToolCallToken(t *testing.T) {
	a := newReadyTestMainAgent(t)

	providerCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"key-1", "key-2"})

	providerImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{
			{err: io.ErrUnexpectedEOF},
			{
				streams: []message.StreamDelta{{
					Type: "tool_use_start",
					ToolCall: &message.ToolCallDelta{
						ID:    "call-1",
						Name:  "Read",
						Input: `{"path":"README.md"}`,
					},
				}},
				resp: &message.Response{
					ToolCalls: []message.ToolCall{{
						ID:   "call-1",
						Name: "Read",
						Args: []byte(`{"path":"README.md"}`),
					}},
					StopReason: "tool_use",
				},
				holdAfterStreams: true,
			},
		},
	}

	client := llm.NewClient(providerCfg, providerImpl, "primary-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	done := make(chan error, 1)
	go func() {
		_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
		done <- err
	}()

	<-providerImpl.streamedCh
	toast := waitForToastEvent(t, a.Events(), "Switched key")
	if toast.Level != "info" {
		t.Fatalf("toast.Level = %q, want info", toast.Level)
	}

	close(providerImpl.releaseCh)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("callLLM: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callLLM to finish")
	}
}

func TestCallLLMPromotesStreamingActivityOnToolUseStartWithoutStatusDelta(t *testing.T) {
	a := newReadyTestMainAgent(t)

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})

	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{{
				Type: "tool_use_start",
				ToolCall: &message.ToolCallDelta{
					ID:    "call-1",
					Name:  "Read",
					Input: `{"path":"README.md"}`,
				},
			}},
			resp: &message.Response{
				ToolCalls: []message.ToolCall{{
					ID:   "call-1",
					Name: "Read",
					Args: []byte(`{"path":"README.md"}`),
				}},
				StopReason: "tool_use",
			},
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}

	events := drainAgentEvents(a.Events())
	var sawStreaming bool
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if !ok {
			continue
		}
		if act.Type == ActivityStreaming {
			sawStreaming = true
		}
	}
	if !sawStreaming {
		t.Fatal("expected streaming activity promoted from tool_use_start")
	}
}

func TestCallLLMEmitsToolArgCompletionUpdateOnToolUseEnd(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})

	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "tool_use_start", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read", Input: `{"path":"READ`}},
				{Type: "tool_use_delta", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read", Input: `ME.md"}`}},
				{Type: "tool_use_end", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read"}},
			},
			resp: &message.Response{
				ToolCalls: []message.ToolCall{{
					ID:   "call-1",
					Name: "Read",
					Args: []byte(`{"path":"README.md"}`),
				}},
				StopReason: "tool_use",
			},
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}

	events := drainAgentEvents(a.Events())
	var sawDone bool
	for _, raw := range events {
		update, ok := raw.(ToolCallUpdateEvent)
		if !ok || update.ID != "call-1" {
			continue
		}
		if update.ArgsStreamingDone {
			sawDone = true
			if update.ArgsJSON != `{"path":"README.md"}` {
				t.Fatalf("done ArgsJSON = %q, want final accumulated args", update.ArgsJSON)
			}
		}
	}
	if !sawDone {
		t.Fatal("expected tool arg completion update on tool_use_end")
	}
}

func TestCallLLMOnlyEmitsOneStreamingActivityWhenStatusAlsoArrives(t *testing.T) {
	a := newReadyTestMainAgent(t)

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})

	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "status", Status: &message.StatusDelta{Type: "waiting_token"}},
				{Type: "tool_use_start", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read", Input: `{"path":"README.md"}`}},
				{Type: "status", Status: &message.StatusDelta{Type: "streaming"}},
			},
			resp: &message.Response{
				ToolCalls: []message.ToolCall{{
					ID:   "call-1",
					Name: "Read",
					Args: []byte(`{"path":"README.md"}`),
				}},
				StopReason: "tool_use",
			},
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}

	events := drainAgentEvents(a.Events())
	streamingCount := 0
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if ok && act.Type == ActivityStreaming {
			streamingCount++
		}
	}
	if streamingCount != 1 {
		t.Fatalf("streaming activity count = %d, want 1", streamingCount)
	}
}

func TestModelNameFromRef(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"meowoo/glm-5.1", "glm-5.1"},
		{"qt/gpt-5.5", "gpt-5.5"},
		{"a/b/c", "c"},          // nested: last segment wins
		{"glm-5.1", "glm-5.1"},  // bare name
		{"/glm-5.1", "glm-5.1"}, // leading slash
		{"", ""},                // empty
	}
	for _, tt := range tests {
		got := modelNameFromRef(tt.input)
		if got != tt.want {
			t.Errorf("modelNameFromRef(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestCallLLMNoFallbackToastForSameModelNameDifferentProvider verifies that
// switching from one provider to another for the same model name (e.g.
// "prov-a/glm-5.1" → "prov-b/glm-5.1") does NOT emit a "Switched to fallback
// model" toast. The user sees this as a key switch, not a model switch.
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
