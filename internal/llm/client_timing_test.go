package llm

import (
	"context"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestDeltaCarriesContent(t *testing.T) {
	cases := []struct {
		name  string
		delta message.StreamDelta
		want  bool
	}{
		{"blank text", message.StreamDelta{Type: message.StreamDeltaText, Text: "  "}, false},
		{"zero width text", message.StreamDelta{Type: message.StreamDeltaText, Text: "\u200b"}, false},
		{"format characters", message.StreamDelta{Type: message.StreamDeltaText, Text: " \t\n\u200c\u200d\u2060\ufeff"}, false},
		{"body text", message.StreamDelta{Type: message.StreamDeltaText, Text: "hello"}, true},
		{"text after placeholder", message.StreamDelta{Type: message.StreamDeltaText, Text: "\u200bhello"}, true},
		{"CJK text", message.StreamDelta{Type: message.StreamDeltaText, Text: "你好"}, true},
		{"joined emoji", message.StreamDelta{Type: message.StreamDeltaText, Text: "👩‍💻"}, true},
		{"hidden thinking", message.StreamDelta{Type: message.StreamDeltaThinking, ThinkingMode: message.StreamThinkingModeHidden, Text: "reasoning"}, false},
		{"zero width thinking", message.StreamDelta{Type: message.StreamDeltaThinking, Text: "\u200b"}, false},
		{"format characters in summary", message.StreamDelta{Type: message.StreamDeltaThinking, ThinkingMode: message.StreamThinkingModeSummary, Text: "\u200c\u200d\u2060\ufeff"}, false},
		{"summary thinking", message.StreamDelta{Type: message.StreamDeltaThinking, ThinkingMode: message.StreamThinkingModeSummary, Text: "summary"}, true},
		{"generic thinking", message.StreamDelta{Type: message.StreamDeltaThinking, Text: "thinking"}, true},
		{"tool start with name", message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{ID: "t1", Name: "shell"}}, true},
		{"tool start metadata only", message.StreamDelta{Type: message.StreamDeltaToolUseStart, ToolCall: &message.ToolCallDelta{}}, false},
		{"nil tool call", message.StreamDelta{Type: message.StreamDeltaToolUseStart}, false},
		{"tool args delta", message.StreamDelta{Type: message.StreamDeltaToolUseDelta, ToolCall: &message.ToolCallDelta{Input: `{"c`}}, true},
		{"tool end marker", message.StreamDelta{Type: message.StreamDeltaToolUseEnd, ToolCall: &message.ToolCallDelta{ID: "t1", Name: "shell"}}, false},
		{"status delta", message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: message.StatusDeltaStreaming}}, false},
	}
	for _, tc := range cases {
		if got := deltaCarriesContent(tc.delta); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestVisibleStreamTrackerFirstContent(t *testing.T) {
	tracker := &visibleStreamTracker{}
	tracker.Callback(message.StreamDelta{Type: message.StreamDeltaText, Text: "  "})
	if !tracker.FirstContentAt().IsZero() {
		t.Fatal("blank text must not start the content clock")
	}
	tracker.Callback(message.StreamDelta{Type: message.StreamDeltaThinking, ThinkingMode: message.StreamThinkingModeHidden, Text: "reasoning"})
	if !tracker.FirstContentAt().IsZero() {
		t.Fatal("hidden reasoning must not start the content clock")
	}
	tracker.Callback(message.StreamDelta{Type: message.StreamDeltaText, Text: "hi"})
	if tracker.FirstContentAt().IsZero() {
		t.Fatal("content delta must start the content clock")
	}
	tracker.Callback(message.StreamDelta{Type: message.StreamDeltaRollback, Rollback: &message.RollbackDelta{Reason: "retry"}})
	if !tracker.FirstContentAt().IsZero() {
		t.Fatal("rollback must reset the content clock")
	}
}

func TestClientFirstOutputTimingSkipsFormatCharacters(t *testing.T) {
	for _, field := range []string{"content", "reasoning_content"} {
		t.Run(field, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := NewProviderConfig("openai", config.ProviderConfig{
					Type:   config.ProviderTypeChatCompletions,
					APIURL: "https://example.invalid/v1/chat/completions",
					Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
				}, []string{"test-key"})
				provider := &OpenAIProvider{provider: cfg, client: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					time.Sleep(time.Second)
					body := &slowChunkReader{
						next: new(0),
						chunks: []struct {
							data []byte
							err  error
						}{
							{data: []byte(`data: {"choices":[{"index":0,"delta":{"` + field + `":"\u200b"}}]}` + "\n\n")},
							{data: []byte(`data: {"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\ndata: [DONE]\n\n")},
						},
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Request: req, Body: io.NopCloser(body)}, nil
				})}}
				client := NewClient(cfg, provider, "test-model", 1024, "")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				resp, err := client.CompleteStream(ctx, []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				// slowChunkReader waits 50ms between the placeholder and text.
				want := (time.Second + 50*time.Millisecond).Nanoseconds()
				if resp.FirstOutputNs != want || resp.RequestNs != want {
					t.Fatalf("timing first=%v request=%v, want both 1.05s", time.Duration(resp.FirstOutputNs), time.Duration(resp.RequestNs))
				}
			})
		})
	}
}

func TestRecordResponseTiming(t *testing.T) {
	start := time.Now()

	tracker := &visibleStreamTracker{}
	resp := &message.Response{}
	recordResponseTiming(resp, start, tracker)
	if resp.RequestNs <= 0 {
		t.Fatalf("RequestNs = %d, want positive", resp.RequestNs)
	}
	if resp.FirstOutputNs != 0 {
		t.Fatalf("FirstOutputNs = %d, want 0 without content", resp.FirstOutputNs)
	}

	contentAt := start.Add(250 * time.Millisecond)
	tracker.hasContent = true
	tracker.firstContentAt = contentAt
	resp = &message.Response{}
	recordResponseTiming(resp, start, tracker)
	if resp.FirstOutputNs != (250 * time.Millisecond).Nanoseconds() {
		t.Fatalf("FirstOutputNs = %d, want 250ms", resp.FirstOutputNs)
	}
	if resp.RequestNs <= 0 {
		t.Fatalf("RequestNs = %d, want positive", resp.RequestNs)
	}

	// A content timestamp before the start point (clock anomaly) must not
	// produce a negative latency.
	early := &visibleStreamTracker{hasContent: true, firstContentAt: start.Add(-time.Second)}
	resp = &message.Response{}
	recordResponseTiming(resp, start, early)
	if resp.FirstOutputNs != 0 {
		t.Fatalf("FirstOutputNs = %d, want 0 for pre-start content", resp.FirstOutputNs)
	}

	resp = &message.Response{}
	recordResponseTiming(nil, start, tracker)
	recordResponseTiming(resp, time.Time{}, tracker)
	if resp.RequestNs != 0 || resp.FirstOutputNs != 0 {
		t.Fatalf("zero start or nil resp must not stamp timing: %+v", resp)
	}
}

func TestContinuationHopTiming(t *testing.T) {
	hopNs := (2 * time.Second).Nanoseconds()
	latencyNs := (300 * time.Millisecond).Nanoseconds()

	completed := geminiHopResult{FinishReason: "STOP", Usage: &geminiUsageSnapshot{}, UsageComplete: true, RequestNs: hopNs, FirstOutputNs: latencyNs}
	if requestNs, latencyNsGot := continuationHopTiming(completed); requestNs != hopNs || latencyNsGot != latencyNs {
		t.Fatalf("completed hop timing = (%d, %d), want (%d, %d)", requestNs, latencyNsGot, hopNs, latencyNs)
	}

	incompleteUsage := completed
	incompleteUsage.UsageComplete = false
	if requestNs, latencyNsGot := continuationHopTiming(incompleteUsage); requestNs != 0 || latencyNsGot != latencyNs {
		t.Fatalf("incomplete usage timing = (%d, %d), want (0, %d)", requestNs, latencyNsGot, latencyNs)
	}

	noUsage := geminiHopResult{FinishReason: "STOP", RequestNs: hopNs, FirstOutputNs: latencyNs}
	if requestNs, latencyNsGot := continuationHopTiming(noUsage); requestNs != 0 || latencyNsGot != latencyNs {
		t.Fatalf("nil usage timing = (%d, %d), want (0, %d)", requestNs, latencyNsGot, latencyNs)
	}

	failed := completed
	failed.Err = io.ErrUnexpectedEOF
	if requestNs, latencyNsGot := continuationHopTiming(failed); requestNs != 0 || latencyNsGot != 0 {
		t.Fatalf("failed hop timing = (%d, %d), want (0, 0)", requestNs, latencyNsGot)
	}

	unfinished := completed
	unfinished.FinishReason = ""
	if requestNs, latencyNsGot := continuationHopTiming(unfinished); requestNs != 0 || latencyNsGot != 0 {
		t.Fatalf("unfinished hop timing = (%d, %d), want (0, 0)", requestNs, latencyNsGot)
	}
}
