package agent

import (
	"context"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestFallbackDiagnosticsUsesDispatchedWindow(t *testing.T) {
	a := newReadyTestMainAgent(t)
	makeConfig := func(name string, limit int) *llm.ProviderConfig {
		return llm.NewProviderConfig(name, config.ProviderConfig{
			Type:   config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: limit, Output: 4096}}},
		}, []string{"sample-key"})
	}
	primary := &blockingStreamProvider{calls: []scriptedStreamCall{{err: &llm.APIError{StatusCode: 500, Message: "unavailable"}}}}
	fallback := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{
		Content: "sample", Usage: &message.TokenUsage{InputTokens: 10},
	}}}}
	client := llm.NewClient(makeConfig("primary", 128000), primary, "test-model", 4096, "system")
	client.SetFallbackModels([]llm.FallbackModel{{
		ProviderConfig: makeConfig("fallback", 64000), ProviderImpl: fallback,
		ModelID: "test-model", MaxTokens: 4096, ContextLimit: 64000,
	}})
	a.swapLLMClientWithRef(client, "test-model", 128000, "primary/test-model")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("agent did not stop")
		}
	})
	a.SendUserMessage("sample")
	waitForAgentState(t, "fallback diagnostics", func() bool {
		v := a.FocusedContextDiagnostics()
		return v.Available && v.Snapshot.State == contextdiag.Completed && v.WindowLimit == 64000-4096
	})
	v := a.FocusedContextDiagnostics()
	if v.Snapshot.ModelRef != "fallback/test-model" || v.Snapshot.WindowLimit != int64(v.WindowLimit) {
		t.Fatalf("fallback snapshot=%+v; live window=%d", v.Snapshot, v.WindowLimit)
	}
}
