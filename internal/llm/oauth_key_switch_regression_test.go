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

func TestResponsesProvider_OpenAIOAuthHTTPSendsConfiguredStoreTrue(t *testing.T) {
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

	if store, ok := gotBody["store"].(bool); !ok || !store {
		t.Fatalf("expected store=true from explicit config, got %#v", gotBody["store"])
	}
}

// TestClient_SetSessionIDIsPerClientIsolated verifies the cache-isolation
// contract: session identity lives on the Client (threaded per-request through
// RequestTuning.SessionKey), never on the shared provider impl. Two clients
// that resolve to the same provider impl must be able to set different session
// keys without one clobbering the other.
func TestClient_SetSessionIDIsPerClientIsolated(t *testing.T) {
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		Preset: config.ProviderPresetCodex,
		APIURL: "https://example.com/v1/responses",
	}, []string{"k1", "k2"})
	impl, err := NewResponsesProvider(providerCfg, "")
	if err != nil {
		t.Fatalf("NewResponsesProvider: %v", err)
	}
	// Both clients share the same provider impl (providerCache.getOrCreateImpl
	// keys impls by provider name), exactly like MainAgent and a SubAgent.
	mainClient := &Client{provider: providerCfg, providerImpl: impl, modelID: "gpt-5.5"}
	subClient := &Client{provider: providerCfg, providerImpl: impl, modelID: "gpt-5.5"}

	mainClient.SetSessionID("session-20260903")
	subClient.SetSessionID("session-20260903:sub:builder-3")

	if mainClient.sessionKey != "session-20260903" {
		t.Fatalf("main client sessionKey = %q, want session-20260903", mainClient.sessionKey)
	}
	if subClient.sessionKey != "session-20260903:sub:builder-3" {
		t.Fatalf("sub client sessionKey = %q, want session-20260903:sub:builder-3", subClient.sessionKey)
	}
	// The second SetSessionID must not have overwritten the first: identity is
	// per-Client, so MainAgent and SubAgent caches stay distinguishable even
	// while sharing one ResponsesProvider.
	if mainClient.sessionKey != "session-20260903" {
		t.Fatalf("main client sessionKey changed after sub client set its own: %q", mainClient.sessionKey)
	}
}

// TestResponsesRequestSignatureIncludesPromptCacheKey verifies that the Codex
// WebSocket incremental-chain signature carries the session key. When a
// MainAgent and a SubAgent alternate requests over a shared WebSocket, their
// differing prompt_cache_key values force a full-input request instead of
// reusing the other agent's previous_response_id chain.
func TestResponsesRequestSignatureIncludesPromptCacheKey(t *testing.T) {
	base := responsesRequest{
		Model:  "gpt-5.5",
		Input:  []responsesInputItem{{Type: "message", Role: "user", Content: "hello"}},
		Stream: true,
	}
	mainReq := base
	mainReq.PromptCacheKey = "session-20260903"
	subReq := base
	subReq.PromptCacheKey = "session-20260903:sub:builder-3"

	mainSig := responsesRequestSignature(&mainReq)
	subSig := responsesRequestSignature(&subReq)
	if mainSig == "" || mainSig == subSig {
		t.Fatalf("signatures must differ by prompt_cache_key: main=%q sub=%q", mainSig, subSig)
	}
	// Same session key on identical requests must produce the same signature so
	// an agent's own incremental chain still reuses previous_response_id.
	again := base
	again.PromptCacheKey = "session-20260903"
	if got := responsesRequestSignature(&again); got != mainSig {
		t.Fatalf("signature changed for identical session key: got=%q want=%q", got, mainSig)
	}
}
