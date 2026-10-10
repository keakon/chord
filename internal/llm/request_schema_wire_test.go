package llm

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestRequestOverridesPreserveSchemaOrderOnEveryWire(t *testing.T) {
	for _, wire := range []string{config.ProviderTypeChatCompletions, config.ProviderTypeMessages, config.ProviderTypeResponses, config.ProviderTypeGenerateContent} {
		t.Run(wire, func(t *testing.T) {
			requests := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				requests <- body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":{"message":"forced test error","code":400,"status":"INVALID_ARGUMENT"}}`)
			}))
			defer server.Close()
			apiURL := server.URL
			if wire == config.ProviderTypeGenerateContent {
				apiURL += "/v1beta/"
			}
			cfg := NewProviderConfig("sample", config.ProviderConfig{Type: wire, APIURL: apiURL, Compat: &config.ProviderCompatConfig{RequestOverrides: &config.RequestOverridesConfig{Body: map[string]any{"sample_flag": true}}}}, []string{"test-key"})
			var provider Provider
			var err error
			switch wire {
			case config.ProviderTypeChatCompletions:
				provider = &OpenAIProvider{provider: cfg, client: server.Client()}
			case config.ProviderTypeResponses:
				provider = &ResponsesProvider{provider: cfg, client: server.Client()}
			case config.ProviderTypeMessages:
				provider, err = NewAnthropicProviderWithClient(cfg, server.Client(), "")
			case config.ProviderTypeGenerateContent:
				provider, err = NewGeminiProvider(cfg, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.CompleteStream(context.Background(), "test-key", "model-1", "", []message.Message{{Role: message.RoleUser, Content: "Edit the file."}}, []message.ToolDefinition{{Name: "edit", InputSchema: editLikeSchema()}}, 128, RequestTuning{}, func(message.StreamDelta) {})
			if err == nil {
				t.Fatal("expected forced server error")
			}
			select {
			case body := <-requests:
				oldPos, newPos := bytes.Index(body, []byte(`"old_string":`)), bytes.Index(body, []byte(`"new_string":`))
				if oldPos < 0 || newPos < oldPos {
					t.Fatalf("source must precede replacement in final request: %s", body)
				}
				if !bytes.Contains(body, []byte(`"sample_flag":true`)) {
					t.Fatal("override was not applied")
				}
			default:
				t.Fatal("provider did not send a request")
			}
		})
	}
}
