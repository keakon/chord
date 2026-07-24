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

// TestOpenAIProvider_StreamOptionsDefaultAndCompatToggle confirms Chat
// Completions sends stream_options.include_usage by default and that
// compat.chat_completions.send_stream_options: false omits it for gateways
// that reject it.
func TestOpenAIProvider_StreamOptionsDefaultAndCompatToggle(t *testing.T) {
	cases := []struct {
		name        string
		compat      *config.ProviderCompatConfig
		wantPresent bool
	}{
		{"default sends stream_options", nil, true},
		{"compat disables stream_options", &config.ProviderCompatConfig{
			ChatCompletions: &config.ChatCompletionsCompatConfig{SendStreamOptions: new(false)},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(data, &gotBody)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			provider := NewProviderConfig("gateway", config.ProviderConfig{
				Type:   config.ProviderTypeChatCompletions,
				APIURL: server.URL + "/v1/chat/completions",
				Compat: tc.compat,
				Models: map[string]config.ModelConfig{
					"gpt-5.5": {Limit: config.ModelLimit{Context: 400000, Output: 128000}},
				},
			}, []string{"test-key"})
			o := &OpenAIProvider{provider: provider, client: server.Client(), responsesProvider: &ResponsesProvider{}}

			_, err := o.CompleteStream(
				context.Background(), "test-key", "gpt-5.5", "",
				[]message.Message{{Role: "user", Content: "hello"}},
				nil, 128, RequestTuning{},
				func(message.StreamDelta) {},
			)
			if err != nil {
				t.Fatalf("CompleteStream: %v", err)
			}
			_, has := gotBody["stream_options"]
			if has != tc.wantPresent {
				t.Fatalf("stream_options present = %v, want %v (body keys %v)", has, tc.wantPresent, sortedKeys(gotBody))
			}
		})
	}
}
