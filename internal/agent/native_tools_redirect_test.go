package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestNativeRedirectPreservesEndpointAndJournalBarrier(t *testing.T) {
	for _, protocol := range []string{config.ProviderTypeResponses, config.ProviderTypeMessages} {
		for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			t.Run(fmt.Sprintf("%s/%d", protocol, status), func(t *testing.T) {
				var redirected, sourceRequests atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					redirected.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"request rejected"}}`)
				}))
				defer target.Close()
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := sourceRequests.Add(1)
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					hasNative := strings.Contains(string(raw), `"type":"web_search`)
					if hasNative != (n == 1) {
						t.Errorf("request %d native declaration=%t", n, hasNative)
					}
					http.Redirect(w, r, target.URL, status)
				}))
				defer source.Close()
				contract := config.NativeWebSearchResponses
				if protocol == config.ProviderTypeMessages {
					contract = config.NativeWebSearchMessages
				}
				provider := llm.NewProviderConfig("sample", config.ProviderConfig{Type: protocol, APIURL: source.URL, Models: map[string]config.ModelConfig{"test-model": {NativeWebSearch: &config.NativeWebSearchConfig{Contract: contract, APIURL: source.URL, Preauthorized: true}}}}, []string{"key-1", "key-2"})
				var impl llm.Provider
				var err error
				if protocol == config.ProviderTypeResponses {
					impl, err = llm.NewResponsesProvider(provider, "direct")
				} else {
					impl, err = llm.NewAnthropicProvider(provider, "direct")
				}
				if err != nil {
					t.Fatal(err)
				}
				client := llm.NewClient(provider, impl, "test-model", 1024, "")
				defer client.Close()
				journal := recovery.NativeRequestJournal{SessionDir: t.TempDir(), AgentID: "main"}
				policy := nativePolicy(journal, func(string) bool { return true }, nil)
				begin := policy.Begin
				began := 0
				policy.Begin = func(ctx context.Context, record llm.NativeRequestRecord) (string, error) {
					began++
					if record.APIURL != source.URL {
						t.Errorf("authorized endpoint=%q, want %q", record.APIURL, source.URL)
					}
					return begin(ctx, record)
				}
				var failed *message.NativeToolHistory
				policy.Failed = func(receipt *message.NativeToolHistory) { failed = receipt }
				msgs := []message.Message{{Role: message.RoleUser, Content: "Search sample documentation"}}
				_, err = client.CompleteStreamWithOptions(t.Context(), msgs, nil, nil, llm.CompleteStreamOptions{NativeTools: policy})
				if !llm.IsNativeToolError(err) || !strings.Contains(err.Error(), "redirect refused") || failed == nil || !failed.OutcomeUnknown {
					t.Fatalf("failure=%+v err=%v", failed, err)
				}
				if redirected.Load() != 0 || sourceRequests.Load() != 1 || began != 1 {
					t.Fatalf("redirected=%d source requests=%d authorizations=%d", redirected.Load(), sourceRequests.Load(), began)
				}
				assertNativeJournalOutcome(t, journal, message.NativeRequestUnknown)
				if _, err = journal.Check(); err == nil {
					t.Fatal("redirect failure lost its execution barrier")
				}
				persisted := 0
				restored, err := journal.Restore(nil, func(msg message.Message) error {
					persisted++
					if msg.NativeTools == nil || !msg.NativeTools.OutcomeUnknown || msg.NativeTools.APIURL != source.URL {
						t.Errorf("recovered receipt=%+v", msg.NativeTools)
					}
					return nil
				})
				if err != nil || persisted != 1 || len(restored) != 1 {
					t.Fatalf("restored=%+v persisted=%d err=%v", restored, persisted, err)
				}
				// An ordinary request on the same provider retains its redirect
				// behavior; the native request must not mutate the shared client.
				_, _ = client.CompleteStream(t.Context(), msgs, nil, nil)
				if redirected.Load() != 1 || sourceRequests.Load() != 2 {
					t.Fatalf("ordinary redirect changed: redirected=%d source requests=%d", redirected.Load(), sourceRequests.Load())
				}
			})
		}
	}
}

func assertNativeJournalOutcome(t *testing.T, journal recovery.NativeRequestJournal, want message.NativeRequestOutcome) {
	t.Helper()
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
		Error   string                       `json:"error"`
	}
	if err = json.Unmarshal(raw, &result); err != nil || result.Outcome != want || result.Error == "" {
		t.Fatalf("result=%s want=%s err=%v", raw, want, err)
	}
}
