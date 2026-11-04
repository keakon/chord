package llm

import (
	"bytes"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

type retainedResponsesResult struct {
	response  *message.Response
	items     []responsesInputItem
	callbacks []message.StreamDelta
	err       error
}

func parseRetainedResponses(body []byte) retainedResponsesResult {
	var result retainedResponsesResult
	result.response, result.items, result.err = parseResponsesSSEWithOutputItemsAndTurnState(
		bytes.NewReader(body), func(delta message.StreamDelta) {
			if delta.ToolCall != nil || delta.ReasoningItem != nil {
				result.callbacks = append(result.callbacks, delta)
			}
		}, nil, nil, "", false, false, true,
	)
	return result
}

func TestResponsesSSEKeepsResultsWithoutRetainingPayloadBuffers(t *testing.T) {
	const output = `[{"type":"reasoning","id":"reasoning-1","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"Sample thought"}]},{"type":"message","id":"message-1","role":"assistant","content":[{"type":"output_text","text":"Sample response"}]},{"type":"function_call","id":"function-1","call_id":"call-1","name":"read","arguments":"{\"path\":\"sample.go\"}"}]`
	const text = `{"type":"response.output_text.delta","output_index":0,"delta":"Sample response"}`
	const added = `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"function-1","call_id":"call-1","name":"read"}}`
	const done = `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"function-1","call_id":"call-1","name":"read","arguments":"{}"}}`
	const completed = `{"type":"response.completed","response":{"id":"response-1","output":[]}}`
	for _, tc := range []struct {
		name    string
		before  []string
		payload string
		after   []string
		wantErr bool
	}{
		{name: "completed", payload: `{"type":"response.completed","response":{"id":"response-1","output":` + output + `}}`},
		{name: "incomplete", payload: `{"type":"response.incomplete","response":{"id":"response-1","incomplete_details":{"reason":"max_output_tokens"},"output":` + output + `}}`},
		{name: "item_added", payload: added, after: []string{done}},
		{name: "item_done_before_interruption", payload: `{"type":"response.output_item.done","item":{"type":"reasoning","id":"reasoning-1","encrypted_content":"opaque"}}`, after: []string{text}},
		{name: "text_done", before: []string{text}, payload: `{"type":"response.output_text.done","text":"Sample response"}`},
		{name: "summary_done", payload: `{"type":"response.reasoning_summary_text.done","text":"Sample thought"}`, after: []string{text, completed}},
		{name: "reasoning_done", payload: `{"type":"response.reasoning_text.done","text":"Sample thought"}`, after: []string{text, completed}},
		{name: "single_reasoning_delta", payload: `{"type":"response.reasoning_text.delta","delta":"Sample thought"}`, after: []string{text}},
		{name: "failed", payload: `{"type":"response.failed","response":{"error":{"type":"server_error","code":"sample_error","message":"Sample failure"}}}`, wantErr: true},
		{name: "error", payload: `{"type":"error","error":{"type":"server_error","code":"sample_error","message":"Sample failure"}}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := func(padding int) []byte {
				events := append([]string(nil), tc.before...)
				payload := tc.payload
				if padding > 0 {
					payload = strings.TrimSuffix(payload, "}") + `,"unused":"` + strings.Repeat("x", padding) + `"}`
				}
				events = append(events, payload)
				events = append(events, tc.after...)
				return buildResponsesSSEBody(events)
			}
			want := parseRetainedResponses(body(0))
			if (want.err != nil) != tc.wantErr {
				t.Fatalf("unexpected fixture parse error: %v", want.err)
			}
			if !tc.wantErr && want.response == nil {
				t.Fatal("fixture produced no response")
			}
			// Warm decoder programs and buffers before measuring retained output.
			// The large unconsumed field must disappear even while replay items,
			// callbacks and errors from the stream remain reachable.
			large := body(1 << 20)
			parseRetainedResponses(large)
			runtime.GC()
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			results := make([]retainedResponsesResult, 6)
			for i := range results {
				results[i] = parseRetainedResponses(large)
				got := &results[i]
				if !reflect.DeepEqual(got.response, want.response) || !reflect.DeepEqual(got.items, want.items) || len(got.callbacks) != len(want.callbacks) {
					t.Fatalf("padding changed parsed output: got %+v, want %+v", results[i], want)
				}
				for j, callback := range got.callbacks {
					expected := want.callbacks[j]
					if callback.Type != expected.Type || !reflect.DeepEqual(callback.ToolCall, expected.ToolCall) || !reflect.DeepEqual(callback.ReasoningItem, expected.ReasoningItem) {
						t.Fatalf("padding changed callback %d: got %+v, want %+v", j, callback, expected)
					}
				}
				if tc.wantErr {
					gotErr, ok := errors.AsType[*APIError](got.err)
					wantErr, wantOK := errors.AsType[*APIError](want.err)
					if !ok || !wantOK || *gotErr != *wantErr {
						t.Fatalf("padding changed API error: got %v, want %v", got.err, want.err)
					}
				} else if got.err != nil {
					t.Fatal(got.err)
				}
			}
			runtime.GC()
			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			t.Logf("retained heap growth for six small outputs: %d bytes", growth)
			if growth > 1<<20 {
				t.Errorf("small retained outputs pin oversized SSE buffers: heap growth = %d bytes", growth)
			}
			runtime.KeepAlive(results)
			runtime.KeepAlive(large)
		})
	}
}

func BenchmarkResponsesSSERetainedOutput(b *testing.B) {
	for _, terminal := range []bool{false, true} {
		name := "deltas"
		if terminal {
			name = "terminal"
		}
		b.Run(name, func(b *testing.B) {
			var events []string
			if terminal {
				events = []string{`{"type":"response.completed","response":{"id":"response-1","output":[{"type":"message","id":"message-1","role":"assistant","content":[{"type":"output_text","text":"Sample response"}]}],"unused":"` + strings.Repeat("x", 64<<10) + `"}}`}
			} else {
				for range 128 {
					events = append(events, `{"type":"response.output_text.delta","output_index":0,"delta":"Sample response "}`)
				}
			}
			body := buildResponsesSSEBody(events)
			b.ReportAllocs()
			for b.Loop() {
				result := parseRetainedResponses(body)
				if result.err != nil || result.response == nil {
					b.Fatalf("parse failed: %v", result.err)
				}
			}
		})
	}
}
