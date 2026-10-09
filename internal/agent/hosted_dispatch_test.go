package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestHostedDispatchRefundsOnlyUnsentAttempts(t *testing.T) {
	for _, protocol := range []string{config.ProviderTypeResponses, config.ProviderTypeMessages} {
		for _, kind := range []string{"construction", "cancelled", "sent_failure"} {
			t.Run(protocol+"/"+kind, func(t *testing.T) {
				var received atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					received.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Invalid request"}}`)
				}))
				defer srv.Close()
				endpoint := srv.URL
				if kind == "construction" {
					endpoint = "://invalid"
				}
				cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: protocol, APIURL: endpoint}, []string{"key"})
				var provider llm.Provider
				var err error
				if protocol == config.ProviderTypeResponses {
					provider, err = llm.NewResponsesProvider(cfg, "")
				} else {
					provider, err = llm.NewAnthropicProvider(cfg, "")
				}
				if err != nil {
					t.Fatal(err)
				}
				g := newResourceGovernor(config.OrchestrationConfig{ProviderHostedRequestsPerMinute: map[string]int{"sample": 1}, ProviderHostedRetriesPerMinute: map[string]int{"sample": 1}})
				observed := 0
				wrapped := hostedRequestProvider{Provider: provider, governor: g, providerName: "sample", retry: func() bool { return true }, observe: func(*message.Response, error, time.Duration) { observed++ }}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				_, err = wrapped.CompleteStream(ctx, "key", "test-model", "", []message.Message{{Role: message.RoleUser, Content: "Sample input"}}, nil, 1024, llm.RequestTuning{}, func(delta message.StreamDelta) {
					if kind == "cancelled" && delta.Status != nil && delta.Status.Type == message.StatusDeltaConnecting {
						cancel()
					}
				})
				if err == nil {
					t.Fatal("expected request failure")
				}
				want := 0
				if kind == "sent_failure" {
					want = 1
				}
				if observed != want || int(received.Load()) != want || len(g.hosted.requests["sample"]) != want || len(g.hosted.retries["sample"]) != want {
					t.Fatalf("observed=%d received=%d requests=%d retries=%d", observed, received.Load(), len(g.hosted.requests["sample"]), len(g.hosted.retries["sample"]))
				}
				if got := g.snapshot(); got.LLMActive != 0 || got.LLMQueued != 0 || g.hosted.active["sample"] != 0 {
					t.Fatalf("leaked capacity: %+v", got)
				}
				if want == 0 {
					next, err := g.acquireHostedLLM(t.Context(), "sample/test-model", true)
					if err != nil {
						t.Fatalf("unsent attempt spent quota: %v", err)
					}
					next.release(true)
				}
			})
		}
	}
}

func TestHostedRateWindowBeginsAtDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newResourceGovernor(config.OrchestrationConfig{ProviderHostedRequestsPerMinute: map[string]int{"sample": 1}})
		reservation, err := g.acquireHostedLLM(t.Context(), "sample/test-model", false)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		if _, err := g.acquireHostedLLM(t.Context(), "sample/test-model", false); err == nil {
			t.Fatal("pending reservation expired before dispatch")
		}
		reservation.markSent()
		time.Sleep(30 * time.Second)
		reservation.release(false)
		if _, err := g.acquireHostedLLM(t.Context(), "sample/test-model", false); err == nil {
			t.Fatal("dispatched request expired before its minute ended")
		}
		time.Sleep(30 * time.Second)
		next, err := g.acquireHostedLLM(t.Context(), "sample/test-model", false)
		if err != nil {
			t.Fatalf("completed rate window did not expire: %v", err)
		}
		next.release(true)
	})
}
