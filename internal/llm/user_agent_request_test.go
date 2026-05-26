package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestOpenAICompleteStreamSetsDefaultUserAgent(t *testing.T) {
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, APIURL: srv.URL}, []string{"test-key"})
	openAIProvider, err := NewOpenAIProvider(provider, "")
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	_, err = openAIProvider.CompleteStream(context.Background(), "test-key", "gpt-test", "", []message.Message{{Role: "user", Content: "hello"}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if gotUserAgent != defaultLLMUserAgent() {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, defaultLLMUserAgent())
	}
}

func TestOpenAICompleteStreamSetsProviderUserAgent(t *testing.T) {
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, APIURL: srv.URL, UserAgent: "ProviderUA/1.0"}, []string{"test-key"})
	openAIProvider, err := NewOpenAIProvider(provider, "")
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	_, err = openAIProvider.CompleteStream(context.Background(), "test-key", "gpt-test", "", []message.Message{{Role: "user", Content: "hello"}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if gotUserAgent != "ProviderUA/1.0" {
		t.Fatalf("User-Agent = %q, want ProviderUA/1.0", gotUserAgent)
	}
}

func TestResponsesCompleteStreamSetsDefaultUserAgent(t *testing.T) {
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("responses", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL}, []string{"test-key"})
	responsesProvider, err := NewResponsesProviderWithClient(provider, srv.Client(), "")
	if err != nil {
		t.Fatalf("NewResponsesProviderWithClient: %v", err)
	}
	_, err = responsesProvider.CompleteStream(context.Background(), "test-key", "gpt-test", "", []message.Message{{Role: "user", Content: "hello"}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if gotUserAgent != defaultLLMUserAgent() {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, defaultLLMUserAgent())
	}
}

func TestResponsesCompleteStreamSetsProviderUserAgent(t *testing.T) {
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("responses", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL, UserAgent: "ProviderUA/1.0"}, []string{"test-key"})
	responsesProvider, err := NewResponsesProviderWithClient(provider, srv.Client(), "")
	if err != nil {
		t.Fatalf("NewResponsesProviderWithClient: %v", err)
	}
	_, err = responsesProvider.CompleteStream(context.Background(), "test-key", "gpt-test", "", []message.Message{{Role: "user", Content: "hello"}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if gotUserAgent != "ProviderUA/1.0" {
		t.Fatalf("User-Agent = %q, want ProviderUA/1.0", gotUserAgent)
	}
}

func TestOpenAICodexUserAgentMatchesCodexCLI(t *testing.T) {
	got := openAICodexUserAgent()
	if want := openAICodexOriginator + "/"; len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("openAICodexUserAgent = %q, want prefix %q", got, want)
	}
	if got == "Go-http-client/1.1" {
		t.Fatalf("openAICodexUserAgent used default Go UA")
	}
}
