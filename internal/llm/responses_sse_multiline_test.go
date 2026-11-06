package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func responsesCompletedSSE(tb testing.TB, parts int, multiline bool) string {
	tb.Helper()
	content := make([]map[string]string, parts)
	for i := range content {
		content[i] = map[string]string{"type": "output_text", "text": "sample"}
	}
	payload := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_sample", "status": "completed",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant", "content": content,
			}},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		tb.Fatal(err)
	}
	if multiline {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, data, "", " "); err != nil {
			tb.Fatal(err)
		}
		data = pretty.Bytes()
	}
	return "data: " + strings.ReplaceAll(string(data), "\n", "\ndata: ") + "\n\n"
}

func TestResponsesSSEMultilineCompletion(t *testing.T) {
	const parts = 128
	stream := responsesCompletedSSE(t, parts, true)
	for _, tc := range []struct {
		name, stream string
	}{
		{"delimiter", stream},
		{"EOF", strings.TrimSuffix(stream, "\n\n")},
		{"empty event", "data:\n\n" + stream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, items, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(tc.stream), nil, nil, nil, "", false, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if resp == nil || resp.Content != strings.Repeat("sample", parts) || resp.ProviderResponseID != "resp_sample" || resp.StopReason != "stop" {
				t.Fatalf("response = %+v, want complete text and stop", resp)
			}
			if len(items) != 1 || items[0].Type != "message" {
				t.Fatalf("output = %+v, want one message", items)
			}
		})
	}
}

func TestResponsesSSEMultilineCompletionRejectsTrailingData(t *testing.T) {
	stream := strings.TrimSuffix(responsesCompletedSSE(t, 1, true), "\n\n") + "\ndata: invalid\n\n"
	_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(stream), nil, nil, nil, "", false, false, false)
	if err == nil || !strings.Contains(err.Error(), "parse event:") {
		t.Fatalf("error = %v, want invalid complete event", err)
	}
}

func BenchmarkResponsesSSEMultiline(b *testing.B) {
	for _, count := range []int{128, 512, 2048} {
		for _, multiline := range []bool{false, true} {
			b.Run(fmt.Sprintf("parts=%d/multiline=%t", count, multiline), func(b *testing.B) {
				stream := responsesCompletedSSE(b, count, multiline)
				want := strings.Repeat("sample", count)
				b.ReportAllocs()
				b.SetBytes(int64(len(stream)))
				for b.Loop() {
					resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(stream), nil, nil, nil, "", false, false, false)
					if err != nil || resp == nil || resp.Content != want || resp.StopReason != "stop" {
						b.Fatalf("response=%+v err=%v", resp, err)
					}
				}
			})
		}
	}
}
