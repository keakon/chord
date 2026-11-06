package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// compactSSEField drops the optional space after the colon for one SSE field
// name. Per the SSE spec, "field: value" and "field:value" are equivalent.
func compactSSEField(stream, field string) string {
	prefix := field + ": "
	lines := strings.Split(stream, "\n")
	for i, line := range lines {
		if value, ok := strings.CutPrefix(line, prefix); ok {
			lines[i] = field + ":" + value
		}
	}
	return strings.Join(lines, "\n")
}

func TestParseSSEStreamAcceptsCompactFieldValues(t *testing.T) {
	full := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude","usage":{"input_tokens":100,"output_tokens":0}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_read","name":"read"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"README.md\"}"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	interrupted := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude","usage":{"input_tokens":1,"output_tokens":0}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		"",
	}, "\n")

	errored := strings.Join([]string{
		"event: error",
		`data: {"type":"error","error":{"type":"upstream_error","code":"upstream_connection_error","message":"Upstream response stream was interrupted"}}`,
		"",
	}, "\n")

	variants := []struct {
		name      string
		transform func(string) string
	}{
		{"spaced", func(s string) string { return s }},
		{"compact_event", func(s string) string { return compactSSEField(s, "event") }},
		{"compact_data", func(s string) string { return compactSSEField(s, "data") }},
		{"compact_both", func(s string) string {
			return compactSSEField(compactSSEField(s, "event"), "data")
		}},
		{"multiline", func(s string) string { return strings.ReplaceAll(s, "data: {", "data: {\ndata: ") }},
		{"bom_cr", func(s string) string { return "\xef\xbb\xbf" + strings.ReplaceAll(s, "\n", "\r") }},
	}

	cases := []struct {
		name       string
		stream     string
		wantDeltas []message.StreamDelta
		check      func(*testing.T, *message.Response, error)
	}{
		{
			name:   "text_and_tool_use",
			stream: full,
			wantDeltas: []message.StreamDelta{
				{Type: message.StreamDeltaText, Text: "hello"},
				{
					Type:     message.StreamDeltaToolUseStart,
					ToolCall: &message.ToolCallDelta{ID: "toolu_read", Name: "read"},
				},
				{
					Type:     message.StreamDeltaToolUseDelta,
					ToolCall: &message.ToolCallDelta{ID: "toolu_read", Name: "read", Input: `{"path":"README.md"}`},
				},
				{
					Type:     message.StreamDeltaToolUseEnd,
					ToolCall: &message.ToolCallDelta{ID: "toolu_read", Name: "read"},
				},
			},
			check: func(t *testing.T, resp *message.Response, err error) {
				if err != nil {
					t.Fatalf("parseSSEStream: %v", err)
				}
				if resp == nil || resp.Content != "hello" {
					t.Fatalf("resp = %#v, want text content hello", resp)
				}
				if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read" || resp.ToolCalls[0].ID != "toolu_read" {
					t.Fatalf("ToolCalls = %+v, want one read call", resp.ToolCalls)
				}
				if got := string(resp.ToolCalls[0].Args); got != `{"path":"README.md"}` {
					t.Fatalf("tool args = %s, want README path", got)
				}
				if resp.StopReason != "tool_use" {
					t.Fatalf("StopReason = %q, want tool_use", resp.StopReason)
				}
				if resp.Usage == nil || resp.Usage.InputTokens != 100 || resp.Usage.OutputTokens != 5 {
					t.Fatalf("Usage = %+v, want input tokens 100 and output tokens 5", resp.Usage)
				}
			},
		},
		{
			name:   "interrupted_without_message_stop",
			stream: interrupted,
			wantDeltas: []message.StreamDelta{
				{Type: message.StreamDeltaText, Text: "hello"},
			},
			check: func(t *testing.T, resp *message.Response, err error) {
				if err != nil {
					t.Fatalf("parseSSEStream: %v", err)
				}
				if resp == nil || resp.Content != "hello" || resp.StopReason != "interrupted" {
					t.Fatalf("resp = %#v, want interrupted partial text", resp)
				}
			},
		},
		{
			name:   "error_event",
			stream: errored,
			check: func(t *testing.T, _ *message.Response, err error) {
				apiErr, ok := errors.AsType[*APIError](err)
				if !ok || apiErr.Code != "upstream_connection_error" {
					t.Fatalf("err = %T %v, want provider API error", err, err)
				}
			},
		},
	}

	for _, tc := range cases {
		for _, variant := range variants {
			t.Run(tc.name+"/"+variant.name, func(t *testing.T) {
				var deltas []message.StreamDelta
				resp, err := parseSSEStream(strings.NewReader(variant.transform(tc.stream)), func(delta message.StreamDelta) {
					if delta.Type == message.StreamDeltaStatus || delta.Progress != nil {
						return
					}
					deltas = append(deltas, delta)
				}, nil, false, false)
				tc.check(t, resp, err)
				if len(deltas) != len(tc.wantDeltas) {
					t.Fatalf("got %d content deltas, want %d: %#v", len(deltas), len(tc.wantDeltas), deltas)
				}
				for i, want := range tc.wantDeltas {
					got := deltas[i]
					if got.Type != want.Type || got.Text != want.Text || got.Err != nil {
						t.Fatalf("content delta %d = %+v, want %+v", i, got, want)
					}
					if (got.ToolCall == nil) != (want.ToolCall == nil) ||
						(got.ToolCall != nil && *got.ToolCall != *want.ToolCall) {
						t.Fatalf("tool delta %d = %+v, want %+v", i, got.ToolCall, want.ToolCall)
					}
				}
			})
		}
	}
}
