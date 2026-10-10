package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func pendingAnthropicNativeHistory() []message.Message {
	return []message.Message{
		{Role: message.RoleUser, Content: "Read the sample and search"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "sample_lookup", Args: json.RawMessage(`{}`)}}, NativeTools: &message.NativeToolHistory{
			Protocol: config.ProviderTypeMessages,
			Items: []json.RawMessage{
				json.RawMessage(`{"type":"thinking","thinking":"Sample reasoning","signature":"sig-1"}`),
				json.RawMessage(`{"type":"server_tool_use","id":"search-1","name":"web_search","input":{"query":"sample"}}`),
				json.RawMessage(`{"type":"tool_use","id":"call-1","name":"sample_lookup","input":{}}`),
			},
			Calls: []message.HostedCall{{ID: "search-1", Name: "web_search", Input: json.RawMessage(`{"query":"sample"}`)}},
		}},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "Sample result"},
	}
}

func TestAnthropicNativeMixedContinuationWire(t *testing.T) {
	for _, multipart := range []bool{false, true} {
		name := "text"
		if multipart {
			name = "multipart"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body struct {
					Messages []struct {
						Role    string
						Content []json.RawMessage
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				last := body.Messages[len(body.Messages)-1]
				if last.Role != "user" || len(last.Content) != 1 {
					t.Errorf("mixed continuation must contain only one result: %+v", last)
				}
				var result struct {
					Type         string
					Content      json.RawMessage
					CacheControl json.RawMessage `json:"cache_control"`
				}
				if err := json.Unmarshal(last.Content[0], &result); err != nil {
					t.Error(err)
				}
				if result.Type != "tool_result" || !strings.Contains(string(result.Content), "Sample hint") || !strings.Contains(string(result.Content), "Another hint") || len(result.CacheControl) != 0 {
					t.Errorf("reminder projection/cache boundary: %s", last.Content[0])
				}
				if multipart && !strings.Contains(string(result.Content), "document") {
					t.Error("attachment lost from tool result")
				}
				if string(body.Messages[1].Content[0]) != `{"type":"thinking","thinking":"Sample reasoning","signature":"sig-1"}` {
					t.Errorf("signed block changed: %s", body.Messages[1].Content[0])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				// The server result answers the previous response's invocation.
				var chunks []string
				for chunk := range strings.SplitSeq(nativeAnthropicFixture("end_turn"), "\n\n") {
					if !strings.Contains(chunk, `"index":0`) {
						chunks = append(chunks, chunk)
					}
				}
				_, _ = io.WriteString(w, strings.Join(chunks, "\n\n"))
			}))
			defer srv.Close()
			cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {Modalities: &config.ModelModalities{Input: []string{"text", "pdf"}}, NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchMessages, APIURL: srv.URL, Preauthorized: true}}}}, []string{"key"})
			impl, err := NewAnthropicProvider(cfg, "")
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(cfg, impl, "test-model", 1024, "")
			defer client.Close()
			begins := 0
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true }, Begin: func(context.Context, NativeRequestRecord) (string, error) { begins++; return "request-1", nil }, Finish: func(string, message.NativeRequestOutcome, *message.Response, error) error { return nil }}
			request, err := resolveNativeToolRequest(cfg, "test-model", RequestTuning{}, policy)
			if err != nil {
				t.Fatal(err)
			}
			history := pendingAnthropicNativeHistory()
			history[1].NativeTools.Target = "sample/test-model"
			history[1].NativeTools.APIURL = srv.URL
			history[1].NativeTools.Authorization = request.authorization
			if multipart {
				history[2].Parts = []message.ContentPart{{Type: message.ContentPartText, Text: "Sample result"}, {Type: message.ContentPartPDF, Data: []byte("%PDF-sample"), MimeType: "application/pdf"}}
			}
			history = append(history, message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "<system-reminder>Sample hint</system-reminder>"}, message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "<system-reminder>Another hint</system-reminder>"})
			if multipart {
				history[len(history)-1].Content = ""
				history[len(history)-1].Parts = []message.ContentPart{{Type: message.ContentPartText, Text: "Another hint"}, {Type: message.ContentPartText, Text: "Sample snapshot"}}
			}
			before, _ := json.Marshal(history)
			client.SetNextRequestTuningOverride(RequestTuning{Anthropic: AnthropicTuning{CacheBoundary: AnthropicCacheBoundary{MessageIndex: 2, Valid: true}}})
			resp, err := client.CompleteStreamWithOptions(t.Context(), history, []message.ToolDefinition{{Name: "sample_lookup", InputSchema: map[string]any{"type": "object"}}}, nil, CompleteStreamOptions{NativeTools: policy})
			if err != nil || requests.Load() != 1 || begins != 1 {
				t.Fatalf("requests=%d begins=%d err=%v", requests.Load(), begins, err)
			}
			history = append(history, message.Message{Role: message.RoleAssistant, NativeTools: resp.NativeTools})
			if anthropicNativeContinuationIndex(history) >= 0 {
				t.Fatal("server result did not close pending continuation")
			}
			after, _ := json.Marshal(history[:len(history)-1])
			if string(before) != string(after) {
				t.Fatal("canonical history mutated")
			}
		})
	}
}

func TestAnthropicNativeContinuationAdmission(t *testing.T) {
	for _, scenario := range []string{"missing", "duplicate", "unrelated", "user", "mailbox", "early-overlay", "partial"} {
		t.Run(scenario, func(t *testing.T) {
			history := pendingAnthropicNativeHistory()
			switch scenario {
			case "missing":
				history = history[:2]
			case "duplicate":
				history = append(history, history[2])
			case "unrelated":
				history[2].ToolCallID = "call-2"
			case "user":
				history = append(history, message.Message{Role: message.RoleUser, Content: "New request"})
			case "mailbox":
				history = append(history, message.Message{Role: message.RoleUser, Kind: message.KindSubAgentMailbox, Content: "Worker result"})
			case "early-overlay":
				history = append(history[:2:2], message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "Sample hint"}, history[2])
			case "partial":
				history[1].ToolCalls = append(history[1].ToolCalls, message.ToolCall{ID: "call-2", Name: "sample_lookup"})
			}
			begins := 0
			policy := &NativeToolPolicy{Begin: func(context.Context, NativeRequestRecord) (string, error) { begins++; return "request-1", nil }}
			ctx := context.WithValue(t.Context(), nativePolicyContextKey{}, policy)
			cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
			_, err := completeWithNativeTools(ctx, nil, cfg, "", "test-model", "", history, nil, 1024, RequestTuning{}, config.ServiceTierStandard, nil)
			if !IsNativeToolError(err) || begins != 0 {
				t.Fatalf("unsafe input reached authorization/dispatch: begins=%d err=%v", begins, err)
			}
		})
	}
	paused := pendingAnthropicNativeHistory()[:2]
	paused[1].ToolCalls = nil
	paused[1].NativeTools.Items = paused[1].NativeTools.Items[:2]
	if err := validateAnthropicNativeContinuation(paused); err != nil {
		t.Fatalf("pause_turn must resume unchanged: %v", err)
	}
}

func TestAnthropicNativeContinuationMultipleResultsAndOrdinaryInput(t *testing.T) {
	history := pendingAnthropicNativeHistory()
	history[1].ToolCalls = append(history[1].ToolCalls, message.ToolCall{ID: "call-2", Name: "sample_lookup", Args: json.RawMessage(`{}`)})
	history = append(history, message.Message{Role: message.RoleTool, ToolCallID: "call-2", Content: "Second result"}, message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "Sample hint"})
	if err := validateAnthropicNativeContinuation(history); err != nil {
		t.Fatal(err)
	}
	wire, _ := convertMessagesWithMap(history)
	blocks := wire[len(wire)-1].Content.([]anthropicContent)
	if len(blocks) != 2 || blocks[0].Content != "Sample result" || !strings.Contains(blocks[1].Content.(string), "Sample hint") || anthropicCacheDurableMessageCount(history) != 3 {
		t.Fatalf("multiple-result continuation/cache prefix: %+v", blocks)
	}
	history[1].NativeTools.Calls[0].Result = json.RawMessage(`[]`)
	history[len(history)-1] = message.Message{Role: message.RoleUser, Content: "Independent input"}
	wire, _ = convertMessagesWithMap(history)
	blocks = wire[len(wire)-1].Content.([]anthropicContent)
	if len(blocks) != 3 || blocks[2].Type != "text" || blocks[2].Text != "Independent input" {
		t.Fatalf("ordinary input was folded into a result: %+v", blocks)
	}
}
