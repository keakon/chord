package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
)

func continuationChunk(text, token, finish string, input, output int) string {
	value := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": text}}}, "continuationToken": token, "finishReason": finish}}}
	if input >= 0 {
		value["usageMetadata"] = map[string]any{"promptTokenCount": input, "candidatesTokenCount": output}
	}
	data, _ := json.Marshal(value)
	return "data: " + string(data) + "\n\n"
}
func continuationProvider(t *testing.T, handler http.HandlerFunc) (*GeminiProvider, *ProviderConfig) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: server.URL + "/v1beta", Models: map[string]config.ModelConfig{"model-alias": {Compat: &config.ModelCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(true)}}, Limit: config.ModelLimit{Context: 1048576, Output: 1048576}}}}, []string{"test-key"})
	provider, err := NewGeminiProviderWithClient(cfg, server.Client(), "")
	if err != nil {
		t.Fatal(err)
	}
	return provider, cfg
}
func TestGeminiContinuationRecoveryAndUsage(t *testing.T) {
	for _, test := range []struct {
		name          string
		streams       []string
		tokens        []string
		want          string
		input, output int
	}{
		{"normal", []string{continuationChunk("A", "T1", geminiContinuationFinish, 100, 40000), continuationChunk("BC", "", "STOP", 40100, 30000)}, []string{"", "T1"}, "ABC", 40200, 70000},
		{"checkpoint_tail", []string{continuationChunk("A", "T1", "", 100, 1) + continuationChunk("B", "", "", -1, 0), continuationChunk("BC", "", "STOP", 101, 2)}, []string{"", "T1"}, "ABC", 201, 3},
		{"checkpoint_retained", []string{continuationChunk("A", "T1", geminiContinuationFinish, 100, 1), "", continuationChunk("BC", "", "STOP", 101, 2)}, []string{"", "T1", "T1"}, "ABC", 201, 3},
		{"no_checkpoint", []string{continuationChunk("discard", "", "", -1, 0), continuationChunk("ABC", "", "STOP", 100, 3)}, []string{"", ""}, "ABC", 100, 3},
		{"max_tokens", []string{continuationChunk("ABC", "T1", "MAX_TOKENS", 100, 3)}, []string{""}, "ABC", 100, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			var tokens []string
			var base map[string]any
			provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("x-goog-api-key") != "test-key" {
					t.Error("credential changed")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				token, _ := body["continuationToken"].(string)
				tokens = append(tokens, token)
				delete(body, "continuationToken")
				if base == nil {
					base = body
				} else if !reflect.DeepEqual(base, body) {
					t.Error("base body changed")
				}
				if len(tokens) > len(test.streams) {
					t.Error("unexpected request")
					w.WriteHeader(400)
					return
				}
				fmt.Fprint(w, test.streams[len(tokens)-1])
			})
			var preview string
			var receipts []ContinuationUsage
			ctx := context.WithValue(t.Context(), continuationObserverKey{}, func(u ContinuationUsage) { receipts = append(receipts, u) })
			frozen := freezeGeminiRequest(provider, "model-alias", "test-key", []byte(`{"contents":[],"generationConfig":{"maxOutputTokens":131072}}`))
			response, err := provider.runGeminiContinuation(ctx, frozen, func(d message.StreamDelta) {
				if d.Type == message.StreamDeltaRollback {
					preview = ""
				}
				if d.Type == message.StreamDeltaText {
					preview += d.Text
				}
			}, geminiRecoveryPolicy{jitter: func(time.Duration) time.Duration { return 0 }, wait: func(context.Context, time.Duration) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if response.Content != test.want || preview != test.want {
				t.Fatalf("response=%q preview=%q", response.Content, preview)
			}
			if !reflect.DeepEqual(tokens, test.tokens) {
				t.Fatalf("tokens=%v", tokens)
			}
			if response.Usage.InputTokens != test.input || response.Usage.OutputTokens != test.output {
				t.Fatalf("usage=%+v", response.Usage)
			}
			if len(receipts) != len(test.streams) || !response.UsageReported {
				t.Fatalf("receipts=%d", len(receipts))
			}
			if test.name == "normal" && (!response.Usage.Context.Known || response.Usage.Context.PromptTokens != 100 || response.Usage.Context.OutputTokens != 70000) {
				t.Fatalf("context=%+v", response.Usage.Context)
			}
		})
	}
}

func TestGeminiContinuationProtocolAndBudget(t *testing.T) {
	for _, test := range []struct {
		name     string
		stream   string
		requests int
		reason   string
	}{
		{"missing_token", continuationChunk("A", "", geminiContinuationFinish, 1, 1), 1, "protocol_error"},
		{"no_progress", continuationChunk("", "T1", geminiContinuationFinish, 1, 1), 2, "no_progress"},
		{"malformed", "data: {invalid}\n", 1, "protocol_error"},
		{"bounded_eof", "", 4, "recovery_exhausted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			count := 0
			provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) { count++; fmt.Fprint(w, test.stream) })
			frozen := freezeGeminiRequest(provider, "model-alias", "test-key", []byte(`{"contents":[]}`))
			_, err := provider.runGeminiContinuation(t.Context(), frozen, nil, geminiRecoveryPolicy{jitter: func(time.Duration) time.Duration { return 0 }, wait: func(context.Context, time.Duration) error { return nil }})
			typed, ok := errors.AsType[*GeminiContinuationError](err)
			if !ok || typed.Reason != test.reason || count != test.requests {
				t.Fatalf("err=%v count=%d", err, count)
			}
		})
	}
}
func TestGeminiContinuationClientBudgetAndTools(t *testing.T) {
	requests := 0
	var events []message.StreamDelta
	provider, cfg := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		data, _ := io.ReadAll(r.Body)
		var body struct {
			Generation struct {
				Max int `json:"maxOutputTokens"`
			} `json:"generationConfig"`
		}
		json.Unmarshal(data, &body)
		if body.Generation.Max != 262144 {
			t.Errorf("budget=%d", body.Generation.Max)
		}
		if requests == 1 {
			fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"Reasoning"}]},"continuationToken":"T1","finishReason":"CONTINUATION"}]}`+"\n\n")
			return
		}
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"sample_tool","args":{"value":1}},"thoughtSignature":"signature"},{"functionCall":{"name":"sample_tool","args":{"value":1}}}]},"finishReason":"STOP"}]}`+"\n\n")
	})
	client := NewClient(cfg, provider, "model-alias", 262144, "")
	client.SetOutputTokenMax(262144)
	response, err := client.CompleteStream(t.Context(), []message.Message{{Role: message.RoleUser, Content: "test"}}, nil, func(d message.StreamDelta) {
		if d.ToolCall != nil {
			if requests != 2 || !d.ToolCall.DeferExecution {
				t.Error("early tool execution")
			}
			events = append(events, d)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 2 || len(events) != 6 || response.ToolCalls[0].ID == response.ToolCalls[1].ID {
		t.Fatalf("calls=%v events=%d", response.ToolCalls, len(events))
	}
}
func TestGeminiContinuationGate(t *testing.T) {
	gate := &executiongate.Gate{}
	requests := 0
	provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprint(w, continuationChunk("A", "T1", geminiContinuationFinish, 1, 1))
		gate.Freeze()
	})
	ctx := executiongate.WithContext(t.Context(), gate, "test")
	response, err := provider.runGeminiContinuation(ctx, freezeGeminiRequest(provider, "model-alias", "test-key", []byte(`{}`)), nil, defaultGeminiRecoveryPolicy())
	if !executiongate.IsPaused(err) || requests != 1 || response.Content != "A" || len(gate.Snapshot().Activities) != 0 {
		t.Fatalf("response=%+v err=%v requests=%d", response, err, requests)
	}
}
func TestGeminiContinuationRedaction(t *testing.T) {
	sentinel := "opaque-secret"
	data := strings.TrimSpace(strings.TrimPrefix(continuationChunk("A", sentinel, geminiContinuationFinish, 1, 1), "data:"))
	if strings.Contains(redactGeminiChunk([]byte(data)), sentinel) {
		t.Fatal("token leaked")
	}
}

func TestGeminiContinuationFallbackUsesReliableHistory(t *testing.T) {
	requests := 0
	provider, cfg := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprint(w, continuationChunk("discarded", "T1", "", 1, 1)+"data: {invalid}\n")
	})
	fallbackCfg := testProviderConfig("fallback", "model-2")
	fallback := &rejectBinaryPartsProvider{resp: &message.Response{Content: "completed"}}
	client := NewClient(cfg, provider, "model-alias", 4096, "system")
	client.SetFallbackModels([]FallbackModel{{ProviderConfig: fallbackCfg, ProviderImpl: fallback, ModelID: "model-2", MaxTokens: 4096}})
	original := []message.Message{{Role: message.RoleUser, Content: "reliable"}}
	response, err := client.CompleteStream(t.Context(), original, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "completed" || requests != 1 || len(fallback.LastMessages()) == 0 {
		t.Fatalf("response=%+v requests=%d", response, requests)
	}
	if got := fallback.LastMessages(); !reflect.DeepEqual(got, original) {
		t.Fatalf("fallback history = %+v, want original reliable history", got)
	}
}

func TestGeminiContinuationDirectOverridesRejected(t *testing.T) {
	for _, body := range []string{`{"continuationToken":"T"}`, `{"continuation_token":"T"}`, `{"generationConfig":{"maxOutputTokens":200,"candidateCount":1}}`, `{"generationConfig":{"maxOutputTokens":100,"candidateCount":2}}`} {
		if validateGeminiContinuationBody([]byte(body), 100, true) == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestGeminiContinuationLimitsAndTrailers(t *testing.T) {
	tests := []struct {
		name         string
		handler      func(int) string
		wantRequests int
		reason       string
	}{
		{"physical_limit", func(n int) string {
			return continuationChunk("", fmt.Sprintf("T%d", n), geminiContinuationFinish, 1, 1)
		}, 128, "request_limit"},
		{"recovery_limit", func(n int) string { return continuationChunk("", fmt.Sprintf("T%d", n), "", 1, 1) }, 9, "recovery_exhausted"},
		{"token_cycle", func(n int) string {
			return continuationChunk("", fmt.Sprintf("T%d", n%2), geminiContinuationFinish, 1, 1)
		}, 3, "no_progress"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			count := 0
			provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) { count++; fmt.Fprint(w, test.handler(count)) })
			_, err := provider.runGeminiContinuation(t.Context(), freezeGeminiRequest(provider, "model-alias", "test-key", []byte(`{}`)), nil, geminiRecoveryPolicy{jitter: func(time.Duration) time.Duration { return 0 }, wait: func(context.Context, time.Duration) error { return nil }})
			typed, ok := errors.AsType[*GeminiContinuationError](err)
			if !ok || typed.Reason != test.reason || count != test.wantRequests {
				t.Fatalf("err=%v count=%d", err, count)
			}
		})
	}
	t.Run("usage_trailer", func(t *testing.T) {
		provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, continuationChunk("final", "", "STOP", -1, 0)+"data: {\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":3}}\n\n")
		})
		response, err := provider.CompleteStream(t.Context(), "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, nil)
		if err != nil || !response.Usage.Context.Known || response.Usage.OutputTokens != 3 {
			t.Fatalf("resp=%+v err=%v", response, err)
		}
	})
	t.Run("missing_terminal_usage", func(t *testing.T) {
		provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, continuationChunk("A", "", "", 100, 1)+continuationChunk("B", "", "STOP", -1, 0))
		})
		response, err := provider.CompleteStream(t.Context(), "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, nil)
		if err != nil || response.Usage.Context.Known || response.Content != "AB" {
			t.Fatalf("resp=%+v err=%v", response, err)
		}
	})
	t.Run("large_checkpoint", func(t *testing.T) {
		token := strings.Repeat("t", 2<<20)
		count := 0
		provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
			count++
			if count == 1 {
				fmt.Fprint(w, continuationChunk("A", token, geminiContinuationFinish, 1, 1))
			} else {
				fmt.Fprint(w, continuationChunk("B", "", "STOP", 2, 1))
			}
		})
		response, err := provider.CompleteStream(t.Context(), "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, nil)
		if err != nil || response.Content != "AB" || count != 2 {
			t.Fatalf("err=%v count=%d", err, count)
		}
	})
}

func TestGeminiContinuationWaitAndParentCancel(t *testing.T) {
	gate := &executiongate.Gate{}
	count := 0
	provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			fmt.Fprint(w, continuationChunk("A", "T1", geminiContinuationFinish, 100, 1))
			return
		}
		w.Header().Set("Retry-After", "121")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := provider.runGeminiContinuation(t.Context(), freezeGeminiRequest(provider, "model-alias", "test-key", []byte(`{}`)), nil, geminiRecoveryPolicy{jitter: func(time.Duration) time.Duration { return 0 }, wait: func(context.Context, time.Duration) error { t.Error("exceeded waiting budget"); return nil }})
	if _, ok := errors.AsType[*GeminiContinuationError](err); !ok || count != 2 {
		t.Fatalf("err=%v count=%d", err, count)
	}
	ctx := executiongate.WithContext(t.Context(), gate, "test")
	gate.Freeze()
	if err := defaultGeminiRecoveryPolicy().wait(ctx, time.Hour); !executiongate.IsPaused(err) {
		t.Fatalf("wait=%v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	before := count
	_, err = provider.runGeminiContinuation(cancelled, freezeGeminiRequest(provider, "model-alias", "test-key", []byte(`{}`)), nil, defaultGeminiRecoveryPolicy())
	if !errors.Is(err, context.Canceled) || before != count {
		t.Fatalf("err=%v count=%d", err, count)
	}
}

func TestGeminiContinuationDumpAndTraceRedactTokens(t *testing.T) {
	const sentinel = "opaque-continuation-secret"
	count := 0
	provider, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			fmt.Fprint(w, continuationChunk("A", sentinel, geminiContinuationFinish, 100, 1))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":{"code":400,"message":"rejected %s","status":"INVALID_ARGUMENT"}}`, sentinel)
	})
	dir := t.TempDir()
	provider.SetDumpWriter(NewDumpWriter(dir))
	provider.SetTraceWriter(NewTraceWriter(filepath.Join(dir, "trace.jsonl")))
	_, err := provider.CompleteStream(t.Context(), "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, nil)
	if err == nil {
		t.Fatal("expected token rejection")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatal("token leaked in error")
	}
	files, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(files) < 3 {
		t.Fatalf("missing diagnostics: %d files", len(files))
	}
	for _, file := range files {
		data, readErr := os.ReadFile(filepath.Join(dir, file.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), sentinel) {
			t.Fatalf("token leaked in %s", file.Name())
		}
	}
}

func TestDisabledContinuationRejectsWholeChunkBeforeTools(t *testing.T) {
	stream := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"sample_tool","args":{}}}]},"finishReason":"STOP"},{"finishReason":"CONTINUATION","continuationToken":"opaque-secret"}]}` + "\n\n"
	toolEvents := 0
	_, err := parseGeminiSSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		if delta.ToolCall != nil {
			toolEvents++
		}
	}, nil)
	failure, ok := errors.AsType[*GeminiContinuationError](err)
	if !ok || failure.Reason != "continuation_disabled" || toolEvents != 0 {
		t.Fatalf("err=%v tools=%d", err, toolEvents)
	}
}
