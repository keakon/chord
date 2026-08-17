package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesTurnStateScopedByAccount(t *testing.T) {
	state := NewResponsesTurnState()
	state.capture("token-acc-a", "account:acc-a")
	if got := state.headerValue("account:acc-a"); got != "token-acc-a" {
		t.Fatalf("headerValue for minting account = %q, want token-acc-a", got)
	}
	if got := state.headerValue("account:acc-b"); got != "" {
		t.Fatalf("headerValue for other account = %q, want empty (no cross-account echo)", got)
	}
}

func TestResponsesTurnStateIdentityScopesStaticKeyByProvider(t *testing.T) {
	providerA := NewProviderConfig("provider-a", config.ProviderConfig{Type: config.ProviderTypeResponses}, []string{"shared-key"})
	providerB := NewProviderConfig("provider-b", config.ProviderConfig{Type: config.ProviderTypeResponses}, []string{"shared-key"})
	identityA := responsesTurnStateIdentity(providerA, "shared-key")
	identityB := responsesTurnStateIdentity(providerB, "shared-key")
	if identityA == identityB {
		t.Fatalf("turn-state identities match across providers: %q", identityA)
	}
	state := NewResponsesTurnState()
	state.capture("provider-a-state", identityA)
	if got := state.headerValue(identityB); got != "" {
		t.Fatalf("provider-b turn state = %q, want no cross-provider echo", got)
	}
}

func TestResponsesTurnStateContextRoundTrip(t *testing.T) {
	state := NewResponsesTurnState()
	ctx := WithResponsesTurnState(context.Background(), state)
	if got := ResponsesTurnStateFromContext(ctx); got != state {
		t.Fatal("ResponsesTurnStateFromContext did not return attached state")
	}
	if got := ResponsesTurnStateFromContext(WithResponsesTurnState(context.Background(), nil)); got != nil {
		t.Fatalf("nil state attach should be ignored, got %#v", got)
	}
	if got := ResponsesTurnStateFromContext(context.Background()); got != nil {
		t.Fatalf("no state should return nil, got %#v", got)
	}
}

func TestCaptureResponsesTurnStateFromHTTPHeader(t *testing.T) {
	state := NewResponsesTurnState()
	h := http.Header{}
	h.Set("X-Codex-Turn-State", "state-from-header")
	captureResponsesTurnState(state, h, "account:acc-a")
	if got := state.headerValue("account:acc-a"); got != "state-from-header" {
		t.Fatalf("captured header value = %q, want state-from-header", got)
	}
}

func TestCaptureResponsesTurnStateFromMetadataEvent(t *testing.T) {
	state := NewResponsesTurnState()
	data := []byte(`{"type":"response.metadata","headers":{"x-codex-turn-state":"state-from-event"}}`)
	captureResponsesTurnStateEvent(state, data, "account:acc-a")
	if got := state.headerValue("account:acc-a"); got != "state-from-event" {
		t.Fatalf("captured event value = %q, want state-from-event", got)
	}
}

func TestResponsesProvider_ReplaysAndCapturesTurnState(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		state := strings.TrimSpace(r.Header.Get(headerCodexTurnState))
		if requestCount == 1 {
			if state != "" {
				t.Fatalf("first request must not echo turn state, got %q", state)
			}
			w.Header().Set("X-Codex-Turn-State", "minted-turn-state")
		} else {
			if state != "minted-turn-state" {
				t.Fatalf("second request must replay minted turn state, got %q", state)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
	}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	state := NewResponsesTurnState()
	ctx := WithResponsesTurnState(context.Background(), state)
	for i := range 2 {
		if _, err := r.CompleteStream(
			ctx, "test-key", "sample/test-model", "", []message.Message{{Role: "user", Content: "hi"}},
			nil, 0, RequestTuning{}, func(message.StreamDelta) {},
		); err != nil {
			t.Fatalf("CompleteStream #%d: %v", i+1, err)
		}
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
}

func TestResponsesProvider_NoTurnStateEchoAcrossDifferentKey(t *testing.T) {
	var sawEcho bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get(headerCodexTurnState)) != "" {
			sawEcho = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeResponses,
		APIURL: server.URL + "/v1/responses",
	}, []string{"key-1", "key-2"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	state := NewResponsesTurnState()
	state.capture("state-for-key1", responsesTurnStateIdentity(providerCfg, "key-1"))
	ctx := WithResponsesTurnState(context.Background(), state)

	// A request on a different key must not replay state minted for key-1.
	if _, err := r.CompleteStream(
		ctx, "key-2", "sample/test-model", "", []message.Message{{Role: "user", Content: "hi"}},
		nil, 0, RequestTuning{}, func(message.StreamDelta) {},
	); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if sawEcho {
		t.Fatal("turn state minted for key-1 was echoed on a key-2 request")
	}
}
