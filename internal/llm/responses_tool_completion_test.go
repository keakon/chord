package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesUnfinishedToolCallsAreNotExecutable(t *testing.T) {
	// Inspired by pi #9974: even closed JSON is not proof that a call finished.
	for _, tc := range []struct {
		name   string
		events []string
	}{
		{"valid JSON", []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
		}},
		{"empty arguments", []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
		}},
		{"arguments done without item completion", []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{}"}`,
		}},
		{"custom input", []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch"}}`,
			`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":"*** Begin Patch\n"}`,
		}},
		{"custom input done without item completion", []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch"}}`,
			`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":"*** Begin Patch\n*** End Patch"}`,
			`{"type":"response.custom_tool_call_input.done","item_id":"ct_1"}`,
		}},
		{"incomplete function item", []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","status":"incomplete","arguments":"{}"}}`,
		}},
		{"in-progress custom item", []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch","status":"in_progress","input":"*** Begin Patch\n*** End Patch"}}`,
		}},
		{"incomplete custom item", []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch","status":"incomplete","input":"*** Begin Patch\n*** End Patch"}}`,
		}},
	} {
		for _, terminal := range []string{
			`[DONE]`,
			`{"type":"response.completed","response":{"status":"completed","output":[]}}`,
			`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`,
		} {
			t.Run(tc.name+"/"+terminal, func(t *testing.T) {
				events := append([]string{}, tc.events...)
				events = append(events, terminal)
				for _, withCallback := range []bool{false, true} {
					var cb StreamCallback
					if withCallback {
						cb = func(message.StreamDelta) {}
					}
					resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(events), cb, nil, nil, "", false, false, false)
					if err != nil {
						t.Fatal(err)
					}
					if len(resp.ToolCalls) != 0 || resp.StopReason != "length" {
						t.Fatalf("response = %+v, want no executable calls and length", resp)
					}
				}
			})
		}
	}
}

func TestResponsesIncompleteItemKeepsCompletedSibling(t *testing.T) {
	events := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","status":"completed","arguments":"{\"value\":1}"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"sample_tool"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"sample_tool","status":"incomplete","arguments":"{\"value\":2}"}}`,
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`,
	}
	var ended []string
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(events), func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaToolUseEnd && delta.ToolCall != nil {
			ended = append(ended, delta.ToolCall.ID)
		}
	}, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || string(resp.ToolCalls[0].Args) != `{"value":1}` {
		t.Fatalf("executable calls = %+v, want completed sibling only", resp.ToolCalls)
	}
	if len(ended) != 1 || ended[0] != "call_1" {
		t.Fatalf("ended = %v, want completed sibling only", ended)
	}
}

func TestResponsesExplicitIncompleteItemBlocksTerminalRecovery(t *testing.T) {
	// A proxy may append a terminal snapshot that omits the item's incomplete
	// status. The explicit stream event is stronger evidence: never execute the
	// partially emitted call just because the trailer looks complete.
	events := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","status":"incomplete","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","arguments":"{}"}]}}`,
	}
	resp, items, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(events), nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 0 || len(resp.ResponsesOutput) != 0 || len(items) != 0 || resp.StopReason != "length" {
		t.Fatalf("response = %+v, want no executable calls and length", resp)
	}
}

func TestResponsesCompletedRecoversEachMissingCallExactlyOnce(t *testing.T) {
	events := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","arguments":"{\"value\":1}"}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"sample_tool"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"value\":0}"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","arguments":"{\"value\":1}"},{"type":"function_call","id":"fc_2","call_id":"call_2","name":"sample_tool","arguments":"{\"value\":2}"},{"type":"function_call","id":"fc_3","call_id":"call_3","name":"sample_tool","arguments":"{\"value\":3}"},{"type":"function_call","id":"fc_3","call_id":"call_3","name":"sample_tool","arguments":"{\"value\":3}"}]}}`,
	}
	starts, ends := map[string]int{}, map[string]int{}
	resp, items, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(events), func(delta message.StreamDelta) {
		if delta.ToolCall == nil {
			return
		}
		switch delta.Type {
		case message.StreamDeltaToolUseStart:
			starts[delta.ToolCall.ID]++
		case message.StreamDeltaToolUseEnd:
			ends[delta.ToolCall.ID]++
		}
	}, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 3 || resp.StopReason != "tool_calls" {
		t.Fatalf("response = %+v, want 3 calls", resp)
	}
	if len(items) != 3 || len(resp.ResponsesOutput) != 3 {
		t.Fatalf("replay calls not deduplicated: items=%+v output=%+v", items, resp.ResponsesOutput)
	}
	for i, call := range resp.ToolCalls {
		var args struct{ Value int }
		if err := json.Unmarshal(call.Args, &args); err != nil || args.Value != i+1 {
			t.Fatalf("call %d = %+v, err=%v", i, call, err)
		}
		if starts[call.ID] != 1 || ends[call.ID] != 1 {
			t.Fatalf("call %s starts=%d ends=%d, want 1 each", call.ID, starts[call.ID], ends[call.ID])
		}
	}
}

func TestResponsesCompletedSettlesCustomInputFromTerminal(t *testing.T) {
	for _, terminalInput := range []string{"", `,"input":"*** Begin Patch\n*** End Patch"`} {
		events := []string{
			`{"type":"response.output_item.added","output_index":2,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch"}}`,
			`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":"*** Begin Patch\n*** End Patch"}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"custom_tool_call","id":"ct_1","name":"apply_patch"` + terminalInput + `}]}}`,
		}
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(events), nil, nil, nil, "", true, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.ToolCalls) != 1 || string(resp.ToolCalls[0].Args) != `{"patch":"*** Begin Patch\n*** End Patch"}` {
			t.Fatalf("ToolCalls = %+v", resp.ToolCalls)
		}
		if len(resp.ResponsesOutput) != 1 || resp.ResponsesOutput[0].Arguments != string(resp.ToolCalls[0].Args) {
			t.Fatalf("replay differs from execution: %+v", resp.ResponsesOutput)
		}
	}
}

func TestResponsesRejectsToolIdentityCollision(t *testing.T) {
	for _, event := range []string{
		// Missing output_index must not silently reuse index zero for another call.
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"sample_tool"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"sample_tool","arguments":"{}"}}`,
	} {
		events := []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
			event,
			`[DONE]`,
		}
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(events), nil, nil, nil, "", false, false, false)
		if err == nil || !strings.Contains(err.Error(), "conflicting Responses tool call identity") || resp != nil {
			t.Fatalf("resp=%+v err=%v, want identity conflict", resp, err)
		}
	}
}

func TestResponsesTerminalDoesNotRecoverUnfinishedOutputItem(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","status":"in_progress","arguments":"{}"}]}}`,
	})
	resp, items, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 0 || resp.StopReason != "length" {
		t.Fatalf("response = %+v", resp)
	}
	if len(items) != 0 || len(resp.ResponsesOutput) != 0 {
		t.Fatalf("unfinished item remains in replay: items=%+v output=%+v", items, resp.ResponsesOutput)
	}
}

func TestResponsesCompletedPreservesFunctionArgsWhenTerminalOmitsThem(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","name":"sample_tool"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"value\":1}"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}]}}`,
	})
	resp, items, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || string(resp.ToolCalls[0].Args) != `{"value":1}` {
		t.Fatalf("ToolCalls = %+v", resp.ToolCalls)
	}
	if len(items) != 1 || items[0].Arguments != string(resp.ToolCalls[0].Args) ||
		len(resp.ResponsesOutput) != 1 || resp.ResponsesOutput[0].Arguments != items[0].Arguments {
		t.Fatalf("replay differs from execution: items=%+v output=%+v", items, resp.ResponsesOutput)
	}
}

func TestResponsesDuplicateCustomItemDoneDoesNotRepeatCall(t *testing.T) {
	done := `{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"}}`
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream([]string{done, done, `[DONE]`}), nil, nil, nil, "", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v, want exactly one", resp.ToolCalls)
	}
}

func TestResponsesUnfinishedCallKeepsCompletedCall(t *testing.T) {
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","arguments":"{}"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"sample_tool"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{}"}`,
		`[DONE]`,
	}), nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" || resp.StopReason != "length" {
		t.Fatalf("response = %+v, want only completed call", resp)
	}
}

func TestCodexWSToolCompletionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal string
		want     int
	}{
		{"unfinished", `{"type":"response.completed","response":{"status":"completed","output":[]}}`, 0},
		{"terminal recovery", `{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","arguments":"{}"}]}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
				for _, event := range []string{
					`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool"}}`,
					`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
					tc.terminal,
				} {
					if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
						return
					}
				}
			}))
			defer srv.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses}, nil)
			r := &ResponsesProvider{provider: provider, codexWSConn: conn}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, _, err := r.codexWSExecuteRequestLocked(ctx, "test-key", "sample/test-model", codexWSResponseCreate{
				Type: "response.create", Model: "sample/test-model", Stream: true,
			}, nil, false, time.Now(), true, nil, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.ToolCalls) != tc.want || (tc.want == 0 && resp.StopReason != "length") {
				t.Fatalf("response = %+v, want %d calls", resp, tc.want)
			}
		})
	}
}

func TestResponsesTerminalPreservesToolOrderAcrossMissingEvents(t *testing.T) {
	for _, finished := range []bool{false, true} {
		for _, missing := range []int{0, 1} {
			t.Run(fmt.Sprintf("finished=%t/missing=%d", finished, missing), func(t *testing.T) {
				var events []string
				var output []map[string]any
				for i := range 3 {
					item := map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", i), "call_id": fmt.Sprintf("call_%d", i), "name": "sample_tool", "arguments": "{}"}
					output = append(output, item)
					if i == missing {
						continue
					}
					types := []string{"response.output_item.added"}
					if finished {
						types = append(types, "response.output_item.done")
					}
					for _, eventType := range types {
						raw, err := json.Marshal(map[string]any{"type": eventType, "output_index": i, "item": item})
						if err != nil {
							t.Fatal(err)
						}
						events = append(events, string(raw))
					}
				}
				// Duplicate terminal items must not duplicate execution.
				output = append(output, output[missing])
				raw, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}})
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, string(raw))
				resp, items, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(events), nil, nil, nil, "", false, false, false)
				if err != nil {
					t.Fatal(err)
				}
				if len(resp.ToolCalls) != 3 || len(items) != 3 {
					t.Fatalf("calls=%+v replay=%+v", resp.ToolCalls, items)
				}
				for i, call := range resp.ToolCalls {
					if want := fmt.Sprintf("call_%d", i); call.ID != want || items[i].CallID != want {
						t.Fatalf("position %d: execution=%s replay=%s, want %s", i, call.ID, items[i].CallID, want)
					}
				}
			})
		}
	}
}
