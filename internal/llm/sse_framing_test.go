package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestSSEFraming(t *testing.T) {
	anthropic := "event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}` + "\n\n" +
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n" +
		"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"
	openai := `data: {"choices":[{"delta":{"content":"hello"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	gemini := `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}` + "\n\n" +
		`data: {"candidates":[{"finishReason":"STOP"}]}` + "\n\n"
	responses := `data: {"type":"response.output_text.delta","output_index":0,"delta":"hello"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"id":"resp_sample","status":"completed","output":[]}}` + "\n\n"
	parsers := []struct {
		name, stream string
		parse        func(io.Reader) (*message.Response, error)
	}{
		{"anthropic", anthropic, func(r io.Reader) (*message.Response, error) {
			return parseSSEStream(r, nil, nil, false, false)
		}},
		{"openai", openai, func(r io.Reader) (*message.Response, error) {
			return parseOpenAISSEStreamOptions(r, nil, nil, false)
		}},
		{"gemini", gemini, func(r io.Reader) (*message.Response, error) {
			return parseGeminiSSEStream(r, nil, nil)
		}},
		{"responses", responses, func(r io.Reader) (*message.Response, error) {
			resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(r, nil, nil, nil, "", false, false, false)
			return resp, err
		}},
	}
	variants := []struct {
		name      string
		transform func(string) string
	}{
		{"standard", func(s string) string { return s }},
		{"compact", func(s string) string {
			return strings.ReplaceAll(strings.ReplaceAll(s, "data: ", "data:"), "event: ", "event:")
		}},
		{"crlf", func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }},
		{"multiline", func(s string) string { return strings.ReplaceAll(s, "data: {", "data: {\ndata: ") }},
		{"leading_json_space", func(s string) string { return strings.ReplaceAll(s, "data: {", "data:  {") }},
		{"bom", func(s string) string { return "\xef\xbb\xbf" + s }},
		{"cr", func(s string) string { return strings.ReplaceAll(s, "\n", "\r") }},
	}
	for _, p := range parsers {
		for _, v := range variants {
			t.Run(p.name+"/"+v.name, func(t *testing.T) {
				resp, err := p.parse(strings.NewReader(v.transform(p.stream)))
				if err != nil {
					t.Fatalf("valid SSE rejected: %v", err)
				}
				if resp == nil || resp.Content != "hello" {
					t.Fatalf("response = %+v; want content hello", resp)
				}
			})
		}
	}
	t.Run("anthropic_event_after_data", func(t *testing.T) {
		var frames []string
		for frame := range strings.SplitSeq(anthropic, "\n\n") {
			lines := strings.Split(frame, "\n")
			if len(lines) == 2 {
				frames = append(frames, lines[1]+"\n"+lines[0])
			}
		}
		resp, err := parseSSEStream(strings.NewReader(strings.Join(frames, "\n\n")+"\n\n"), nil, nil, false, false)
		if err != nil || resp == nil || resp.Content != "hello" || resp.StopReason != "end_turn" {
			t.Fatalf("response = %+v, err = %v; want normal hello response", resp, err)
		}
	})
}

func TestResponsesSSELeadingSpacePreservesPayloadType(t *testing.T) {
	stream := "event: response.output_text.delta\n" +
		`data:  {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"README.md\"}"}` +
		"\n\ndata: [DONE]\n\n"
	var text strings.Builder
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(stream), func(d message.StreamDelta) {
		if d.Type == message.StreamDeltaText {
			text.WriteString(d.Text)
		}
	}, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if text.Len() != 0 || (resp != nil && resp.Content != "") {
		t.Fatalf("tool arguments routed to text: callback=%q response=%+v", text.String(), resp)
	}
}

func TestGeminiContinuationSSEFraming(t *testing.T) {
	base := continuationChunk("hello", "", "STOP", 1, 1)
	for _, tc := range []struct{ name, stream string }{
		{"standard", base},
		{"multiline", strings.ReplaceAll(base, "data: {", "data: {\ndata: ")},
		{"bom_cr", "\xef\xbb\xbf: ping\r\r" + strings.ReplaceAll(base, "\n", "\r")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.stream)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			frozen := freezeGeminiRequest(provider, "model-alias", "test-key", []byte(`{"contents":[]}`))
			resp, err := provider.runGeminiContinuation(ctx, frozen, nil, geminiRecoveryPolicy{
				jitter: func(time.Duration) time.Duration { return 0 },
				wait:   func(context.Context, time.Duration) error { return nil },
			})
			if err != nil || resp == nil || resp.Content != "hello" {
				t.Fatalf("response=%+v, err=%v; want normal hello response", resp, err)
			}
		})
	}
}
