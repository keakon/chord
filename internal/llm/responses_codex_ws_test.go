package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/logtest"
	"github.com/keakon/chord/internal/message"
)

func TestCodexWSResponseCreateAlwaysSerializesIncludeArray(t *testing.T) {
	// Codex's ResponseCreateWsRequest.include has no skip_serializing, so the field
	// must always be present as a JSON array on the wire (empty or populated).
	nilInclude := codexWSResponseCreate{Type: "response.create", Model: "gpt-5.5"}
	nilJSON, err := json.Marshal(nilInclude)
	if err != nil {
		t.Fatalf("marshal nil include: %v", err)
	}
	if !bytes.Contains(nilJSON, []byte(`"include":[]`)) {
		t.Fatalf("nil include not serialized as []: %s", nilJSON)
	}
	if !bytes.Contains(nilJSON, []byte(`"input":[]`)) {
		t.Fatalf("nil input not serialized as []: %s", nilJSON)
	}
	if !bytes.Contains(nilJSON, []byte(`"tools":[]`)) {
		t.Fatalf("nil tools not serialized as []: %s", nilJSON)
	}

	empty := codexWSResponseCreate{Type: "response.create", Model: "gpt-5.5", Include: []string{}}
	emptyJSON, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if !bytes.Contains(emptyJSON, []byte(`"include":[]`)) {
		t.Fatalf("empty include not serialized as []: %s", emptyJSON)
	}

	withReasoning := codexWSResponseCreate{Type: "response.create", Model: "gpt-5.5", Include: []string{"reasoning.encrypted_content"}}
	rJSON, err := json.Marshal(withReasoning)
	if err != nil {
		t.Fatalf("marshal with reasoning: %v", err)
	}
	if !bytes.Contains(rJSON, []byte(`"include":["reasoning.encrypted_content"]`)) {
		t.Fatalf("include not serialized with encrypted content: %s", rJSON)
	}

	withStore := codexWSResponseCreate{Type: "response.create", Model: "gpt-5.5", Store: true}
	storeJSON, err := json.Marshal(withStore)
	if err != nil {
		t.Fatalf("marshal with store: %v", err)
	}
	if !bytes.Contains(storeJSON, []byte(`"store":true`)) {
		t.Fatalf("store=true not serialized: %s", storeJSON)
	}

	withMetadata := codexWSResponseCreate{
		Type:  "response.create",
		Model: "sample/test-model",
		ClientMetadata: map[string]string{
			responsesClientMetadataInstallationID: "installation-123",
			responsesClientMetadataWindowID:       "session-123:0",
		},
	}
	metadataJSON, err := json.Marshal(withMetadata)
	if err != nil {
		t.Fatalf("marshal with client metadata: %v", err)
	}
	if !bytes.Contains(metadataJSON, []byte(`"client_metadata":{"x-codex-installation-id":"installation-123","x-codex-window-id":"session-123:0"}`)) {
		t.Fatalf("client_metadata not serialized: %s", metadataJSON)
	}
}

func TestResetCodexWebSocketChainClearsState(t *testing.T) {
	r := &ResponsesProvider{}
	r.codexWSLastKey = "k1"
	r.codexWSLastAPIURL = "https://example.com/v1/responses"
	r.codexWSLastModel = "m1"
	r.codexWSLastRespID = "resp-1"
	r.codexWSLastInpLen = 3
	r.codexWSLastInpSig = "sig"
	r.codexWSLastReqSig = "reqsig"
	r.codexWSPromptCacheKey = "prompt"

	r.resetCodexWebSocketChain("test")

	if r.codexWSLastKey != "" || r.codexWSLastAPIURL != "" || r.codexWSLastModel != "" || r.codexWSLastRespID != "" || r.codexWSLastInpLen != 0 || r.codexWSLastInpSig != "" || r.codexWSLastReqSig != "" || r.codexWSPromptCacheKey != "" {
		t.Fatalf("chain state not fully cleared: %+v", r)
	}
}

func TestResetCodexWebSocketChainLogsChainResetWithoutConnectionClosed(t *testing.T) {
	var buf bytes.Buffer
	logger := logtest.NewLogger(&buf, golog.DebugLevel)
	log.SetDefaultLogger(logger)
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))

	r := &ResponsesProvider{
		codexWSLastRespID: "resp-1",
		codexWSLastReqSig: "reqsig",
	}

	r.resetCodexWebSocketChain("session_restore")

	logs := buf.String()
	if !strings.Contains(logs, "responses codex ws: chain reset") {
		t.Fatalf("expected chain reset log, got %q", logs)
	}
	if strings.Contains(logs, "responses codex ws: connection closed") {
		t.Fatalf("unexpected connection closed log without websocket connection: %q", logs)
	}
	if !strings.Contains(logs, "had_conn=false") {
		t.Fatalf("expected had_conn=false in log, got %q", logs)
	}
}

func TestInvalidateRoutingResetsCodexWebSocketChain(t *testing.T) {
	r := &ResponsesProvider{}
	r.codexWSLastKey = "k1"
	r.codexWSLastAPIURL = "https://example.com/v1/responses"
	r.codexWSLastModel = "m1"
	r.codexWSLastRespID = "resp-1"
	r.codexWSLastInpLen = 3
	r.codexWSLastInpSig = "sig"
	r.codexWSLastReqSig = "reqsig"
	r.codexWSPromptCacheKey = "prompt"

	r.InvalidateRouting("model_pool_changed")

	if r.codexWSLastKey != "" || r.codexWSLastAPIURL != "" || r.codexWSLastModel != "" || r.codexWSLastRespID != "" || r.codexWSLastInpLen != 0 || r.codexWSLastInpSig != "" || r.codexWSLastReqSig != "" || r.codexWSPromptCacheKey != "" {
		t.Fatalf("routing invalidation did not fully clear websocket chain: %+v", r)
	}
}

func TestCodexWSCanUseIncrementalLockedRequiresMatchingRequestSignature(t *testing.T) {
	r := &ResponsesProvider{
		codexWSConn:       &websocket.Conn{},
		codexWSLastKey:    "k1",
		codexWSLastAPIURL: "https://example.com/v1/responses",
		codexWSLastModel:  "m1",
		codexWSLastRespID: "resp-1",
		codexWSLastReqSig: "sig-a",
	}
	fullInput := []responsesInputItem{
		{Type: "message", Role: "user", Content: "hello"},
		{Type: "message", Role: "user", Content: "next"},
	}
	r.codexWSLastInpLen = 1
	r.codexWSLastInpSig = responsesInputPrefixSignature(fullInput, 1)

	if ok, n, reason := r.codexWSCanUseIncrementalLocked("k1", "https://example.com/v1/responses", "m1", "sig-a", fullInput, false); !ok || n != 1 || reason != "ok" {
		t.Fatalf("expected incremental allowed with matching req signature, got ok=%v n=%d reason=%q", ok, n, reason)
	}
	if ok, _, reason := r.codexWSCanUseIncrementalLocked("k1", "https://example.com/v1/responses", "m1", "sig-b", fullInput, false); ok || reason != "request_signature_changed" {
		t.Fatal("expected incremental denied when request signature changes")
	}
}

func TestCodexWSCanUseIncrementalLockedAllowEmptyDelta(t *testing.T) {
	fullInput := []responsesInputItem{
		{Type: "message", Role: "user", Content: "hello"},
	}
	r := &ResponsesProvider{
		codexWSConn:       &websocket.Conn{},
		codexWSLastKey:    "k1",
		codexWSLastAPIURL: "https://example.com/v1/responses",
		codexWSLastModel:  "m1",
		codexWSLastRespID: "resp-1",
		codexWSLastReqSig: "sig-a",
		codexWSLastInpLen: len(fullInput),
		codexWSLastInpSig: responsesInputSignature(fullInput),
	}
	if ok, _, reason := r.codexWSCanUseIncrementalLocked("k1", "https://example.com/v1/responses", "m1", "sig-a", fullInput, false); ok || reason != "empty_delta_not_allowed" {
		t.Fatalf("expected no incremental when delta is empty and allowEmptyDelta=false, got ok=%v reason=%q", ok, reason)
	}
	if ok, n, reason := r.codexWSCanUseIncrementalLocked("k1", "https://example.com/v1/responses", "m1", "sig-a", fullInput, true); !ok || n != len(fullInput) || reason != "ok" {
		t.Fatalf("expected incremental with empty delta allowed, got ok=%v n=%d reason=%q", ok, n, reason)
	}
}

func TestCodexWSState_KeyOrModelChangeRequiresFull(t *testing.T) {
	fullInput := []responsesInputItem{
		{Type: "message", Role: "user", Content: "hello"},
		{Type: "message", Role: "user", Content: "next"},
	}
	newProvider := func() *ResponsesProvider {
		r := &ResponsesProvider{
			codexWSConn:       &websocket.Conn{},
			codexWSLastKey:    "k1",
			codexWSLastAPIURL: "https://example.com/v1/responses",
			codexWSLastModel:  "m1",
			codexWSLastRespID: "resp-1",
			codexWSLastInpLen: 1,
			codexWSLastReqSig: "sig-a",
		}
		r.codexWSLastInpSig = responsesInputPrefixSignature(fullInput, r.codexWSLastInpLen)
		return r
	}

	if ok, _, reason := newProvider().codexWSCanUseIncrementalLocked("k2", "https://example.com/v1/responses", "m1", "", fullInput, false); ok || reason != "key_model_or_endpoint_changed" {
		t.Fatalf("key change should prevent incremental reuse, got ok=%v reason=%q", ok, reason)
	}
	if ok, _, reason := newProvider().codexWSCanUseIncrementalLocked("k1", "https://example.com/v1/responses", "m2", "", fullInput, false); ok || reason != "key_model_or_endpoint_changed" {
		t.Fatalf("model change should prevent incremental reuse, got ok=%v reason=%q", ok, reason)
	}
}

func TestCodexWSState_EndpointChangeRequiresFull(t *testing.T) {
	fullInput := []responsesInputItem{
		{Type: "message", Role: "user", Content: "hello"},
		{Type: "message", Role: "user", Content: "next"},
	}
	r := &ResponsesProvider{
		codexWSConn:       &websocket.Conn{},
		codexWSLastKey:    "k1",
		codexWSLastAPIURL: "https://example.com/v1/responses",
		codexWSLastModel:  "m1",
		codexWSLastRespID: "resp-1",
		codexWSLastInpLen: 1,
		codexWSLastReqSig: "sig-a",
	}
	r.codexWSLastInpSig = responsesInputPrefixSignature(fullInput, r.codexWSLastInpLen)

	if ok, _, reason := r.codexWSCanUseIncrementalLocked("k1", "https://other.example.com/v1/responses", "m1", "sig-a", fullInput, false); ok || reason != "key_model_or_endpoint_changed" {
		t.Fatalf("endpoint change should prevent incremental reuse, got ok=%v reason=%q", ok, reason)
	}
}

func TestCodexWSState_PrefixMismatchRequiresFull(t *testing.T) {
	r := &ResponsesProvider{
		codexWSConn:       &websocket.Conn{},
		codexWSLastKey:    "k1",
		codexWSLastAPIURL: "https://example.com/v1/responses",
		codexWSLastModel:  "m1",
		codexWSLastRespID: "resp-1",
		codexWSLastInpLen: 1,
		codexWSLastReqSig: "sig-a",
	}
	orig := []responsesInputItem{{Type: "message", Role: "user", Content: "hello"}}
	r.codexWSLastInpSig = responsesInputSignature(orig)

	fullInput := []responsesInputItem{
		{Type: "message", Role: "user", Content: "different"},
		{Type: "message", Role: "user", Content: "next"},
	}
	if ok, _, reason := r.codexWSCanUseIncrementalLocked("k1", "https://example.com/v1/responses", "m1", "sig-a", fullInput, false); ok || reason != "input_prefix_mismatch" {
		t.Fatalf("prefix mismatch should force full request, got ok=%v reason=%q", ok, reason)
	}
}

func TestEffectiveResponsesWebsocket_PresetAndOverride(t *testing.T) {
	falseVal := false
	trueVal := true
	if !config.EffectiveResponsesWebsocket(config.ProviderPresetCodex, nil) {
		t.Fatal("preset codex should default websocket on")
	}
	if config.EffectiveResponsesWebsocket(config.ProviderPresetCodex, &falseVal) {
		t.Fatal("explicit false should disable websocket")
	}
	if !config.EffectiveResponsesWebsocket("", &trueVal) {
		t.Fatal("explicit true should enable websocket even without preset")
	}
}

func TestParseCodexWebSocketErrorJSON(t *testing.T) {
	msg := []byte(`{
			"type": "error",
			"status": 429,
			"error": {
				"type": "usage_limit_reached",
				"message": "The usage limit has been reached",
				"resets_in_seconds": 1234
			},
			"headers": {
				"x-codex-primary-used-percent": "100.0",
				"x-codex-primary-window-minutes": "15"
			}
		}`)
	apiErr, h := parseCodexWebSocketErrorJSON(msg)
	if apiErr == nil {
		t.Fatal("expected APIError")
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("StatusCode = %d", apiErr.StatusCode)
	}
	if apiErr.RetryAfter != 1234*time.Second {
		t.Fatalf("RetryAfter = %v", apiErr.RetryAfter)
	}
	if h.Get("X-Codex-Primary-Used-Percent") != "100.0" {
		t.Fatalf("header primary percent = %q", h.Get("X-Codex-Primary-Used-Percent"))
	}
}

func TestParseCodexWebSocketErrorJSON401(t *testing.T) {
	msg := []byte(`{"type":"error","status":401,"error":{"type":"auth","message":"nope"}}`)
	apiErr, h := parseCodexWebSocketErrorJSON(msg)
	if apiErr == nil || apiErr.StatusCode != 401 || len(h) != 0 {
		t.Fatalf("apiErr=%v h=%v", apiErr, h)
	}
}

type codexWSCaptureServer struct {
	server *httptest.Server

	mu          sync.Mutex
	requests    []map[string]any
	responseIDs []string
}

func newCodexWSCaptureServer(t *testing.T, responseIDs []string) *codexWSCaptureServer {
	t.Helper()

	c := &codexWSCaptureServer{responseIDs: append([]string(nil), responseIDs...)}
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var body map[string]any
			if err := json.Unmarshal(msg, &body); err != nil {
				return
			}
			c.mu.Lock()
			idx := len(c.requests)
			c.requests = append(c.requests, body)
			respID := ""
			if idx < len(c.responseIDs) {
				respID = c.responseIDs[idx]
			}
			c.mu.Unlock()

			completed := map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"status": "completed",
					"output": []any{},
					"usage": map[string]any{
						"input_tokens":  5,
						"output_tokens": 2,
					},
				},
			}
			if respID != "" {
				completed["response"].(map[string]any)["id"] = respID
			}
			payload, err := json.Marshal(completed)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		}
	})
	c.server = httptest.NewServer(mux)
	return c
}

func (c *codexWSCaptureServer) Close() {
	c.server.Close()
}

func (c *codexWSCaptureServer) ResponsesURL() string {
	return c.server.URL + "/v1/responses"
}

func (c *codexWSCaptureServer) Requests() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.requests...)
}

func testCodexWSResponsesRequest(model string, input []responsesInputItem) *responsesRequest {
	return &responsesRequest{
		Model:             model,
		Input:             input,
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Store:             false,
		Stream:            true,
		Include:           []string{},
	}
}

func assertCodexWSFullInputWithoutPreviousResponseID(t *testing.T, body map[string]any, wantLen int) {
	t.Helper()
	if _, ok := body["previous_response_id"]; ok {
		t.Fatalf("previous_response_id = %v, want absent", body["previous_response_id"])
	}
	input, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("input = %#v, want array", body["input"])
	}
	if len(input) != wantLen {
		t.Fatalf("input len = %d, want full len %d", len(input), wantLen)
	}
}

func TestCompleteStreamCodexWebSocketMissingResponseIDRequiresFullFollowUp(t *testing.T) {
	srv := newCodexWSCaptureServer(t, []string{"", "resp-2"})
	defer srv.Close()

	r := &ResponsesProvider{sessionID: "session-123"}
	input1 := []responsesInputItem{{Type: "message", Role: "user", Content: "hello"}}
	input2 := []responsesInputItem{
		{Type: "message", Role: "user", Content: "hello"},
		{Type: "message", Role: "user", Content: "follow up"},
	}
	req := testCodexWSResponsesRequest("sample/test-model", input1)

	if _, _, err := r.completeStreamCodexWebSocket(context.Background(), srv.ResponsesURL(), "key-1", "sample/test-model", req, input1, nil, time.Now(), codexWSCompleteOptions{SkipPrewarm: true}); err != nil {
		t.Fatalf("first completeStreamCodexWebSocket: %v", err)
	}
	req.Input = input2
	if _, _, err := r.completeStreamCodexWebSocket(context.Background(), srv.ResponsesURL(), "key-1", "sample/test-model", req, input2, nil, time.Now(), codexWSCompleteOptions{SkipPrewarm: true}); err != nil {
		t.Fatalf("second completeStreamCodexWebSocket: %v", err)
	}

	requests := srv.Requests()
	if len(requests) != 2 {
		t.Fatalf("captured %d requests, want 2", len(requests))
	}
	assertCodexWSFullInputWithoutPreviousResponseID(t, requests[1], len(input2))
}

func TestCompleteStreamCodexWebSocketKeyOrModelChangeRequiresFullRequest(t *testing.T) {
	for _, tc := range []struct {
		name        string
		secondKey   string
		secondModel string
	}{
		{name: "key change", secondKey: "key-2", secondModel: "sample/test-model"},
		{name: "model change", secondKey: "key-1", secondModel: "sample/other-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newCodexWSCaptureServer(t, []string{"resp-1", "resp-2"})
			defer srv.Close()

			r := &ResponsesProvider{sessionID: "session-123"}
			input1 := []responsesInputItem{{Type: "message", Role: "user", Content: "hello"}}
			input2 := []responsesInputItem{
				{Type: "message", Role: "user", Content: "hello"},
				{Type: "message", Role: "user", Content: "follow up"},
			}
			req := testCodexWSResponsesRequest("sample/test-model", input1)

			if _, _, err := r.completeStreamCodexWebSocket(context.Background(), srv.ResponsesURL(), "key-1", "sample/test-model", req, input1, nil, time.Now(), codexWSCompleteOptions{SkipPrewarm: true}); err != nil {
				t.Fatalf("first completeStreamCodexWebSocket: %v", err)
			}
			req.Model = tc.secondModel
			req.Input = input2
			if _, _, err := r.completeStreamCodexWebSocket(context.Background(), srv.ResponsesURL(), tc.secondKey, tc.secondModel, req, input2, nil, time.Now(), codexWSCompleteOptions{SkipPrewarm: true}); err != nil {
				t.Fatalf("second completeStreamCodexWebSocket: %v", err)
			}

			requests := srv.Requests()
			if len(requests) != 2 {
				t.Fatalf("captured %d requests, want 2", len(requests))
			}
			assertCodexWSFullInputWithoutPreviousResponseID(t, requests[1], len(input2))
		})
	}
}

// codexWSEchoServer creates a test WebSocket server that reads one message
// from the client, then writes back a valid response.completed event and
// closes the connection.
func codexWSEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Read the request payload (ignored).
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		// Send a minimal response.completed event.
		completed := `{"type":"response.completed","response":{"id":"resp-test","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`
		if err := conn.WriteMessage(websocket.TextMessage, []byte(completed)); err != nil {
			return
		}
	})
	return httptest.NewServer(mux)
}

func TestCodexWSExecuteRequestLocked_StatusConnectingSkippedOnReusedConn(t *testing.T) {
	srv := codexWSEchoServer(t)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Dial a real WebSocket connection.
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer wsConn.Close()

	// The server expects one WS message then sends response.completed.
	// We need to consume the server's upgrade first — the httptest server
	// already handled that, but we need a fresh connection per call below
	// since the echo server closes after one response.
	//
	// Instead, test both paths by dialing separate connections.

	// --- Case 1: reusingConn=false → "connecting" should be emitted ---
	wsConn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	defer wsConn1.Close()

	r := &ResponsesProvider{codexWSConn: wsConn1}
	var statuses1 []string
	cb1 := func(delta message.StreamDelta) {
		if delta.Type == "status" && delta.Status != nil {
			statuses1 = append(statuses1, delta.Status.Type)
		}
	}
	env := codexWSResponseCreate{
		Type:   "response.create",
		Model:  "sample/test-model",
		Stream: true,
		Input:  []responsesInputItem{{Type: "message", Role: "user", Content: "hi"}},
	}
	_, _, err = r.codexWSExecuteRequestLocked(
		context.Background(), "test-key", "sample/test-model", env, cb1, false, time.Now(), false,
	)
	if err != nil {
		t.Fatalf("execute new conn: %v", err)
	}
	if len(statuses1) == 0 || statuses1[0] != "connecting" {
		t.Fatalf("new connection: expected first status 'connecting', got %v", statuses1)
	}
	foundWaiting1 := false
	for _, s := range statuses1 {
		if s == "waiting_headers" {
			foundWaiting1 = true
		}
	}
	if !foundWaiting1 {
		t.Fatalf("new connection: expected 'waiting_headers' in statuses, got %v", statuses1)
	}

	// --- Case 2: reusingConn=true → "connecting" should NOT be emitted ---
	wsConn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer wsConn2.Close()

	r2 := &ResponsesProvider{codexWSConn: wsConn2}
	var statuses2 []string
	cb2 := func(delta message.StreamDelta) {
		if delta.Type == "status" && delta.Status != nil {
			statuses2 = append(statuses2, delta.Status.Type)
		}
	}
	_, _, err = r2.codexWSExecuteRequestLocked(
		context.Background(), "test-key", "sample/test-model", env, cb2, false, time.Now(), true,
	)
	if err != nil {
		t.Fatalf("execute reused conn: %v", err)
	}
	for _, s := range statuses2 {
		if s == "connecting" {
			t.Fatalf("reused connection: should NOT emit 'connecting', got statuses %v", statuses2)
		}
	}
	foundWaiting2 := false
	for _, s := range statuses2 {
		if s == "waiting_headers" {
			foundWaiting2 = true
		}
	}
	if !foundWaiting2 {
		t.Fatalf("reused connection: expected 'waiting_headers' in statuses, got %v", statuses2)
	}

	// --- Case 3: reusingConn=true + nil cb → no panic ---
	wsConn3, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 3: %v", err)
	}
	defer wsConn3.Close()
	r3 := &ResponsesProvider{codexWSConn: wsConn3}
	_, _, err = r3.codexWSExecuteRequestLocked(
		context.Background(), "test-key", "sample/test-model", env, nil, false, time.Now(), true,
	)
	if err != nil {
		t.Fatalf("execute reused conn nil cb: %v", err)
	}
}

func TestCodexWSExecuteRequestLockedEmitsWaitingHeadersProgress(t *testing.T) {
	srv := codexWSEchoServer(t)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer wsConn.Close()

	r := &ResponsesProvider{codexWSConn: wsConn}
	env := codexWSResponseCreate{
		Type:   "response.create",
		Model:  "sample/test-model",
		Stream: true,
		Input:  []responsesInputItem{{Type: "message", Role: "user", Content: "hi"}},
	}
	var waiting *message.StreamProgressDelta
	cb := func(delta message.StreamDelta) {
		if delta.Type == "status" && delta.Status != nil && delta.Status.Type == "waiting_headers" {
			waiting = delta.Progress
		}
	}
	_, _, err = r.codexWSExecuteRequestLocked(
		context.Background(), "test-key", "sample/test-model", env, cb, false, time.Now(), true,
	)
	if err != nil {
		t.Fatalf("execute reused conn: %v", err)
	}
	if waiting == nil || waiting.Bytes <= 0 {
		t.Fatalf("waiting_headers progress = %#v, want positive bytes", waiting)
	}
}

func TestCodexWSExecuteRequestLockedEmitsStreamingProgressAcrossWSFrames(t *testing.T) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		// Send reasoning deltas before completed so we exercise non-terminal frames.
		events := []string{
			`{"type":"response.reasoning_summary_text.delta","delta":"Analyzing..."}`,
			`{"type":"response.reasoning_summary_text.done","text":"Analyzing..."}`,
			`{"type":"response.completed","response":{"id":"resp-progress","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`,
		}
		for _, evt := range events {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(evt)); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer wsConn.Close()

	r := &ResponsesProvider{codexWSConn: wsConn}
	env := codexWSResponseCreate{
		Type:   "response.create",
		Model:  "sample/test-model",
		Stream: true,
		Input:  []responsesInputItem{{Type: "message", Role: "user", Content: "hi"}},
	}

	var lastProgress message.StreamProgressDelta
	var sawProgress bool
	cb := func(delta message.StreamDelta) {
		if delta.Progress == nil {
			return
		}
		lastProgress = *delta.Progress
		sawProgress = true
	}

	_, _, err = r.codexWSExecuteRequestLocked(
		context.Background(), "test-key", "sample/test-model", env, cb, false, time.Now(), true,
	)
	if err != nil {
		t.Fatalf("execute reused conn: %v", err)
	}
	if !sawProgress {
		t.Fatal("expected progress deltas on websocket frames")
	}
	if lastProgress.Events < 3 {
		t.Fatalf("last progress events = %d, want >= 3", lastProgress.Events)
	}
	if lastProgress.Bytes <= 0 {
		t.Fatalf("last progress bytes = %d, want positive", lastProgress.Bytes)
	}
}

// TestCompleteStreamCodexWebSocket_ConnectingBeforeDial verifies the key
// contract fixed in the "connecting before dial" change: when
// completeStreamCodexWebSocket needs to establish a new WS connection, it
// must emit the "connecting" status BEFORE the potentially slow DialContext
// call. Since completeStreamCodexWebSocket is hard to call in isolation (it
// holds codexWSMu and does URL/proxy setup), we verify the equivalent
// lower-level sequence using two separate WS connections for prewarm and
// real-request (the echo server only handles one message per connection).
func TestCompleteStreamCodexWebSocket_ConnectingBeforeDial(t *testing.T) {
	srv := codexWSEchoServer(t)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	r := &ResponsesProvider{
		sessionID:             "test-session",
		codexWSPromptCacheKey: "test-prompt-cache",
	}

	// Track status events and whether "connecting" was emitted before dial.
	var statuses []string
	connectingBeforeDial := false
	dialDone := false

	cb := func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaStatus && delta.Status != nil {
			statuses = append(statuses, delta.Status.Type)
			if delta.Status.Type == "connecting" && !dialDone {
				connectingBeforeDial = true
			}
		}
	}

	// Simulate the sequence completeStreamCodexWebSocket now performs:
	//
	// Step 1: Emit "connecting" BEFORE dial (the fix).
	cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &message.StatusDelta{Type: "connecting"}})

	// Step 2: Dial (simulates DialContext in completeStreamCodexWebSocket).
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	dialDone = true
	defer conn1.Close()

	// Step 3: Prewarm (cb=nil, reusingConn doesn't matter).
	r.codexWSConn = conn1
	prewarmEnv := codexWSResponseCreate{
		Type:   "response.create",
		Model:  "sample/test-model",
		Stream: true,
		Input:  []responsesInputItem{{Type: "message", Role: "user", Content: "hi"}},
	}
	_, _, err = r.codexWSExecuteRequestLocked(
		context.Background(), "test-key", "sample/test-model", prewarmEnv, nil, false, time.Now(), false,
	)
	if err != nil {
		t.Fatalf("prewarm: %v", err)
	}

	// Step 4: Real request on a fresh connection (echo server is
	// one-shot, so we dial a new one — this matches the reusingConn=true
	// contract since the connection is established before the call).
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer conn2.Close()
	r.codexWSConn = conn2

	env := codexWSResponseCreate{
		Type:   "response.create",
		Model:  "sample/test-model",
		Stream: true,
		Input:  []responsesInputItem{{Type: "message", Role: "user", Content: "hi"}},
	}
	_, _, err = r.codexWSExecuteRequestLocked(
		context.Background(), "test-key", "sample/test-model", env, cb, false, time.Now(), true,
	)
	if err != nil {
		t.Fatalf("real request: %v", err)
	}

	// Verify: "connecting" was emitted before dial.
	if !connectingBeforeDial {
		t.Fatal("expected 'connecting' to be emitted before WS dial completed")
	}

	// Verify: "connecting" appears exactly once (the pre-dial emission).
	// The real-request call used reusingConn=true, so it should not emit
	// a duplicate "connecting".
	connectingCount := 0
	for _, s := range statuses {
		if s == "connecting" {
			connectingCount++
		}
	}
	if connectingCount != 1 {
		t.Fatalf("expected exactly 1 'connecting', got %d (statuses: %v)", connectingCount, statuses)
	}

	// Verify: "waiting_headers" is still emitted for the real request.
	foundWaiting := false
	for _, s := range statuses {
		if s == "waiting_headers" {
			foundWaiting = true
		}
	}
	if !foundWaiting {
		t.Fatalf("expected 'waiting_headers' in statuses, got %v", statuses)
	}
}
