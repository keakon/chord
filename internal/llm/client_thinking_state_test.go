package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestClientThinkingStateRequest(t *testing.T) {
	for _, testcase := range []struct {
		name      string
		model     string
		family    string
		wire      string
		blocks    []message.ThinkingBlock
		signature string
	}{
		{name: "aliased Claude", model: "deployment-a", family: modelcompat.NativeFamilyAnthropic, wire: modelcompat.WireFamilyOpenAIChat, blocks: []message.ThinkingBlock{{Thinking: "plan", Signature: "signed-block"}}},
		{name: "Gemini tool signature", model: "gemini-3-test", family: modelcompat.NativeFamilyGemini, wire: modelcompat.WireFamilyOpenAIChat, signature: "signed-block"},
		{name: "Gemini Messages block", model: "gemini-3-test", family: modelcompat.NativeFamilyGemini, wire: modelcompat.WireFamilyAnthropic, blocks: []message.ThinkingBlock{{Thinking: "plan", Signature: "signed-block"}}},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				select {
				case requests <- body:
				default:
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			t.Cleanup(server.Close)
			provider := NewProviderConfig("sample", config.ProviderConfig{
				Type:   config.ProviderTypeChatCompletions,
				APIURL: server.URL + "/v1/chat/completions",
				Compat: &config.ProviderCompatConfig{ChatCompletions: &config.ChatCompletionsCompatConfig{NativeThinking: testcase.family}},
				Models: map[string]config.ModelConfig{testcase.model: {Limit: config.ModelLimit{Context: 128000, Output: 4096}}},
			}, []string{"test-key"})
			impl := &OpenAIProvider{provider: provider, client: server.Client(), responsesProvider: &ResponsesProvider{}}
			client := NewClient(provider, impl, testcase.model, 4096, "")
			client.SetNextRequestTuningOverride(RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 1024}, Gemini: GeminiTuning{ThinkingLevel: "high"}})
			msgs := []message.Message{
				{Role: message.RoleUser, Content: "hello"},
				{Role: message.RoleAssistant, ThinkingBlocks: testcase.blocks, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`), ThoughtSignature: testcase.signature}}, Provenance: &message.MessageProvenance{ProviderID: "sample", ModelID: testcase.model, WireFamily: testcase.wire, NativeFamily: testcase.family}},
				{Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := client.CompleteStream(ctx, msgs, nil, nil); err != nil {
				t.Fatal(err)
			}
			body := <-requests
			if testcase.family == modelcompat.NativeFamilyGemini {
				if got := firstToolCallSignature(t, body); got != "signed-block" {
					t.Fatalf("signature = %q", got)
				}
				extra, ok := body["extra_body"].(map[string]any)
				if !ok {
					t.Fatal("Gemini thinking controls omitted")
				}
				google := extra["google"].(map[string]any)
				thinking := google["thinking_config"].(map[string]any)
				if thinking["thinking_level"] != "high" {
					t.Fatalf("thinking = %+v", thinking)
				}
			} else {
				assistant := body["messages"].([]any)[1].(map[string]any)
				blocks, ok := assistant["thinking_blocks"].([]any)
				if !ok || len(blocks) != 1 || blocks[0].(map[string]any)["signature"] != "signed-block" {
					t.Fatalf("assistant = %+v", assistant)
				}
				if body["thinking"] == nil {
					t.Fatal("Claude thinking controls omitted")
				}
			}
		})
	}
}
