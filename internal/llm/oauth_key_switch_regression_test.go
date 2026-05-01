package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesProvider_OpenAIOAuthHTTPIgnoresConfiguredStoreTrue(t *testing.T) {
	trueVal := true
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, accessToken := newOpenAITestOAuthProvider(t, server.URL+"/v1/responses")
	provider.store = &trueVal
	r := &ResponsesProvider{provider: provider, client: server.Client()}

	_, err := r.CompleteStream(
		context.Background(),
		accessToken,
		"gpt-5.5",
		"system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	if store, ok := gotBody["store"]; ok {
		if b, ok := store.(bool); !ok || b {
			t.Fatalf("expected store to be omitted or false, got %#v", store)
		}
	}
}

func TestResponsesProvider_SetSessionIDClearsCodexChainState(t *testing.T) {
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Preset: config.ProviderPresetCodex,
		APIURL: "https://example.com/v1/responses",
	}, []string{"k1", "k2"})
	o, err := NewOpenAIProvider(providerCfg, "")
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	o.responsesProvider.codexWSLastKey = "k1"
	o.responsesProvider.codexWSLastModel = "gpt-5.5"
	o.responsesProvider.codexWSLastRespID = "resp-123"
	o.responsesProvider.codexWSLastInpLen = 2
	o.responsesProvider.codexWSLastInpSig = "sig-123"
	o.responsesProvider.codexWSPromptCacheKey = "prompt-123"
	o.responsesProvider.sessionID = "old-session"

	client := &Client{provider: providerCfg, providerImpl: o}
	client.SetSessionID("new-session")

	if o.responsesProvider.codexWSLastRespID != "" {
		t.Fatalf("expected codexWSLastRespID reset, got %q", o.responsesProvider.codexWSLastRespID)
	}
	if o.responsesProvider.codexWSLastKey != "" {
		t.Fatalf("expected codexWSLastKey reset, got %q", o.responsesProvider.codexWSLastKey)
	}
	if o.responsesProvider.codexWSPromptCacheKey != "" {
		t.Fatalf("expected prompt cache key reset, got %q", o.responsesProvider.codexWSPromptCacheKey)
	}
	if o.responsesProvider.sessionID != "new-session" {
		t.Fatalf("expected sessionID updated, got %q", o.responsesProvider.sessionID)
	}
}
