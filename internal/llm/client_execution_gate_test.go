package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"golang.org/x/time/rate"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
)

func TestHTTPAdmissionFreezeRetainsBodyAndRejectsNextRequest(t *testing.T) {
	var gate executiongate.Gate
	var calls, starts atomic.Int32
	ctx := executiongate.WithContext(t.Context(), &gate, "task")
	ctx = WithRequestStarted(ctx, func() { starts.Add(1) })
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.GetBody != nil {
			t.Fatal("transport can replay request without admission")
		}
		gate.Freeze()
		if req.Context().Err() != nil {
			t.Fatal("freeze cancelled admitted request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("result")), Header: make(http.Header)}, nil
	})}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", strings.NewReader("{}"))
	resp, err := doRequestUntilHeaders(client, request, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(gate.Snapshot().Activities) != 1 {
		t.Fatal("lease released before response body close")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "result" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if _, err := doRequestUntilHeaders(client, request, time.Second); !executiongate.IsPaused(err) {
		t.Fatalf("next request err=%v", err)
	}
	if calls.Load() != 1 || starts.Load() != 1 {
		t.Fatalf("calls=%d starts=%d", calls.Load(), starts.Load())
	}
	resp.Body.Close()
	resp.Body.Close()
	if len(gate.Snapshot().Activities) != 0 {
		t.Fatal("lease leaked")
	}
}

func TestRequestStartedIndependentOfNativeDispatch(t *testing.T) {
	starts, sent := 0, 0
	ctx := WithRequestStarted(t.Context(), func() { starts++ })
	for range 2 {
		requestCtx, dispatch := WithRequestDispatch(ctx, func() { sent++ })
		markRequestPrepared(requestCtx)
		markRequestSent(requestCtx)
		if dispatch.NotSent() {
			t.Fatal("request was marked not_sent")
		}
	}
	if starts != 2 || sent != 2 {
		t.Fatalf("starts=%d sent=%d", starts, sent)
	}
}

func TestKeyLimiterWaitStopsOnFreeze(t *testing.T) {
	var gate executiongate.Gate
	p := NewProviderConfig("sample", config.ProviderConfig{}, []string{"key"})
	p.limiter = rate.NewLimiter(rate.Every(time.Hour), 1)
	if !p.limiter.Allow() {
		t.Fatal("initial token unavailable")
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := p.SelectKeyWithContext(executiongate.WithContext(t.Context(), &gate, "task"))
		done <- err
	}()
	gate.Freeze()
	select {
	case err := <-done:
		if !executiongate.IsPaused(err) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("key wait ignored freeze")
	}
	if p.keyStates[0].Invalid || p.keyStates[0].CooldownCount != 0 {
		t.Fatal("pause damaged key health")
	}
}

func TestClientPauseStopsRetryWithoutMovingCursor(t *testing.T) {
	var gate executiongate.Gate
	p := NewProviderConfig("sample", config.ProviderConfig{Models: map[string]config.ModelConfig{"test-model": {}}}, []string{"key"})
	impl := &scriptedProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 500}}, {err: &APIError{StatusCode: 500}}}}
	c := NewClient(p, impl, "test-model", 1024, "")
	defer c.Close()
	c.SetExecutionGate(&gate, "task")
	c.SetFallbackModels([]FallbackModel{{ProviderConfig: p, ProviderImpl: impl, ModelID: "other-model", MaxTokens: 1024}})
	c.SetStreamRetryRounds(2)
	_, err := c.CompleteStream(t.Context(), nil, nil, func(delta message.StreamDelta) {
		if delta.Status != nil && delta.Status.Type == message.StatusDeltaRetrying {
			gate.Freeze()
		}
	})
	if !executiongate.IsPaused(err) {
		t.Fatalf("error=%v", err)
	}
	if impl.CallCount() != 1 {
		t.Fatalf("calls=%d", impl.CallCount())
	}
	if c.poolCursor != 0 {
		t.Fatalf("cursor changed to %d", c.poolCursor)
	}
}

func TestNativeAdmissionFreezePreservesCompletedReceipt(t *testing.T) {
	for _, when := range []string{"authorization", "completed response"} {
		t.Run(when, func(t *testing.T) {
			var gate executiongate.Gate
			model := config.ModelConfig{NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchMessages, APIURL: "https://example.invalid/messages", Preauthorized: true}}
			p := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: model.NativeWebSearch.APIURL, Models: map[string]config.ModelConfig{"test-model": model}}, []string{"key"})
			impl := &scriptedProvider{calls: []scriptedCall{{resp: &message.Response{Content: "Searching.", StopReason: "pause_turn", Hosted: &message.HostedObservation{Items: []json.RawMessage{json.RawMessage(`{"type":"text","text":"Searching."}`)}}}}}}
			c := NewClient(p, impl, "test-model", 1024, "")
			defer c.Close()
			c.SetExecutionGate(&gate, "task")
			begins, finishes, failures := 0, 0, 0
			var outcome message.NativeRequestOutcome
			policy := &NativeToolPolicy{Permitted: func(string) bool { return true }, Begin: func(context.Context, NativeRequestRecord) (string, error) {
				begins++
				if when == "authorization" {
					gate.Freeze()
				}
				return "request-1", nil
			}, Finish: func(_ string, o message.NativeRequestOutcome, _ *message.Response, _ error) error {
				outcome = o
				finishes++
				if when == "completed response" {
					gate.Freeze()
				}
				return nil
			}, Failed: func(*message.NativeToolHistory) { failures++ }}
			resp, err := c.CompleteStreamWithOptions(t.Context(), nil, nil, nil, CompleteStreamOptions{NativeTools: policy})
			if !executiongate.IsPaused(err) || begins != 1 || finishes != 1 || failures != 0 {
				t.Fatalf("err=%v begins=%d finishes=%d failures=%d", err, begins, finishes, failures)
			}
			if when == "authorization" {
				if impl.CallCount() != 0 || outcome != message.NativeRequestNotSent || resp != nil {
					t.Fatalf("calls=%d outcome=%s resp=%v", impl.CallCount(), outcome, resp)
				}
			} else {
				if impl.CallCount() != 1 || outcome != message.NativeRequestCompleted || resp == nil || resp.Content != "Searching." || resp.NativeTools == nil || resp.NativeTools.OutcomeUnknown || len(resp.NativeTools.RequestIDs) != 1 {
					t.Fatalf("calls=%d outcome=%s resp=%+v", impl.CallCount(), outcome, resp)
				}
			}
			if len(gate.Snapshot().Activities) != 0 {
				t.Fatal("native lease leaked")
			}
		})
	}
}

func TestHTTPAdmissionDoesNotFollowRedirect(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "native"}[native], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Redirect(w, r, "/second", http.StatusTemporaryRedirect)
			}))
			defer server.Close()
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, strings.NewReader("{}"))
			resp, err := doRequestUntilHeaders(nativeToolHTTPClient(server.Client(), native), req, time.Second)
			if native {
				if err == nil || !strings.Contains(err.Error(), "redirect refused") {
					t.Fatalf("err=%v", err)
				}
			} else {
				if resp != nil {
					resp.Body.Close()
				}
				if _, ok := errors.AsType[*RequestRedirectError](err); !ok {
					t.Fatalf("ordinary redirect err=%v", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("redirect issued %d requests", calls.Load())
			}
		})
	}
}

func TestWebSocketFreezePreservesResponseAndRejectsNextWrite(t *testing.T) {
	var gate executiongate.Gate
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		calls.Add(1)
		gate.Freeze()
		conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"response-1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`))
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := &ResponsesProvider{codexWSConn: conn}
	ctx := executiongate.WithContext(t.Context(), &gate, "task")
	env := codexWSResponseCreate{Type: "response.create", Model: "test-model"}
	resp, _, err := r.codexWSExecuteRequestLocked(ctx, "key", "test-model", env, nil, false, time.Now(), true, nil, "", false)
	if err != nil || resp == nil || resp.ProviderResponseID != "response-1" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if _, _, err := r.codexWSExecuteRequestLocked(ctx, "key", "test-model", env, nil, false, time.Now(), true, nil, "", false); !executiongate.IsPaused(err) {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(gate.Snapshot().Activities) != 0 {
		t.Fatalf("requests=%d state=%+v", calls.Load(), gate.Snapshot())
	}
}

func TestWebSocketPauseDoesNotFallbackOrRetry(t *testing.T) {
	var httpCalls, wsCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { httpCalls.Add(1) }))
	defer server.Close()
	p := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex, APIURL: server.URL + "/v1/responses", ResponsesWebsocket: new(true), Models: map[string]config.ModelConfig{"test-model": {}}}, []string{"key"})
	p.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"key": {CredentialIndex: 0, AccountID: "account", Expires: 32503680000000}}, "")
	r := &ResponsesProvider{provider: p, client: server.Client()}
	r.codexWSCompleteFn = func(context.Context, string, string, string, *responsesRequest, []responsesInputItem, StreamCallback, time.Time, codexWSCompleteOptions) (*message.Response, bool, error) {
		wsCalls.Add(1)
		return nil, true, &executiongate.ErrSessionPaused{}
	}
	_, err := r.CompleteStream(t.Context(), "key", "test-model", "", []message.Message{{Role: message.RoleUser, Content: "Hello."}}, nil, 128, RequestTuning{}, nil)
	if !executiongate.IsPaused(err) || wsCalls.Load() != 1 || httpCalls.Load() != 0 {
		t.Fatalf("err=%v ws=%d http=%d", err, wsCalls.Load(), httpCalls.Load())
	}
}
