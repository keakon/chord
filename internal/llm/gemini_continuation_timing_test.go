package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/message"
)

func TestGeminiDecodeStateFirstContentStamping(t *testing.T) {
	newState := func() *geminiDecodeState { return &geminiDecodeState{cb: func(message.StreamDelta) {}} }
	t.Run("emit stamps only content-bearing deltas", func(t *testing.T) {
		s := newState()
		s.emit(message.StreamDelta{Type: message.StreamDeltaThinkingEnd})
		if !s.firstContentAt.IsZero() {
			t.Fatal("thinking end stamped first content")
		}
		s.emit(message.StreamDelta{Type: message.StreamDeltaText, Text: "   "})
		if !s.firstContentAt.IsZero() {
			t.Fatal("blank text stamped first content")
		}
		s.emit(message.StreamDelta{Type: message.StreamDeltaText, Text: "hi"})
		if s.firstContentAt.IsZero() {
			t.Fatal("text did not stamp first content")
		}
	})
	t.Run("buffered function calls are not visible output", func(t *testing.T) {
		s := newState()
		unnamed, err := decodeGeminiStreamChunk([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{}}]}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Consume(unnamed); err != nil {
			t.Fatal(err)
		}
		if !s.firstContentAt.IsZero() {
			t.Fatal("unnamed function call stamped first content")
		}
		named, err := decodeGeminiStreamChunk([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Consume(named); err != nil {
			t.Fatal(err)
		}
		if !s.firstContentAt.IsZero() {
			t.Fatal("buffered function call stamped first content before publication")
		}
	})
}

func TestGeminiDecodeStateFirstContentSkipsFormatCharacters(t *testing.T) {
	for _, thought := range []bool{false, true} {
		t.Run(fmt.Sprintf("thought=%t", thought), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state := &geminiDecodeState{}
				start := time.Now()
				for i, text := range []string{"\u200b", "hello"} {
					if i == 0 {
						time.Sleep(time.Second)
					} else {
						time.Sleep(50 * time.Millisecond)
					}
					chunk, err := decodeGeminiStreamChunk(fmt.Appendf(nil, `{"candidates":[{"content":{"parts":[{"text":%q,"thought":%t}]}}]}`, text, thought))
					if err != nil {
						t.Fatal(err)
					}
					if err := state.Consume(chunk); err != nil {
						t.Fatal(err)
					}
					if i == 0 && !state.firstContentAt.IsZero() {
						t.Fatal("format character started the content clock")
					}
				}
				if got := state.firstContentAt.Sub(start); got != time.Second+50*time.Millisecond {
					t.Fatalf("first output = %v, want 1.05s", got)
				}
			})
		})
	}
}

func TestGeminiContinuationRejectedEmptyResponseDropsSpeedTiming(t *testing.T) {
	p, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, continuationChunk("", "", "STOP", 100, 10))
	})
	var receipt ContinuationUsage
	ctx := context.WithValue(t.Context(), continuationObserverKey{}, func(u ContinuationUsage) { receipt = u })
	frozen := freezeGeminiRequest(p, "model-alias", "test-key", []byte(`{"contents":[],"generationConfig":{"maxOutputTokens":131072}}`))
	response, err := p.runGeminiContinuation(ctx, frozen, func(message.StreamDelta) {}, geminiRecoveryPolicy{jitter: func(time.Duration) time.Duration { return 0 }, wait: func(context.Context, time.Duration) error { return nil }})
	if err == nil {
		t.Fatal("empty response accepted")
	}
	if _, ok := errors.AsType[*EmptyResponseError](err); !ok {
		t.Fatalf("unexpected error: %v", err)
	}
	// The billing receipt is kept for the rejected response, but without
	// speed timing so the failed attempt never contributes a sample.
	if receipt.Outcome != "completed" {
		t.Fatalf("outcome = %q, want completed", receipt.Outcome)
	}
	if receipt.Usage == nil || receipt.Usage.InputTokens != 100 || receipt.Usage.OutputTokens != 10 {
		t.Fatalf("usage = %+v, want billing preserved", receipt.Usage)
	}
	if receipt.RequestNs != 0 || receipt.FirstOutputNs != 0 {
		t.Fatalf("rejected response carried speed timing: request=%d first=%d", receipt.RequestNs, receipt.FirstOutputNs)
	}
	if !response.UsageReported {
		t.Fatal("response not marked usage reported")
	}
}

func TestGeminiContinuationToolOnlyResponseCarriesFirstOutput(t *testing.T) {
	p, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":5}}`+"\n\n")
	})
	var receipt ContinuationUsage
	var toolStarts int
	ctx := context.WithValue(t.Context(), continuationObserverKey{}, func(u ContinuationUsage) { receipt = u })
	frozen := freezeGeminiRequest(p, "model-alias", "test-key", []byte(`{"contents":[],"generationConfig":{"maxOutputTokens":131072}}`))
	response, err := p.runGeminiContinuation(ctx, frozen, func(d message.StreamDelta) {
		if d.Type == message.StreamDeltaToolUseStart {
			toolStarts++
		}
	}, geminiRecoveryPolicy{jitter: func(time.Duration) time.Duration { return 0 }, wait: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || toolStarts != 1 {
		t.Fatalf("tool calls=%d tool starts=%d, want 1/1", len(response.ToolCalls), toolStarts)
	}
	if receipt.Outcome != "completed" {
		t.Fatalf("outcome = %q, want completed", receipt.Outcome)
	}
	if receipt.RequestNs <= 0 || receipt.FirstOutputNs <= 0 {
		t.Fatalf("timing request=%d first=%d, want both positive", receipt.RequestNs, receipt.FirstOutputNs)
	}
	if response.ToolCalls[0].Name != "lookup" {
		t.Fatalf("tool call name = %q", response.ToolCalls[0].Name)
	}
}

func TestGeminiContinuationTimingMatchesVisiblePublication(t *testing.T) {
	for _, tc := range []struct {
		name      string
		continued bool
		text      string
	}{
		{name: "tool only"},
		{name: "text before final tools", text: "answer"},
		{name: "tool buffered across hops", continued: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const delay = 50 * time.Millisecond
			var request int
			p, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
				request++
				if request == 1 {
					fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}]}`+"\n\n")
					w.(http.Flusher).Flush()
					if tc.continued {
						fmt.Fprint(w, continuationChunk("", "checkpoint", geminiContinuationFinish, 100, 5))
						return
					}
				}
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-r.Context().Done():
					return
				case <-timer.C:
				}
				fmt.Fprint(w, continuationChunk(tc.text, "", "STOP", 100, 5))
			})
			var receipts []ContinuationUsage
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, continuationObserverKey{}, func(u ContinuationUsage) {
				receipts = append(receipts, u)
			})
			var firstVisible time.Time
			response, err := p.CompleteStream(ctx, "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, func(d message.StreamDelta) {
				if firstVisible.IsZero() && deltaCarriesContent(d) {
					firstVisible = time.Now()
				}
			})
			if err != nil || firstVisible.IsZero() || len(response.ToolCalls) != 1 {
				t.Fatalf("response=%+v first=%v err=%v", response, firstVisible, err)
			}
			wantReceipts := 1
			if tc.continued {
				wantReceipts++
			}
			if len(receipts) != wantReceipts {
				t.Fatalf("receipts=%d, want %d", len(receipts), wantReceipts)
			}
			last := receipts[len(receipts)-1]
			if time.Duration(last.FirstOutputNs) < delay || last.FirstOutputNs > last.RequestNs {
				t.Fatalf("first output=%v request=%v, want latency to include publication wait", time.Duration(last.FirstOutputNs), time.Duration(last.RequestNs))
			}
			if tc.continued && receipts[0].FirstOutputNs != 0 {
				t.Fatalf("buffered hop reported visible output: %+v", receipts[0])
			}
		})
	}
}

func TestGeminiContinuationCancelledDuringPublicationKeepsBillingOnly(t *testing.T) {
	p, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":5}}`+"\n\n")
	})
	var receipt ContinuationUsage
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = context.WithValue(ctx, continuationObserverKey{}, func(u ContinuationUsage) { receipt = u })
	_, err := p.CompleteStream(ctx, "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, func(d message.StreamDelta) {
		if d.Type == message.StreamDeltaToolUseStart {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want cancellation", err)
	}
	if receipt.Usage == nil || receipt.Usage.OutputTokens != 5 || receipt.RequestNs != 0 || receipt.FirstOutputNs != 0 {
		t.Fatalf("cancelled publication receipt=%+v, want billing without timing", receipt)
	}
}

func TestGeminiTerminalDrainWithoutCompleteUsageDropsThroughput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		usage      string
		usageState string
		output     int
	}{
		{name: "missing", usageState: contextdiag.Missing},
		{name: "partial", usage: `,"usageMetadata":{"candidatesTokenCount":5}`, usageState: contextdiag.Partial, output: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := NewProviderConfig("provider", config.ProviderConfig{
					Type:   config.ProviderTypeGenerateContent,
					APIURL: "https://generativelanguage.googleapis.com/v1beta",
					Models: map[string]config.ModelConfig{"test-model": {
						Compat: &config.ModelCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(true)}},
						Limit:  config.ModelLimit{Context: 8192, Output: 1024},
					}},
				}, []string{"test-key"})
				provider, err := NewGeminiProviderWithClient(cfg, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					time.Sleep(100 * time.Millisecond)
					reader, writer := io.Pipe()
					go func() {
						defer writer.Close()
						_, _ = io.WriteString(writer, `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}]`+tc.usage+"}\n\n")
						<-req.Context().Done()
						_ = writer.CloseWithError(req.Context().Err())
					}()
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req, Body: reader}, nil
				})}, "")
				if err != nil {
					t.Fatal(err)
				}
				client := NewClient(cfg, provider, "test-model", 1024, "")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				var receipts []ContinuationUsage
				start := time.Now()
				resp, err := client.CompleteStreamWithOptions(ctx, []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, func(message.StreamDelta) {}, CompleteStreamOptions{
					OnContinuationUsage: func(receipt ContinuationUsage) { receipts = append(receipts, receipt) },
				})
				if err != nil {
					t.Fatal(err)
				}
				if resp.Content != "hello" || !resp.UsageReported || resp.Usage.OutputTokens != tc.output {
					t.Fatalf("response = %+v, want completed text and only confirmed token counts", resp)
				}
				if elapsed := time.Since(start); elapsed != 100*time.Millisecond+TerminalDrainChunkTimeout {
					t.Fatalf("elapsed = %v, want return after terminal drain", elapsed)
				}
				if len(receipts) != 1 || receipts[0].UsageState != tc.usageState || receipts[0].RequestNs != 0 {
					t.Fatalf("receipts = %+v, want %s usage without throughput timing", receipts, tc.usageState)
				}
				if receipts[0].FirstOutputNs != int64(100*time.Millisecond) {
					t.Fatalf("first output = %v, want measured 100ms latency", time.Duration(receipts[0].FirstOutputNs))
				}
			})
		})
	}
}
