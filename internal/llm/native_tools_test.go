package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func nativeResponseFixture() string {
	return "data: " + `{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[{"type":"web_search_call","id":"search-1","status":"completed","action":{"type":"search","query":"sample","sources":[]}},{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"Search completed.","annotations":[{"type":"url_citation","url":"https://example.invalid","title":"Sample","start_index":0,"end_index":6}]}]}],"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n\n"
}
func nativeAnthropicFixture(stop string) string {
	events := []struct{ kind, data string }{
		{"message_start", `{"type":"message_start","message":{"id":"msg-1","role":"assistant","usage":{"input_tokens":10,"output_tokens":0}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"search-1","name":"web_search","input":{"query":"sample"}}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"search-1","content":[]}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":1}`},
		{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"Search completed."}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.invalid/a","title":"Sample","cited_text":"Source fact"}}}`},
		{"content_block_stop", `{"type":"content_block_stop","index":2}`},
		{"message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":5}}`, stop)},
		{"message_stop", `{"type":"message_stop"}`},
	}
	var b strings.Builder
	for _, ev := range events {
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", ev.kind, ev.data)
	}
	return b.String()
}

func TestNativeWebSearchRequestAuthorizationHistoryAndReplay(t *testing.T) {
	for _, protocol := range []string{config.ProviderTypeResponses, config.ProviderTypeMessages} {
		t.Run(protocol, func(t *testing.T) {
			var bodies []map[string]json.RawMessage
			began, finished := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if began != len(bodies)+1 {
					t.Error("request sent before durable authorization")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				bodies = append(bodies, body)
				w.Header().Set("Content-Type", "text/event-stream")
				fixture := nativeResponseFixture()
				if protocol == config.ProviderTypeMessages {
					fixture = nativeAnthropicFixture("end_turn")
				}
				_, _ = io.WriteString(w, fixture)
			}))
			defer srv.Close()
			contract := config.NativeWebSearchResponses
			if protocol == config.ProviderTypeMessages {
				contract = config.NativeWebSearchMessages
			}
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: protocol, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 32000, Output: 1024}, NativeWebSearch: &config.NativeWebSearchConfig{Contract: contract, APIURL: srv.URL, Preauthorized: true, AllowedDomains: []string{"example.invalid"}, MaxUses: 2}}}}, []string{"key"})
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
			client := NewClient(provider, impl, "test-model", 1024, "Use tools when useful.")
			defer client.Close()
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true }, Begin: func(_ context.Context, r NativeRequestRecord) (string, error) {
				began++
				if r.Authorization.Tool != toolname.WebSearch || !strings.Contains(string(r.Authorization.Constraints), "example.invalid") {
					t.Error("authorization lost domains")
				}
				return fmt.Sprintf("request-%d", began), nil
			}, Finish: func(_ string, _ message.NativeRequestOutcome, r *message.Response, e error) error {
				finished++
				return nil
			}}
			defs := []message.ToolDefinition{{Name: toolname.WebSearch, InputSchema: map[string]any{"type": "object"}}, {Name: "sample_lookup", InputSchema: map[string]any{"type": "object"}}}
			history := []message.Message{{Role: message.RoleUser, Content: "Search the sample documentation"}}
			resp, err := client.CompleteStreamWithOptions(t.Context(), history, defs, func(message.StreamDelta) {}, CompleteStreamOptions{NativeTools: policy})
			if err != nil {
				t.Fatal(err)
			}
			if finished != 1 || resp.NativeTools == nil || len(resp.NativeTools.Calls) != 1 || len(resp.ToolCalls) != 0 {
				t.Fatalf("response=%+v receipts=%d", resp, finished)
			}
			if protocol == config.ProviderTypeResponses && resp.Content != "Search [Sample](<https://example.invalid>) completed." {
				t.Fatalf("native answer lost visible citations: %q", resp.Content)
			}
			if protocol == config.ProviderTypeMessages && resp.Content != "Search completed. [Sample](<https://example.invalid/a>)" {
				t.Fatalf("native answer lost streamed citations: %q", resp.Content)
			}
			var declarations []map[string]json.RawMessage
			if err := json.Unmarshal(bodies[0]["tools"], &declarations); err != nil {
				t.Fatal(err)
			}
			if len(declarations) != 2 {
				t.Fatalf("duplicate/absent declarations: %s", bodies[0]["tools"])
			}
			if !strings.Contains(string(bodies[0]["tools"]), "example.invalid") {
				t.Fatal("hard filter missing")
			}
			history = append(history, message.Message{Role: message.RoleAssistant, Content: resp.Content, NativeTools: resp.NativeTools}, message.Message{Role: message.RoleUser, Content: "Explain the result"})
			_, err = client.CompleteStreamWithOptions(t.Context(), history, defs, func(message.StreamDelta) {}, CompleteStreamOptions{NativeTools: policy})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(bodies[1])
			if !strings.Contains(string(raw), "search-1") {
				t.Fatalf("native history missing: %s", raw)
			}
			if protocol == config.ProviderTypeMessages && !strings.Contains(string(raw), "web_search_result_location") {
				t.Fatal("streamed citation metadata lost on replay")
			}
			if protocol == config.ProviderTypeResponses && !strings.Contains(string(raw), "url_citation") {
				t.Fatal("citation metadata lost")
			}
			history[1].NativeTools.APIURL = "https://example.invalid/other"
			if _, err = client.CompleteStreamWithOptions(t.Context(), history, defs, nil, CompleteStreamOptions{NativeTools: policy}); err == nil {
				t.Fatal("incompatible native history accepted")
			}
			if len(bodies) != 2 {
				t.Fatal("incompatible history sent")
			}
		})
	}
}

func TestNativeAuthorizationAndUnknownOutcomeStopAllReplay(t *testing.T) {
	for _, failBefore := range []bool{true, false} {
		t.Run(fmt.Sprint(failBefore), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
			}))
			defer srv.Close()
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchResponses, APIURL: srv.URL, Preauthorized: true}}}}, []string{"key-1", "key-2"})
			impl, err := NewResponsesProvider(provider, "")
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(provider, impl, "test-model", 1024, "")
			defer client.Close()
			client.SetStreamRetryRounds(3)
			finished := 0
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true }, Begin: func(context.Context, NativeRequestRecord) (string, error) {
				if failBefore {
					return "", errors.New("write rejected")
				}
				return "request-1", nil
			}, Finish: func(string, message.NativeRequestOutcome, *message.Response, error) error { finished++; return nil }}
			_, err = client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Search"}}, nil, nil, CompleteStreamOptions{NativeTools: policy})
			if _, ok := errors.AsType[*NativeToolError](err); !ok {
				t.Fatalf("err=%v", err)
			}
			want := 1
			if failBefore {
				want = 0
			}
			if requests != want || finished != want {
				t.Fatalf("requests=%d finished=%d", requests, finished)
			}
		})
	}
}

func TestNativePauseContinuationAndResultBarrier(t *testing.T) {
	for _, failReceipt := range []bool{false, true} {
		t.Run(fmt.Sprint(failReceipt), func(t *testing.T) {
			var bodies []map[string]json.RawMessage
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				bodies = append(bodies, body)
				fixture := nativeAnthropicFixture("pause_turn")
				omitIndex := `"index":1`
				if len(bodies) == 2 {
					fixture = nativeAnthropicFixture("end_turn")
					omitIndex = `"index":0`
				}
				// Pause after invocation; the continuation returns only its result.
				var chunks []string
				for chunk := range strings.SplitSeq(fixture, "\n\n") {
					if !strings.Contains(chunk, omitIndex) {
						chunks = append(chunks, chunk)
					}
				}
				fixture = strings.Join(chunks, "\n\n")
				fixture = strings.Replace(fixture, `"role":"assistant","usage"`, `"role":"assistant","container":{"id":"container-1"},"usage"`, 1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, fixture)
			}))
			defer srv.Close()
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchMessages, APIURL: srv.URL, Preauthorized: true}}}}, []string{"key"})
			impl, err := NewAnthropicProvider(provider, "")
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(provider, impl, "test-model", 1024, "")
			defer client.Close()
			var recorded []int
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true }, Begin: func(_ context.Context, record NativeRequestRecord) (string, error) {
				if record.Continuation != len(recorded) {
					t.Errorf("continuation %d after %d receipts", record.Continuation, len(recorded))
				}
				return fmt.Sprintf("request-%d", record.Continuation), nil
			}, Finish: func(_ string, _ message.NativeRequestOutcome, resp *message.Response, err error) error {
				if err != nil {
					t.Error(err)
				}
				recorded = append(recorded, resp.Usage.InputTokens)
				if failReceipt {
					return errors.New("receipt write rejected")
				}
				return nil
			}}
			resp, err := client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Search"}}, nil, nil, CompleteStreamOptions{NativeTools: policy})
			if failReceipt {
				if !IsNativeToolError(err) || len(bodies) != 1 {
					t.Fatalf("requests=%d err=%v", len(bodies), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(bodies) != 2 || len(recorded) != 2 || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 || recorded[0]+recorded[1] != 20 {
				t.Fatalf("usage=%+v requests=%d receipts=%v", resp.Usage, len(bodies), recorded)
			}
			if string(bodies[1]["container"]) != `"container-1"` || !strings.Contains(string(bodies[1]["messages"]), "search-1") {
				t.Fatalf("continuation lost native state: %s", bodies[1])
			}
			if len(resp.NativeTools.RequestIDs) != 2 || len(resp.NativeTools.Calls) != 1 || !strings.Contains(string(resp.NativeTools.Calls[0].Input), "sample") {
				t.Fatalf("history=%+v", resp.NativeTools)
			}
		})
	}
}

func TestNativePendingHistoryCannotLoseAuthorization(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				_, _ = io.WriteString(w, nativeResponseFixture())
			}))
			defer srv.Close()
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchResponses, APIURL: srv.URL, Preauthorized: true, MaxUses: 2}}}}, []string{"key"})
			impl, err := NewResponsesProvider(provider, "")
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(provider, impl, "test-model", 1024, "")
			defer client.Close()
			history := []message.Message{{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Target: "sample/test-model", APIURL: srv.URL, Protocol: config.ProviderTypeResponses, Authorization: message.NativeToolAuthorization{Tool: toolname.WebSearch, Contract: config.NativeWebSearchResponses, Constraints: json.RawMessage(`{"max_uses":1}`)}, Calls: []message.HostedCall{{ID: "pending", Name: toolname.WebSearch}}}}}
			policy := &NativeToolPolicy{Permitted: func(string) bool { return allowed }, Begin: func(context.Context, NativeRequestRecord) (string, error) {
				t.Error("authorization changed before pending tool completed")
				return "", nil
			}, Finish: func(string, message.NativeRequestOutcome, *message.Response, error) error { return nil }}
			_, err = client.CompleteStreamWithOptions(t.Context(), history, nil, nil, CompleteStreamOptions{NativeTools: policy})
			if !IsNativeToolError(err) || requests != 0 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestNativeDeclarationPreservesToolChoice(t *testing.T) {
	for _, contract := range []string{config.NativeWebSearchResponses, config.NativeWebSearchMessages} {
		for _, choice := range []string{"", `"auto"`, `"required"`, `"none"`, `{"type":"any"}`, `{"type":"tool","name":"sample_lookup"}`} {
			t.Run(contract+"/"+choice, func(t *testing.T) {
				body := map[string]json.RawMessage{"tools": json.RawMessage(`[]`)}
				if choice != "" {
					body["tool_choice"] = json.RawMessage(choice)
				}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				raw, err = addNativeWebSearchDeclaration(raw, &config.NativeWebSearchConfig{Contract: contract, MaxUses: 1})
				if err != nil {
					t.Fatal(err)
				}
				var out map[string]json.RawMessage
				if err := json.Unmarshal(raw, &out); err != nil {
					t.Fatal(err)
				}
				if string(out["tool_choice"]) != choice {
					t.Fatalf("tool_choice=%s want=%s", out["tool_choice"], choice)
				}
			})
		}
	}
}

func TestNativeForcedClientToolChoiceRetainsOrdinaryRequest(t *testing.T) {
	for _, protocol := range []string{config.ProviderTypeResponses, config.ProviderTypeMessages} {
		t.Run(protocol, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				want := `"required"`
				if protocol == config.ProviderTypeMessages {
					want = `{"type":"any"}`
				}
				if string(body["tool_choice"]) != want {
					t.Errorf("tool_choice=%s want=%s", body["tool_choice"], want)
				}
				if strings.Contains(string(body["tools"]), `"type":"web_search`) || !strings.Contains(string(body["tools"]), toolname.WebSearch) {
					t.Errorf("ordinary tools changed: %s", body["tools"])
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
			if protocol == config.ProviderTypeMessages {
				contract = config.NativeWebSearchMessages
			}
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: protocol, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: contract, APIURL: srv.URL, Preauthorized: true}}}}, []string{"key"})
			var impl Provider
			var err error
			if protocol == config.ProviderTypeMessages {
				impl, err = NewAnthropicProvider(provider, "")
			} else {
				impl, err = NewResponsesProvider(provider, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(provider, impl, "test-model", 1024, "")
			defer client.Close()
			client.SetNextRequestTuningOverride(RequestTuning{OpenAI: OpenAITuning{ToolChoice: "required"}, Anthropic: AnthropicTuning{ToolChoice: "required"}})
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true }, Begin: func(context.Context, NativeRequestRecord) (string, error) {
				t.Error("forced client-tool request enabled native execution")
				return "", errors.New("unexpected native request")
			}, Finish: func(string, message.NativeRequestOutcome, *message.Response, error) error { return nil }}
			_, err = client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Use the search tool"}}, []message.ToolDefinition{{Name: toolname.WebSearch, InputSchema: map[string]any{"type": "object"}}}, nil, CompleteStreamOptions{NativeTools: policy})
			if err != nil || requests != 1 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}
