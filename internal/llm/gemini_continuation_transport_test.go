package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeminiContinuationRedactsInputTokenAfterCheckpointReplacement(t *testing.T) {
	const inputToken = "opaque-input-capability"
	const nextToken = "opaque-next-capability"
	count := 0
	p, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			fmt.Fprint(w, continuationChunk("A", inputToken, geminiContinuationFinish, 100, 1))
			return
		}
		fmt.Fprint(w, continuationChunk("B", nextToken, "", 101, 1))
		fmt.Fprintf(w, "data: {\"error\":{\"code\":400,\"status\":%q,\"message\":%q}}\n\n", inputToken, "invalid "+inputToken+" "+nextToken)
	})
	dir := t.TempDir()
	p.SetDumpWriter(NewDumpWriter(dir))
	p.SetTraceWriter(NewTraceWriter(filepath.Join(dir, "trace.jsonl")))
	_, err := p.CompleteStream(t.Context(), "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, nil)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.StatusCode != 400 || apiErr.Origin != APIErrorOriginSSEEvent {
		t.Fatalf("lost error classification: %v", err)
	}
	for _, token := range []string{inputToken, nextToken} {
		if strings.Contains(apiErr.Error(), token) {
			t.Fatal("retained capability in error cause")
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 3 {
		t.Fatal("missing diagnostics")
	}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range []string{inputToken, nextToken} {
			if strings.Contains(string(data), token) {
				t.Fatalf("capability persisted in %s", f.Name())
			}
		}
	}
}

func TestGeminiContinuationInitialRejectionKeepsClassification(t *testing.T) {
	for _, status := range []int{400, 401, 403, 408, 429, 500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			requests := 0
			p, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"context length exceeded","status":"INVALID_ARGUMENT"}}`)
			})
			_, err := p.CompleteStream(t.Context(), "test-key", "model-alias", "", nil, nil, 100, RequestTuning{}, nil)
			if requests != 1 {
				t.Fatalf("initial rejection retried within continuation: %d requests", requests)
			}
			if _, wrapped := errors.AsType[*GeminiContinuationError](err); wrapped {
				t.Fatalf("initial rejection became exhausted chain: %v", err)
			}
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.StatusCode != status || apiErr.Message != "context length exceeded" {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGeminiContinuationAcceptedStreamRetainsRecoveryTarget(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(fmt.Sprint(checkpoint), func(t *testing.T) {
			requests := 0
			p, _ := continuationProvider(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("x-goog-api-key") != "test-key" {
					t.Error("credential changed")
				}
				if requests == 1 {
					if checkpoint {
						fmt.Fprint(w, continuationChunk("A", "T1", geminiContinuationFinish, 100, 1))
					} else {
						fmt.Fprint(w, continuationChunk("discard", "", "", -1, 0))
					}
					return
				}
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":{"message":"quota exhausted"}}`)
			})
			frozen := freezeGeminiRequest(p, "model-alias", "test-key", []byte(`{"contents":[]}`))
			_, err := p.runGeminiContinuation(t.Context(), frozen, nil, geminiRecoveryPolicy{jitter: func(time.Duration) time.Duration { return 0 }, wait: func(context.Context, time.Duration) error { return nil }})
			failure, ok := errors.AsType[*GeminiContinuationError](err)
			wantRequests := 4
			if checkpoint {
				wantRequests++
			}
			if !ok || failure.Reason != "recovery_exhausted" || requests != wantRequests {
				t.Fatalf("error=%v requests=%d want=%d", err, requests, wantRequests)
			}
		})
	}
}
