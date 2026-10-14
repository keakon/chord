package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/message"
)

func TestDiagnosticWebSocketRetryThenHTTP(t *testing.T) {
	var wsCalls, httpCalls atomic.Int32
	collector, store := diagnosticTestCollector()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			httpCalls.Add(1)
			snapshot, ok := store.Latest()
			if !ok || snapshot.Transport != contextdiag.HTTP || snapshot.ID.Attempt != 3 || snapshot.Usage.State != contextdiag.Missing {
				t.Errorf("HTTP submission=%+v", snapshot)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"type":"response.completed","response":{"id":"http-final","status":"completed","output":[],"usage":{"input_tokens":23,"output_tokens":7}}}`+"\n\n")
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request map[string]any
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			n := wsCalls.Add(1)
			if n > 2 {
				snapshot, ok := store.Latest()
				if !ok || snapshot.Transport != contextdiag.WebSocket || snapshot.ID.Attempt != uint64(n-2) || snapshot.State != contextdiag.Dispatched {
					t.Errorf("WS submission=%+v", snapshot)
				}
				if n == 3 && request["previous_response_id"] == nil {
					t.Error("expected incremental request")
				}
				if n == 4 && request["previous_response_id"] != nil {
					t.Error("expected full retry")
				}
				if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":500,"error":{"code":"server_error","message":"sample unavailable"}}`)); err != nil {
					return
				}
				continue
			}
			response := fmt.Sprintf(`{"type":"response.completed","response":{"id":"response-%d","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`, n)
			if err := conn.WriteMessage(websocket.TextMessage, []byte(response)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeResponses, Preset: config.ProviderPresetCodex, APIURL: srv.URL + "/v1/responses", ResponsesWebsocket: new(true), Models: map[string]config.ModelConfig{"test-model": {}}}, []string{"sample-key"})
	cfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"sample-key": {CredentialIndex: 0, AccountID: "sample-account", Expires: 32503680000000}}, "")
	impl, err := NewResponsesProvider(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer impl.resetCodexWebSocketChain("test")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	messages := []message.Message{{Role: message.RoleUser, Content: "First request"}}
	for i := range 2 {
		requestCtx := context.WithValue(ctx, diagnosticsContextKey{}, collector.BeginRequest(0))
		resp, err := completeObservedProvider(requestCtx, impl, cfg, "sample-key", "test-model", "system", messages, nil, 128, RequestTuning{SessionKey: "sample-session"}, nil)
		if err != nil {
			t.Fatalf("request failed: %v; WS=%d HTTP=%d", err, wsCalls.Load(), httpCalls.Load())
		}
		snapshot, ok := store.Latest()
		if !ok || snapshot.State != contextdiag.Completed {
			t.Fatalf("completion=%+v", snapshot)
		}
		if i == 0 {
			if snapshot.ID.Attempt != 1 || snapshot.Transport != contextdiag.WebSocket {
				t.Fatalf("prewarm counted as request: %+v", snapshot)
			}
			messages = append(messages, message.Message{Role: message.RoleUser, Content: "Second request"})
		} else if snapshot.ID.Attempt != 3 || snapshot.Usage.FullInput != 23 || snapshot.Usage.Output != 7 || resp.ProviderResponseID != "http-final" {
			t.Fatalf("final usage=%+v", snapshot)
		}
	}
	if wsCalls.Load() != 4 || httpCalls.Load() != 1 {
		t.Fatalf("WS=%d HTTP=%d", wsCalls.Load(), httpCalls.Load())
	}
}

func TestDiagnosticWebSocketWriteFailure(t *testing.T) {
	srv := newCodexWSCaptureServer(t, nil)
	defer srv.Close()
	url := "ws" + srv.server.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.DialContext(t.Context(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A sent close frame leaves deadline updates valid but rejects a later data write.
	if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cfg := diagnosticProviderConfig(config.ProviderTypeResponses)
	collector, store := diagnosticTestCollector()
	scope := &diagnosticScope{request: collector.BeginRequest(0), provider: cfg, baseline: diagnosticEmptyBaseline(), snapshot: contextdiag.Snapshot{Usage: contextdiag.Usage{State: contextdiag.Missing}}}
	ctx := context.WithValue(t.Context(), diagnosticScopeContextKey{}, scope)
	provider := &ResponsesProvider{codexWSConn: conn}
	_, _, err = provider.codexWSExecuteRequestLocked(ctx, "sample-key", "test-model", codexWSResponseCreate{Type: "response.create", Model: "test-model"}, nil, false, time.Now(), true, nil, "", false)
	scope.finish(ctx, nil, err, "")
	snapshot, ok := store.Latest()
	if err == nil || !ok || snapshot.State != contextdiag.Failed || snapshot.ID.Attempt != 1 || snapshot.Usage.State != contextdiag.Missing {
		t.Fatalf("write failure=%v snapshot=%+v", err, snapshot)
	}
}
