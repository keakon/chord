package llm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

// The decode sites migrated to encoding/json/v2 fall into three failure-path
// classes, and these tests pin each class:
//
//  1. explicit-error sites decode strictly: a duplicate member is a parse
//     error the caller sees (authorized tightening over v1 last-wins);
//  2. skip-class sites keep their skip semantics: a rejected event is
//     dropped with a log and the stream continues;
//  3. sites whose structs capture raw provider payloads (tool arguments,
//     hosted results, relay headers) decode with the v1 tolerance
//     (AllowDuplicateNames + AllowInvalidUTF8) so relay variants cannot kill
//     streams the v1 parser accepted.

func TestAnthropicSSEStrictDecodeRejectsDuplicateMembers(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"m1","id":"m1","role":"assistant","model":"sample/test-model","usage":{"input_tokens":1}}}`,
		"",
	}, "\n")

	_, err := parseSSEStream(strings.NewReader(stream), nil, nil, false, false)
	if err == nil {
		t.Fatal("parseSSEStream: want duplicate-member parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse message_start") || !strings.Contains(err.Error(), "duplicate object member name") {
		t.Fatalf("err = %v, want message_start duplicate-member parse error", err)
	}
}

func TestAnthropicSSEStrictDecodeRejectsDuplicateDeltaMembers(t *testing.T) {
	stream := strings.Join([]string{
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		"",
	}, "\n")

	_, err := parseSSEStream(strings.NewReader(stream), nil, nil, false, false)
	if err == nil || !strings.Contains(err.Error(), "parse content_block_delta") || !strings.Contains(err.Error(), "duplicate object member name") {
		t.Fatalf("err = %v, want content_block_delta duplicate-member parse error", err)
	}
}

// A tool_use content_block_start may carry its complete input object (some
// relays fill it instead of streaming deltas). The input is captured raw, so
// the event must keep the v1 tolerance for duplicate members inside it and
// the stream must survive.
func TestAnthropicSSELenientDecodeAcceptsDuplicateMembersInRawCaptures(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"m1","role":"assistant","model":"sample/test-model","usage":{"input_tokens":1}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"Read","input":{"path":"first","path":"last"}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"last\"}"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	resp, err := parseSSEStream(strings.NewReader(stream), nil, nil, false, false)
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if resp == nil || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "Read" {
		t.Fatalf("resp = %#v, want one Read tool call", resp)
	}
}

func TestResponsesSSEStrictDecodeRejectsDuplicateMembers(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_text.delta","output_index":0,"delta":"hello","delta":"hello"}`,
	})
	_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err == nil || !strings.Contains(err.Error(), "parse output_text.delta") || !strings.Contains(err.Error(), "duplicate object member name") {
		t.Fatalf("err = %v, want output_text.delta duplicate-member parse error", err)
	}
}

// A strict-rejected terminal payload fails the stream explicitly (same as any
// mid-stream parse failure) instead of hanging or silently degrading the
// response.
func TestResponsesSSEStrictDecodeRejectsDuplicateTerminalMembers(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[]},"response":{"id":"resp-1","status":"completed","output":[]}}`,
	})
	done := make(chan struct{})
	var err error
	go func() {
		defer close(done)
		_, _, err = parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("parse did not return after strict-rejected response.completed")
	}
	if err == nil || !strings.Contains(err.Error(), "parse completed") || !strings.Contains(err.Error(), "duplicate object member name") {
		t.Fatalf("err = %v, want completed duplicate-member parse error", err)
	}
}

// A rejected skip-class event is dropped with a log while the rest of the
// stream — text deltas and the terminal payload — keeps flowing.
func TestResponsesSSESkipClassDropsRejectedEventAndContinues(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.reasoning_text.delta","delta":"secret","delta":"secret"}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"hello"}`,
		`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":2}}}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp == nil || resp.Content != "hello" {
		t.Fatalf("resp = %#v, want content hello", resp)
	}
	if resp.ProviderResponseID != "resp-1" || resp.StopReason != "stop" {
		t.Fatalf("resp = %#v, want terminal payload adopted (id resp-1, stop)", resp)
	}
	if resp.Usage == nil || resp.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %#v, want terminal usage adopted", resp.Usage)
	}
}

// A done payload whose item arguments arrive as a raw object with duplicate
// members must be accepted (v1 tolerance for raw captures) and the call must
// finalize with the bytes verbatim.
func TestResponsesSSELenientDecodePreservesDuplicateMemberArgs(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"call_1","name":"Read"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"call_1","name":"Read","status":"completed","arguments":{"path":"first","path":"last"}}}`,
		`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2}}}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp == nil || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "Read" {
		t.Fatalf("resp = %#v, want one Read tool call", resp)
	}
	args := string(resp.ToolCalls[0].Args)
	if !strings.Contains(args, `"path":"first"`) || !strings.Contains(args, `"path":"last"`) {
		t.Fatalf("args = %s, want duplicate members preserved verbatim", args)
	}
}

// A stream that ends without response.completed must still settle at EOF:
// the response is returned with a backfilled stop reason rather than hanging
// or being dropped.
func TestResponsesSSEEOFSettlesWithoutTerminalEvent(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"call_1","name":"Read"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"README.md\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"call_1","name":"Read","status":"completed"}}`,
	})
	done := make(chan struct{})
	var (
		resp *message.Response
		err  error
	)
	go func() {
		defer close(done)
		resp, _, err = parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("parse did not return at EOF without response.completed")
	}
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp == nil || len(resp.ToolCalls) != 1 {
		t.Fatalf("resp = %#v, want the accumulated tool call", resp)
	}
	if resp.StopReason != "tool_calls" {
		t.Fatalf("StopReason = %q, want backfilled tool_calls", resp.StopReason)
	}
}

// The chat-completions chunk captures function arguments and Claude-replay
// thinking blocks as raw JSON; relay variants that send object-form
// arguments with duplicate members must not kill the stream.
func TestOpenAISSELenientDecodePreservesDuplicateMemberArgs(t *testing.T) {
	// json.Marshal embeds the RawMessage verbatim (duplicate members and
	// all), the same way the persistence layer stores provider args.
	chunk := map[string]any{
		"id":    "c1",
		"model": "sample/test-model",
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{
						{
							"index": 0,
							"id":    "call_1",
							"type":  "function",
							"function": map[string]any{
								"name":      "Read",
								"arguments": json.RawMessage(`{"path":"first","path":"last"}`),
							},
						},
					},
				},
			},
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "tool_calls",
			},
		},
	}
	wire, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("build chunk: %v", err)
	}
	stream := "data: " + string(wire) + "\n\ndata: [DONE]\n\n"

	resp, err := parseOpenAISSEStreamOptions(strings.NewReader(stream), nil, nil, false)
	if err != nil {
		t.Fatalf("parseOpenAISSEStreamOptions: %v", err)
	}
	if resp == nil || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "Read" {
		t.Fatalf("resp = %#v, want one Read tool call", resp)
	}
	args := string(resp.ToolCalls[0].Args)
	if !strings.Contains(args, `"path":"first"`) || !strings.Contains(args, `"path":"last"`) {
		t.Fatalf("args = %s, want duplicate members preserved verbatim", args)
	}
}

// The Gemini chunk carries functionCall.args as a raw capture: duplicate
// members and invalid UTF-8 must be kept verbatim instead of failing the
// chunk.
func TestGeminiChunkLenientDecodePreservesRawArgs(t *testing.T) {
	// Byte concatenation keeps the 0xFF a raw invalid byte instead of a Go
	// escape sequence; json.Marshal embeds the RawMessage verbatim.
	dupKeyArgs := json.RawMessage(`{"path":"first","path":"last","note":"x` + "\xff" + `y"}`)
	chunk := map[string]any{
		"candidates": []map[string]any{
			{
				"content": map[string]any{
					"parts": []map[string]any{
						{
							"functionCall": map[string]any{
								"name": "Read",
								"args": dupKeyArgs,
							},
						},
					},
				},
			},
		},
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("build chunk: %v", err)
	}

	decoded, err := decodeGeminiStreamChunk(data)
	if err != nil {
		t.Fatalf("decodeGeminiStreamChunk: %v", err)
	}
	if len(decoded.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want 1", decoded.Candidates)
	}
	parts := decoded.Candidates[0].Content.Parts
	if len(parts) != 1 || parts[0].FunctionCall == nil || parts[0].FunctionCall.Name != "Read" {
		t.Fatalf("parts = %#v, want one Read functionCall", parts)
	}
	args := string(parts[0].FunctionCall.Args)
	if !strings.Contains(args, `"path":"first"`) || !strings.Contains(args, `"path":"last"`) || !strings.Contains(args, "x\xffy") {
		t.Fatalf("args = %s, want raw args preserved verbatim", args)
	}
}

// The terminal-event early-flush hint decodes strictly: a duplicate member
// anywhere in the line means the hint is not taken (the blank-line/EOF
// delimiter still finalizes the event), and a clean terminal line keeps the
// hint.
func TestSSEDataLineTerminatesEventStrictHint(t *testing.T) {
	terminal := map[string]struct{}{"response.completed": {}}
	if !sseDataLineTerminatesEvent([]byte(`{"type":"response.completed","response":{"id":"r1"}}`), "", terminal) {
		t.Fatal("clean terminal line should flush early")
	}
	if sseDataLineTerminatesEvent([]byte(`{"type":"response.completed","response":{"id":"r1","id":"r1"}}`), "", terminal) {
		t.Fatal("duplicate-member terminal line must not take the early-flush hint")
	}
	if sseDataLineTerminatesEvent([]byte(`{"TYPE":"response.completed"}`), "", terminal) {
		t.Fatal("case-mismatched field must not take the early-flush hint under v2 field matching")
	}
}
