package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestAnthropicDerivesDurableBoundaryWithoutMainHints(t *testing.T) {
	var body anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-a\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()
	cfg := NewProviderConfig("anthropic", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: server.URL + "/messages"}, []string{"test-key"})
	provider := &AnthropicProvider{provider: cfg, client: server.Client()}
	msgs := []message.Message{{Role: message.RoleUser, Content: "request"}, {Role: message.RoleAssistant, Content: "response"}, {Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "hint"}}
	_, err := provider.CompleteStream(context.Background(), "test-key", "sample/test-model", "instructions", msgs, nil, 32, RequestTuning{Anthropic: AnthropicTuning{PromptCacheMode: "explicit"}}, func(message.StreamDelta) {})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(body)
	var decoded struct {
		Messages []struct {
			Content []struct {
				Text         string `json:"text"`
				CacheControl any    `json:"cache_control"`
			}
		} `json:"messages"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	var markedResponse bool
	for _, msg := range decoded.Messages {
		for _, block := range msg.Content {
			if block.Text == "hint" && block.CacheControl != nil {
				t.Fatal("transient suffix was cached")
			}
			if block.Text == "response" && block.CacheControl != nil {
				markedResponse = true
			}
		}
	}
	if !markedResponse {
		t.Fatal("durable endpoint was not cached")
	}
}
