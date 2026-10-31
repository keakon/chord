package llm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestGeminiTruncationPreservesTextAfterToolCall(t *testing.T) {
	for _, separateFinish := range []bool{false, true} {
		name := "same_chunk"
		if separateFinish {
			name = "separate_finish"
		}
		t.Run(name, func(t *testing.T) {
			chunk := `data: {"candidates":[{"content":{"parts":[{"text":"before"},{"functionCall":{"name":"sample_tool","args":{}}},{"text":"after"}]}`
			if separateFinish {
				chunk += `}]}`
				chunk += "\n" + `data: {"candidates":[{"finishReason":"MAX_TOKENS"}]}`
			} else {
				chunk += `,"finishReason":"MAX_TOKENS"}]}`
			}
			var toolEnds int
			resp, err := parseGeminiSSEStream(strings.NewReader(chunk+"\n"), func(delta message.StreamDelta) {
				if delta.Type == message.StreamDeltaToolUseEnd {
					toolEnds++
				}
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Content != "beforeafter" || resp.StopReason != "MAX_TOKENS" {
				t.Fatalf("response = %#v, want truncated text preserved", resp)
			}
			if len(resp.ToolCalls) != 0 || toolEnds != 0 {
				t.Fatalf("truncated tools survived: calls=%v end events=%d", resp.ToolCalls, toolEnds)
			}
			want := []message.GeminiReplayPart{{Type: "text", Text: "before"}, {Type: "text", Text: "after"}}
			if !reflect.DeepEqual(resp.GeminiParts, want) {
				t.Fatalf("replay = %#v, want %#v", resp.GeminiParts, want)
			}
		})
	}
}
