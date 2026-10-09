package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestNativeRejectionRetryRequiresNoEarlierExecution(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		t.Run(fmt.Sprint(continuation), func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if continuation && n == 1 {
					w.Header().Set("Content-Type", "text/event-stream")
					var chunks []string
					for chunk := range strings.SplitSeq(nativeAnthropicFixture("pause_turn"), "\n\n") {
						if !strings.Contains(chunk, `"index":1`) {
							chunks = append(chunks, chunk)
						}
					}
					_, _ = io.WriteString(w, strings.Join(chunks, "\n\n"))
					return
				}
				if continuation || n == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"Invalid API key"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, nativeAnthropicFixture("end_turn"))
			}))
			defer srv.Close()
			provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchMessages, APIURL: srv.URL, Preauthorized: true}}}}, []string{"key-1", "key-2"})
			impl, err := NewAnthropicProvider(provider, "")
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(provider, impl, "test-model", 1024, "")
			defer client.Close()
			var outcomes []message.NativeRequestOutcome
			var failed *message.NativeToolHistory
			began := 0
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true },
				Begin: func(context.Context, NativeRequestRecord) (string, error) {
					began++
					return fmt.Sprintf("request-%d", began), nil
				},
				Finish: func(_ string, outcome message.NativeRequestOutcome, _ *message.Response, _ error) error {
					outcomes = append(outcomes, outcome)
					return nil
				},
				Failed: func(receipt *message.NativeToolHistory) { failed = receipt },
			}
			resp, err := client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Search sample documentation"}}, nil, nil, CompleteStreamOptions{NativeTools: policy})
			if requests.Load() != 2 || len(outcomes) != 2 {
				t.Fatalf("requests=%d outcomes=%v err=%v", requests.Load(), outcomes, err)
			}
			if continuation {
				if !IsNativeToolError(err) || failed == nil || !failed.OutcomeUnknown || len(failed.RequestIDs) != 1 || failed.RequestIDs[0] != "request-1" {
					t.Fatalf("earlier execution lost its barrier: receipt=%+v err=%v", failed, err)
				}
				if outcomes[0] != message.NativeRequestCompleted || outcomes[1] != message.NativeRequestRejected {
					t.Fatalf("outcomes=%v", outcomes)
				}
			} else {
				if err != nil || failed != nil || resp == nil || resp.NativeTools == nil || len(resp.NativeTools.RequestIDs) != 1 || resp.NativeTools.RequestIDs[0] != "request-2" {
					t.Fatalf("safe key retry failed: response=%+v receipt=%+v err=%v", resp, failed, err)
				}
				if outcomes[0] != message.NativeRequestRejected || outcomes[1] != message.NativeRequestCompleted {
					t.Fatalf("outcomes=%v", outcomes)
				}
			}
		})
	}
}
