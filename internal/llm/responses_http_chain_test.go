package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesProvider_WebsocketMissingResponseIDDoesNotAdvanceChain(t *testing.T) {
	r := &ResponsesProvider{}
	r.codexWSLastRespID = "resp-old"
	r.codexWSLastInpLen = 2
	r.codexWSLastInpSig = "sig-old"

	resp := &message.Response{}
	fullInput := []responsesInputItem{{Type: "message", Role: "user", Content: "hello"}}

	r.codexWSLastInpLen = len(fullInput)
	r.codexWSLastInpSig = responsesInputSignature(fullInput)
	if resp.ProviderResponseID != "" {
		r.codexWSLastRespID = resp.ProviderResponseID
	} else {
		r.codexWSLastRespID = ""
	}

	if r.codexWSLastRespID != "" {
		t.Fatalf("last resp id = %q, want empty when response id missing", r.codexWSLastRespID)
	}
}

func TestResponsesProvider_NoPreviousIDOnFirstRound(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL + "/v1/responses",
		Store:  &storeTrue,
	}, []string{"test-key"})

	r := &ResponsesProvider{
		provider: providerCfg,
		client:   server.Client(),
	}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if _, ok := gotBody["previous_response_id"]; ok {
		t.Error("first request must not include previous_response_id")
	}
}

func TestResponsesProvider_HTTPSendsFullInputOnSecondRound(t *testing.T) {
	var requestBodies []map[string]any
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requestBodies = append(requestBodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-2","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL + "/v1/responses",
		Store:  &storeTrue,
	}, []string{"test-key"})

	r := &ResponsesProvider{
		provider: providerCfg,
		client:   server.Client(),
	}

	// Second round: full input must always be sent on HTTP path.
	msgs2 := []message.Message{
		{Role: "user", Content: "hello"},
		{Role: "user", Content: "follow up"},
	}
	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5", "",
		msgs2, nil, 128, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	mu.Lock()
	body := requestBodies[0]
	mu.Unlock()
	if _, ok := body["previous_response_id"]; ok {
		t.Errorf("second round: unexpected previous_response_id = %v", body["previous_response_id"])
	}
	input, _ := body["input"].([]any)
	if len(input) != 2 {
		t.Errorf("second round: sent %d input items, want 2 (full)", len(input))
	}
}

func TestResponsesProvider_NoFallbackOnFourXX(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL + "/v1/responses",
		Store:  &storeTrue,
	}, []string{"test-key"})

	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected 400 error")
	}
	if callCount != 1 {
		t.Fatalf("expected 1 HTTP call without fallback, got %d", callCount)
	}
}

func TestResponsesProvider_IgnoresNullPreviousResponseIDWithoutRollback(t *testing.T) {
	callCount := 0
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-null-prev","previous_response_id":null,"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, APIURL: server.URL + "/v1/responses", Store: &storeTrue}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	msgs2 := []message.Message{{Role: "user", Content: "hello"}, {Role: "user", Content: "follow up"}}
	rollbackSeen := false
	_, err := r.CompleteStream(context.Background(), "test-key", "gpt-5", "", msgs2, nil, 128, RequestTuning{}, func(delta message.StreamDelta) {
		if delta.Type == "rollback" {
			rollbackSeen = true
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", callCount)
	}
	if rollbackSeen {
		t.Fatal("did not expect rollback delta")
	}
	if _, ok := bodies[0]["previous_response_id"]; ok {
		t.Fatalf("request must not include previous_response_id, got %v", bodies[0]["previous_response_id"])
	}
}

func TestResponsesProvider_AlwaysFullInputOnHTTP(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-fixed","previous_response_id":null,"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, APIURL: server.URL + "/v1/responses", Store: &storeTrue}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	for _, msgs := range [][]message.Message{
		{{Role: "user", Content: "m1"}},
		{{Role: "user", Content: "m1"}, {Role: "user", Content: "m2"}},
		{{Role: "user", Content: "m1"}, {Role: "user", Content: "m2"}, {Role: "user", Content: "m3"}},
	} {
		_, err := r.CompleteStream(context.Background(), "test-key", "gpt-5", "", msgs, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
		if err != nil {
			t.Fatalf("CompleteStream: %v", err)
		}
	}
	if len(bodies) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(bodies))
	}
	for i, b := range bodies {
		if _, ok := b["previous_response_id"]; ok {
			t.Fatalf("request %d unexpectedly includes previous_response_id", i+1)
		}
	}
}
