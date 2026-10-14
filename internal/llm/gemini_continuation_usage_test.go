package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/keakon/chord/internal/contextdiag"
)

func TestGeminiContinuationUsageCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name, stream  string
		input, output int
		known         bool
		state         string
	}{
		{"checkpoint_without_terminal_usage", continuationChunk("A", "T1", "", 100, 1) + continuationChunk("B", "", "STOP", -1, 0), 100, 1, false, contextdiag.Partial},
		{"missing_later_checkpoint_usage", continuationChunk("A", "T1", "", 100, 1) + continuationChunk("B", "T2", "", -1, 0) + continuationChunk("C", "", "STOP", -1, 0), 100, 1, false, contextdiag.Partial},
		{"checkpoint_then_empty_terminal_usage", continuationChunk("A", "T1", "", 100, 1) + `data: {"candidates":[{"content":{"parts":[{"text":"B"}]},"finishReason":"STOP"}],"usageMetadata":{}}` + "\n\n", 100, 1, false, contextdiag.Partial},
		{"checkpoint_then_partial_terminal_usage", continuationChunk("A", "T1", "", 100, 1) + `data: {"candidates":[{"content":{"parts":[{"text":"B"}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":2}}` + "\n\n", 100, 2, false, contextdiag.Partial},
		{"terminal_without_prompt", `data: {"candidates":[{"content":{"parts":[{"text":"A"}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":3}}` + "\n\n", 0, 3, false, contextdiag.Partial},
		{"terminal_without_output", `data: {"candidates":[{"content":{"parts":[{"text":"A"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100}}` + "\n\n", 100, 0, false, contextdiag.Partial},
		{"explicit_zero", continuationChunk("A", "", "STOP", 0, 0), 0, 0, true, contextdiag.Reported},
		{"thought_only_counter", `data: {"candidates":[{"content":{"parts":[{"text":"A","thought":true}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"thoughtsTokenCount":3}}` + "\n\n", 100, 3, true, contextdiag.Reported},
		{"complete_terminal", continuationChunk("A", "T1", "", 100, 1) + continuationChunk("B", "", "STOP", 100, 2), 100, 2, true, contextdiag.Reported},
		{"no_usage", continuationChunk("A", "", "STOP", -1, 0), 0, 0, false, contextdiag.Missing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, cfg := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.stream) })
			var receipts []ContinuationUsage
			ctx := context.WithValue(t.Context(), continuationObserverKey{}, func(u ContinuationUsage) { receipts = append(receipts, u) })
			collector, store := diagnosticTestCollector()
			ctx = context.WithValue(ctx, diagnosticsContextKey{}, collector.BeginRequest(1))
			resp, err := completeObservedProvider(ctx, p, cfg, "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, nil)
			if snapshot, ok := store.Latest(); !ok || snapshot.Usage.State != tc.state || snapshot.Usage.FullInput != int64(tc.input) || snapshot.Usage.Output != int64(tc.output) {
				t.Fatalf("diagnostics=%+v", snapshot)
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.Usage.InputTokens != tc.input || resp.Usage.OutputTokens != tc.output || resp.Usage.Context.Known != tc.known {
				t.Fatalf("usage=%+v context=%+v", resp.Usage, resp.Usage.Context)
			}
			if len(receipts) != 1 || receipts[0].UsageState != tc.state {
				t.Fatalf("receipts=%+v", receipts)
			}
			if receipts[0].Usage != nil && (receipts[0].Usage.InputTokens != tc.input || receipts[0].Usage.OutputTokens != tc.output) {
				t.Fatalf("receipt=%+v", receipts[0].Usage)
			}
		})
	}
}

func TestGeminiContinuationMissingCheckpointUsageAcrossHops(t *testing.T) {
	for _, finish := range []string{"", geminiContinuationFinish} {
		t.Run(finish, func(t *testing.T) {
			count := 0
			p, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
				count++
				if count == 1 {
					fmt.Fprint(w, continuationChunk("A", "T1", "", 100, 1)+continuationChunk("B", "T2", finish, -1, 0))
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["continuationToken"] != "T2" {
					t.Error("lost latest checkpoint")
				}
				fmt.Fprint(w, continuationChunk("C", "", "STOP", 102, 1))
			})
			var receipts []ContinuationUsage
			ctx := context.WithValue(t.Context(), continuationObserverKey{}, func(u ContinuationUsage) { receipts = append(receipts, u) })
			resp, err := p.runGeminiContinuation(ctx, freezeGeminiRequest(p, "model-alias", "test-key", []byte(`{}`)), nil, geminiRecoveryPolicy{jitter: func(time.Duration) time.Duration { return 0 }, wait: func(context.Context, time.Duration) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Content != "ABC" || resp.Usage.InputTokens != 202 || resp.Usage.OutputTokens != 2 || resp.Usage.Context.Known {
				t.Fatalf("resp=%+v usage=%+v", resp, resp.Usage)
			}
			if len(receipts) != 2 || receipts[0].UsageState != contextdiag.Partial || receipts[1].UsageState != contextdiag.Reported {
				t.Fatalf("receipts=%+v", receipts)
			}
		})
	}
}

func TestGeminiContinuationMissingInitialPromptRemainsUnknown(t *testing.T) {
	l := geminiUsageLedger{known: true}
	l.settle(&geminiUsageSnapshot{OutputTokens: 2, outputKnown: true}, true, true)
	l.settle(&geminiUsageSnapshot{InputTokens: 102, OutputTokens: 1, promptKnown: true, outputKnown: true}, false, true)
	if l.usage().Context.Known {
		t.Fatal("continuation prompt substituted for initial prompt")
	}
}

func TestGeminiContinuationUsagePreservesCachedCheckpointInput(t *testing.T) {
	p, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"A"}]},"continuationToken":"T1"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":60,"candidatesTokenCount":1}}`+"\n\n"+
			`data: {"candidates":[{"content":{"parts":[{"text":"B"}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":2}}`+"\n\n")
	})
	var receipt ContinuationUsage
	ctx := context.WithValue(t.Context(), continuationObserverKey{}, func(u ContinuationUsage) { receipt = u })
	resp, err := p.CompleteStream(ctx, "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 100 || resp.Usage.CacheReadTokens != 60 || resp.Usage.Context.Known || receipt.UsageState != contextdiag.Partial || receipt.Usage.CacheReadTokens != 60 {
		t.Fatalf("usage=%+v receipt=%+v", resp.Usage, receipt)
	}
}
