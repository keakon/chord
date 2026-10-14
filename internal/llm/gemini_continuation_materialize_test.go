package llm

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestGeminiContinuationMaterializePreservesReplayBoundaries(t *testing.T) {
	parts := []geminiPart{{Text: "a", Thought: true}, {Text: "b", Thought: true}, {Thought: true, ThoughtSignature: "s1"}, {Text: "c", Thought: true, ThoughtSignature: "s2"}, {Text: "d"}, {Thought: true}, {Text: "e"}, {ThoughtSignature: "s3"}, {Text: "f"}}
	state := geminiDecodeState{parts: parts, finish: "STOP"}
	got, err := state.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	want := &message.Response{}
	for _, part := range parts {
		appendGeminiResponseTextPart(want, part)
	}
	if !reflect.DeepEqual(got.GeminiParts, want.GeminiParts) || got.Content != "def" || got.ReasoningContent != "abc" {
		t.Fatalf("response=%+v", got)
	}
}

func TestGeminiContinuationMaterializeToolSeparatesText(t *testing.T) {
	var chunk geminiStreamChunk
	err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[{"text":"a"},{"functionCall":{"name":"sample_tool","args":{}},"thoughtSignature":"s1"},{"text":"b"},{"text":"c","thoughtSignature":"s2"}]},"finishReason":"STOP"}]}`), &chunk)
	if err != nil {
		t.Fatal(err)
	}
	state := geminiDecodeState{}
	if err := state.Consume(chunk); err != nil {
		t.Fatal(err)
	}
	resp, err := state.Materialize()
	if err != nil || len(resp.GeminiParts) != 3 || len(resp.ToolCalls) != 1 {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if resp.GeminiParts[0].Text != "a" || resp.GeminiParts[1].ToolCallID != resp.ToolCalls[0].ID || resp.GeminiParts[2].Text != "bc" || resp.GeminiParts[2].ThoughtSignature != "s2" {
		t.Fatalf("parts=%+v", resp.GeminiParts)
	}
}

func BenchmarkGeminiContinuationMaterialize(b *testing.B) {
	for _, n := range []int{1024, 4096, 8192} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			parts := make([]geminiPart, n)
			for i := range parts {
				parts[i] = geminiPart{Text: strings.Repeat("x", 256)}
			}
			state := geminiDecodeState{parts: parts, finish: "STOP"}
			b.ReportAllocs()
			b.SetBytes(int64(n * 256))
			b.ResetTimer()
			for b.Loop() {
				resp, err := state.Materialize()
				if err != nil || len(resp.Content) != n*256 {
					b.Fatal("invalid output")
				}
			}
		})
	}
}
