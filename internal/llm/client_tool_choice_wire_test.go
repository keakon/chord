package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestAdaptiveToolChoiceUsesFinalRequest(t *testing.T) {
	for _, protocol := range []string{config.ProviderTypeChatCompletions, config.ProviderTypeResponses, config.ProviderTypeMessages, config.ProviderTypeGenerateContent} {
		for _, mode := range []string{"required", "thinking", "no_tools", "auto", "none", "overridden"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				var calls int
				var bodies [][]byte
				var mu sync.Mutex
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					calls++
					bodies = append(bodies, body)
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":{"code":400,"type":"invalid_request_error","message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`)
				}))
				defer srv.Close()
				compat := &config.ModelCompatConfig{ForcedToolChoice: &config.ForcedToolChoiceCompatConfig{SuppressInThinking: new(true)}}
				if mode == "overridden" {
					field, value := "tool_choice", any("auto")
					switch protocol {
					case config.ProviderTypeMessages:
						value = map[string]any{"type": "auto"}
					case config.ProviderTypeGenerateContent:
						field, value = "toolConfig", map[string]any{"functionCallingConfig": map[string]any{"mode": "AUTO"}}
					}
					compat.RequestOverrides = &config.RequestOverridesConfig{Body: map[string]any{field: value}}
				}
				apiURL := srv.URL
				if protocol == config.ProviderTypeGenerateContent {
					apiURL += "/v1beta/"
				}
				provider := NewProviderConfig("sample", config.ProviderConfig{Type: protocol, APIURL: apiURL, Models: map[string]config.ModelConfig{"test-model": {Compat: compat}}}, []string{"sample-key"})
				var impl Provider
				var err error
				switch protocol {
				case config.ProviderTypeChatCompletions:
					impl, err = NewOpenAIProvider(provider, "")
				case config.ProviderTypeResponses:
					impl, err = NewResponsesProvider(provider, "")
				case config.ProviderTypeMessages:
					impl, err = NewAnthropicProvider(provider, "")
				case config.ProviderTypeGenerateContent:
					impl, err = NewGeminiProvider(provider, "")
				}
				if err != nil {
					t.Fatal(err)
				}
				client := NewClient(provider, impl, "test-model", 4096, "")
				defer client.Close()
				choice := "required"
				if mode == "auto" || mode == "none" {
					choice = mode
				}
				tuning := RequestTuning{
					OpenAI:    OpenAITuning{ToolChoice: choice},
					Anthropic: AnthropicTuning{ToolChoice: choice},
					Gemini:    GeminiTuning{ToolChoice: choice, ThinkingBudget: new(0)},
				}
				if mode == "thinking" {
					tuning.OpenAI.ReasoningEffort = "high"
					tuning.Anthropic.ThinkingType = "enabled"
					tuning.Anthropic.ThinkingBudget = 1024
					tuning.Gemini.ThinkingBudget = new(1024)
				}
				client.MergeNextRequestTuningOverride(tuning)
				defs := []message.ToolDefinition{{Name: "sample_tool", InputSchema: map[string]any{"type": "object"}}}
				if mode == "no_tools" {
					defs = nil
				}
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				_, err = client.CompleteStream(ctx, []message.Message{{Role: message.RoleUser, Content: "Sample"}}, defs, nil)
				if err == nil {
					t.Fatal("expected rejection")
				}
				want := 1
				if mode == "required" {
					want = 2
				}
				mu.Lock()
				defer mu.Unlock()
				if calls != want || provider.forcedChoiceRefused("test-model") {
					t.Fatalf("calls=%d want=%d memo=%v bodies=%s", calls, want, provider.forcedChoiceRefused("test-model"), bodies)
				}
				if calls == 2 && string(bodies[0]) == string(bodies[1]) {
					t.Fatal("probe reused the forced request body")
				}
			})
		}
	}
}

func TestAdaptiveToolChoicePreservesClientToolSurface(t *testing.T) {
	var bodies []map[string]json.RawMessage
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Request contains an invalid argument."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"response-1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Ready"}]}]}}`+"\n\n")
	}))
	defer srv.Close()
	provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {
		NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchResponses, APIURL: srv.URL, Preauthorized: true},
	}}}, []string{"sample-key"})
	impl, err := NewResponsesProvider(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(provider, impl, "test-model", 4096, "")
	defer client.Close()
	client.MergeNextRequestTuningOverride(RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required"}})
	begins := 0
	policy := &NativeToolPolicy{
		Permitted: func(string) bool { return true },
		Begin:     func(context.Context, NativeRequestRecord) (string, error) { begins++; return "request-1", nil },
		Finish:    func(string, message.NativeRequestOutcome, *message.Response, error) error { return nil },
	}
	resp, err := client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Sample"}},
		[]message.ToolDefinition{{Name: "sample_tool", InputSchema: map[string]any{"type": "object"}}}, nil, CompleteStreamOptions{NativeTools: policy})
	if err != nil || resp == nil || resp.Content != "Ready" || begins != 0 || !provider.forcedChoiceRefused("test-model") {
		t.Fatalf("resp=%+v err=%v native_requests=%d", resp, err, begins)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || string(bodies[0]["tools"]) != string(bodies[1]["tools"]) {
		t.Fatalf("probe changed tool surface: %#v", bodies)
	}
	if impl.bodyReuse.valid {
		t.Fatal("retry walk retained its request body cache")
	}
}
