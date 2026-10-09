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
	"github.com/keakon/chord/internal/modelcompat"
)

func TestNativeClientToolReplayAdmission(t *testing.T) {
	for _, protocol := range []string{config.ProviderTypeResponses, config.ProviderTypeMessages} {
		for _, paired := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/orphan", true: "/paired"}[paired], func(t *testing.T) {
				var requests atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					raw, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(raw), "call-1") || !strings.Contains(string(raw), "Sample result") {
						t.Errorf("lost paired history: %s", raw)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fixture := nativeResponseFixture()
					if protocol == config.ProviderTypeMessages {
						fixture = nativeAnthropicFixture("end_turn")
					}
					_, _ = io.WriteString(w, fixture)
				}))
				defer srv.Close()
				contract := config.NativeWebSearchResponses
				item := json.RawMessage(`{"type":"function_call","id":"fc-1","call_id":"call-1","name":"sample_lookup","arguments":"{}"}`)
				if protocol == config.ProviderTypeMessages {
					contract = config.NativeWebSearchMessages
					item = json.RawMessage(`{"type":"tool_use","id":"call-1","name":"sample_lookup","input":{}}`)
				}
				provider := NewProviderConfig("sample", config.ProviderConfig{Type: protocol, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: contract, APIURL: srv.URL, Preauthorized: true}}}}, []string{"key"})
				var impl Provider
				var err error
				if protocol == config.ProviderTypeResponses {
					impl, err = NewResponsesProvider(provider, "")
				} else {
					impl, err = NewAnthropicProvider(provider, "")
				}
				if err != nil {
					t.Fatal(err)
				}
				client := NewClient(provider, impl, "test-model", 1024, "")
				defer client.Close()
				history := []message.Message{{Role: message.RoleUser, Content: "Read sample records"}, {Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "sample_lookup", Args: json.RawMessage(`{}`)}}, NativeTools: &message.NativeToolHistory{Target: "sample/test-model", APIURL: srv.URL, Protocol: protocol, Items: []json.RawMessage{item}}}}
				if paired {
					history = append(history, message.Message{Role: message.RoleTool, ToolCallID: "call-1", Content: "Sample result"})
				}
				history = append(history, message.Message{Role: message.RoleUser, Content: "Continue"})
				before, _ := json.Marshal(history)
				begins := 0
				policy := &NativeToolPolicy{Permitted: func(string) bool { return true }, Begin: func(context.Context, NativeRequestRecord) (string, error) { begins++; return "request-1", nil }, Finish: func(string, message.NativeRequestOutcome, *message.Response, error) error { return nil }}
				_, err = client.CompleteStreamWithOptions(t.Context(), history, []message.ToolDefinition{{Name: "sample_lookup", InputSchema: map[string]any{"type": "object"}}}, nil, CompleteStreamOptions{NativeTools: policy})
				if paired {
					if err != nil || requests.Load() != 1 || begins != 1 {
						t.Fatalf("requests=%d begins=%d err=%v", requests.Load(), begins, err)
					}
				} else {
					if !IsNativeToolError(err) || requests.Load() != 0 || begins != 0 {
						t.Fatalf("unsafe replay dispatched: requests=%d begins=%d err=%v", requests.Load(), begins, err)
					}
				}
				after, _ := json.Marshal(history)
				if string(before) != string(after) {
					t.Fatal("canonical history mutated")
				}
			})
		}
	}
}

func TestNativeEstimateMatchesReplaySurface(t *testing.T) {
	text := strings.Repeat("sample ", 3000)
	raw, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}})
	native := &message.NativeToolHistory{Items: []json.RawMessage{raw}}
	mirrored := []message.Message{{Role: message.RoleAssistant, Content: text, NativeTools: native}}
	rawOnly := []message.Message{{Role: message.RoleAssistant, NativeTools: native}}
	wireA, _ := json.Marshal(convertMessagesToResponsesWithItemIDs("", mirrored, false, false, false))
	wireB, _ := json.Marshal(convertMessagesToResponsesWithItemIDs("", rawOnly, false, false, false))
	if string(wireA) != string(wireB) {
		t.Fatal("test requires identical wire requests")
	}
	model := config.ModelConfig{Limit: config.ModelLimit{Context: 16000, Output: 4096}}
	capA, estimateA, _ := clampEffectiveMaxTokens(model, 4096, 4096, RequestTuning{}, "", mirrored, nil, 0)
	capB, estimateB, _ := clampEffectiveMaxTokens(model, 4096, 4096, RequestTuning{}, "", rawOnly, nil, 0)
	if estimateA != estimateB || capA != capB || capA != 4096 {
		t.Fatalf("estimates %d/%d, output budgets %d/%d", estimateA, estimateB, capA, capB)
	}
}

func TestNativeClientToolReplaySurvivesStrictNormalization(t *testing.T) {
	for _, protocol := range []string{config.ProviderTypeResponses, config.ProviderTypeMessages} {
		t.Run(protocol, func(t *testing.T) {
			raw := json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"sample_lookup","arguments":"{}"}`)
			family, encoding := modelcompat.WireFamilyOpenAIResponses, modelcompat.ToolResultEncodingOpenAIToolRole
			if protocol == config.ProviderTypeMessages {
				raw = json.RawMessage(`{"type":"tool_use","id":"call-1","name":"sample_lookup","input":{}}`)
				family, encoding = modelcompat.WireFamilyAnthropic, modelcompat.ToolResultEncodingAnthropicUserBlock
			}
			history := []message.Message{
				{Role: message.RoleUser, Content: "Read sample records"},
				{Role: message.RoleAssistant, Content: "Reading", ReasoningContent: "Plan", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "Plan", Signature: "sample-signature"}}, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "sample_lookup", Args: json.RawMessage(`{}`)}}, NativeTools: &message.NativeToolHistory{Items: []json.RawMessage{raw}}},
				{Role: message.RoleTool, ToolCallID: "call-1", Content: "Sample result"},
			}
			target := modelcompat.TargetModel{WireFamily: family, ReasoningContinuityMode: modelcompat.ReasoningContinuityAnthropicBlocks, SupportsStructuredTools: true, ToolResultEncoding: encoding}
			projected, _ := modelcompat.NormalizeForTarget(history, target, modelcompat.NormalizeOptions{StructuredTools: true, ReplayCompat: modelcompat.ReplayCompatStrict})
			if err := validateNativeClientToolReplay(projected); err != nil {
				t.Fatal(err)
			}
			var wire []byte
			if protocol == config.ProviderTypeResponses {
				wire, _ = json.Marshal(convertMessagesToResponsesWithItemIDs("", projected, false, false, false))
			} else {
				messages, _ := convertMessagesWithMap(projected)
				wire, _ = json.Marshal(messages)
			}
			if !strings.Contains(string(wire), string(raw)) || !strings.Contains(string(wire), "Sample result") {
				t.Fatalf("lost native replay pairing: %s", wire)
			}
			orphaned := projected[:2]
			if err := validateNativeClientToolReplay(orphaned); err == nil {
				t.Fatal("strict projection bypassed orphan validation")
			}
		})
	}
}
