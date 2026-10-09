package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestNativePreExecutionFailureReleasesSession(t *testing.T) {
	for _, protocol := range []string{config.ProviderTypeResponses, config.ProviderTypeMessages} {
		for _, local := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/rejected", true: "/not_sent"}[local], func(t *testing.T) {
				var requests atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"invalid_parameter","message":"Unsupported search option"}}`)
				}))
				defer srv.Close()
				endpoint := srv.URL
				if local {
					endpoint = "://invalid"
				}
				contract := config.NativeWebSearchResponses
				if protocol == config.ProviderTypeMessages {
					contract = config.NativeWebSearchMessages
				}
				provider := llm.NewProviderConfig("sample", config.ProviderConfig{Type: protocol, APIURL: endpoint, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: contract, APIURL: endpoint, Preauthorized: true}}}}, []string{"key"})
				var impl llm.Provider
				var err error
				if protocol == config.ProviderTypeResponses {
					impl, err = llm.NewResponsesProvider(provider, "")
				} else {
					impl, err = llm.NewAnthropicProvider(provider, "")
				}
				if err != nil {
					t.Fatal(err)
				}
				client := llm.NewClient(provider, impl, "test-model", 1024, "")
				defer client.Close()
				journal := recovery.NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main"}
				policy := nativePolicy(journal, func(string) bool { return true }, nil)
				policy.Failed = func(*message.NativeToolHistory) { t.Error("unexecuted request produced an unknown receipt") }
				_, err = client.CompleteStreamWithOptions(t.Context(), []message.Message{{Role: message.RoleUser, Content: "Search sample documentation"}}, nil, nil, llm.CompleteStreamOptions{NativeTools: policy})
				if err == nil {
					t.Fatal("expected request failure")
				}
				wantRequests := int32(1)
				wantOutcome := message.NativeRequestRejected
				if local {
					wantRequests = 0
					wantOutcome = message.NativeRequestNotSent
				}
				if got := requests.Load(); got != wantRequests {
					t.Fatalf("requests=%d, want %d", got, wantRequests)
				}
				if _, err = journal.Check(); err != nil {
					t.Fatalf("unexecuted request blocked session: %v", err)
				}
				files, err := filepath.Glob(filepath.Join(journal.SessionDir, "native-requests", "*.result.json"))
				if err != nil || len(files) != 1 {
					t.Fatalf("results=%v err=%v", files, err)
				}
				raw, err := os.ReadFile(files[0])
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					Outcome message.NativeRequestOutcome `json:"outcome"`
				}
				if err = json.Unmarshal(raw, &result); err != nil || result.Outcome != wantOutcome {
					t.Fatalf("result=%s err=%v", raw, err)
				}
				if _, err = journal.Restore(nil, func(message.Message) error { t.Error("unexecuted request created recovery card"); return nil }); err != nil {
					t.Fatal(err)
				}
				if _, err = journal.Begin(nil, nil); err != nil {
					t.Fatalf("corrected request remains blocked: %v", err)
				}
			})
		}
	}
}

func TestNativeResponsesContractRejectionAllowsNextRequestAndRestore(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"request type", `{"error":{"type":"invalid_request_error","message":"request rejected"}}`},
		{"Responses code", `{"error":{"code":"invalid_responses_request","message":"request rejected"}}`},
		{"nested Responses code", `{"error":{"message":"{\"error\":{\"code\":\"invalid_responses_request\"}}"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if requests.Add(1) == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, tc.body)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Sample answer."}]}]}}`+"\n\n")
			}))
			defer srv.Close()
			provider := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: srv.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchResponses, APIURL: srv.URL, Preauthorized: true}}}}, []string{"key-1", "key-2"})
			impl, err := llm.NewResponsesProvider(provider, "direct")
			if err != nil {
				t.Fatal(err)
			}
			client := llm.NewClient(provider, impl, "test-model", 1024, "")
			defer client.Close()
			journal := recovery.NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main"}
			policy := nativePolicy(journal, func(string) bool { return true }, nil)
			policy.Failed = func(*message.NativeToolHistory) { t.Error("explicit refusal produced an unknown receipt") }
			msgs := []message.Message{{Role: message.RoleUser, Content: "Search sample documentation"}}
			_, err = client.CompleteStreamWithOptions(t.Context(), msgs, nil, nil, llm.CompleteStreamOptions{NativeTools: policy})
			if err == nil || requests.Load() != 1 {
				t.Fatalf("requests=%d err=%v", requests.Load(), err)
			}
			assertNativeJournalOutcome(t, journal, message.NativeRequestRejected)
			if _, err = journal.Check(); err != nil {
				t.Fatalf("explicit refusal blocked session: %v", err)
			}
			// Reopen the journal to exercise recovery from the durable result.
			reopened := recovery.NativeRequestJournal{SessionDir: journal.SessionDir, AgentID: journal.AgentID}
			history, err := reopened.Restore(nil, func(message.Message) error { t.Error("explicit refusal created recovery card"); return nil })
			if err != nil || len(history) != 0 {
				t.Fatalf("history=%+v err=%v", history, err)
			}
			if _, err = reopened.Check(); err != nil {
				t.Fatalf("restored refusal blocked session: %v", err)
			}
			resumedPolicy := nativePolicy(reopened, func(string) bool { return true }, nil)
			resumedPolicy.Failed = policy.Failed
			resp, err := client.CompleteStreamWithOptions(t.Context(), msgs, nil, nil, llm.CompleteStreamOptions{NativeTools: resumedPolicy})
			if err != nil || requests.Load() != 2 || resp == nil || resp.Content != "Sample answer." || resp.NativeTools == nil || resp.NativeTools.OutcomeUnknown {
				t.Fatalf("requests=%d response=%+v err=%v", requests.Load(), resp, err)
			}
		})
	}
}
