package mcp

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestClassifyCallFailure(t *testing.T) {
	jsonErr := &JSONRPCError{Code: -32603, Message: "boom"}
	cases := []struct {
		name string
		err  error
		want callOutcome
	}{
		{name: "plain", err: errors.New("transport failed"), want: callOutcomeUnknown},
		{name: "jsonrpc", err: jsonErr, want: callReportedError},
		{name: "wrapped jsonrpc", err: fmt.Errorf("call: %w", jsonErr), want: callReportedError},
		{name: "not sent", err: newTransportFailure("not sent", SendNotSent, errors.New("x")), want: callNotSent},
		{name: "response received", err: newTransportFailure("bad body", SendResponseReceived, io.ErrUnexpectedEOF), want: callOutcomeUnknown},
		{name: "explicit unknown wins", err: newCallFailure(callOutcomeUnknown, jsonErr), want: callOutcomeUnknown},
		{name: "explicit reported wins", err: newCallFailure(callReportedError, errors.New("invalid params")), want: callReportedError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCallFailure(tc.err); got != tc.want {
				t.Fatalf("classifyCallFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func requireCallOutcome(t *testing.T, err error, want callOutcome) {
	t.Helper()
	got, ok := callOutcomeOf(err)
	if !ok || got != want {
		t.Fatalf("outcome = %v, %v; err = %v", got, ok, err)
	}
}

func TestClientCallToolClassifiesReportedErrors(t *testing.T) {
	t.Run("jsonrpc error", func(t *testing.T) {
		ft := newFakeTransport()
		client := NewClientWithInfo("test", ft, testClientInfo)
		// No registered tools/call result: the transport reports method-not-found.

		_, _, err := client.CallTool(context.Background(), "search", json.RawMessage(`{}`))
		requireCallOutcome(t, err, callReportedError)
	})

	t.Run("isError result", func(t *testing.T) {
		ft := newFakeTransport()
		client := NewClientWithInfo("test", ft, testClientInfo)
		ft.onMethod("tools/call", toolCallResult{
			IsError: true,
			Content: []toolCallContent{{Type: "text", Text: new("quota exceeded")}},
		})

		_, _, err := client.CallTool(context.Background(), "search", json.RawMessage(`{}`))
		if !strings.Contains(err.Error(), "quota exceeded") {
			t.Fatalf("error lost server detail: %v", err)
		}
		requireCallOutcome(t, err, callReportedError)
	})
}

func TestClientCallToolClassifiesMalformedResultsAsUnknown(t *testing.T) {
	t.Run("invalid args are not sent", func(t *testing.T) {
		ft := newFakeTransport()
		client := NewClientWithInfo("test", ft, testClientInfo)

		_, _, err := client.CallTool(context.Background(), "search", json.RawMessage(`{`))
		requireCallOutcome(t, err, callNotSent)
		if ft.requestCount() != 0 {
			t.Fatalf("invalid args reached the transport: %d requests", ft.requestCount())
		}
	})

	t.Run("non-object result", func(t *testing.T) {
		ft := newFakeTransport()
		client := NewClientWithInfo("test", ft, testClientInfo)
		ft.mu.Lock()
		ft.responses["tools/call"] = json.RawMessage(`[1,2,3]`)
		ft.mu.Unlock()

		_, _, err := client.CallTool(context.Background(), "search", json.RawMessage(`{}`))
		requireCallOutcome(t, err, callOutcomeUnknown)
	})

	t.Run("undecodable result", func(t *testing.T) {
		ft := newFakeTransport()
		client := NewClientWithInfo("test", ft, testClientInfo)
		ft.mu.Lock()
		ft.responses["tools/call"] = json.RawMessage(`{"content":"not-an-array"}`)
		ft.mu.Unlock()

		_, _, err := client.CallTool(context.Background(), "search", json.RawMessage(`{}`))
		requireCallOutcome(t, err, callOutcomeUnknown)
	})
}

func TestMCPToolExecuteExposesExternalOutcome(t *testing.T) {
	tool := &MCPTool{
		handle:     &ExecutionHandle{serverName: "local", remoteName: "search"},
		serverName: "local",
		remoteName: "search",
	}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	outcome, ok := tools.ExternalCallOutcomeOf(err)
	if !ok || outcome != tools.ExternalCallNotSent {
		t.Fatalf("outcome = %q, %v; err = %v", outcome, ok, err)
	}
	if !strings.Contains(err.Error(), "local/search") {
		t.Fatalf("error lost tool identity: %v", err)
	}
}

// newHTTPCallToolClient builds a client whose HTTP transport endpoints are
// served by handler and whose calls expect ID 1 (the first request issued by
// this client).
func newHTTPCallToolClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	transport := NewHTTPTransport(HTTPTransportOptions{Endpoint: endpoint})
	t.Cleanup(func() { _ = transport.Close() })
	return NewClientWithInfo("http-test", transport, testClientInfo)
}

func TestHTTPCallToolMalformedReceiptIsOutcomeUnknownOnce(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// The server executed the call, then the receipt was truncated.
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[`, req.ID)
	}))
	defer server.Close()

	client := newHTTPCallToolClient(t, server.URL)
	_, _, err := client.CallTool(context.Background(), "search", json.RawMessage(`{}`))
	requireCallOutcome(t, err, callOutcomeUnknown)
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("business requests = %d, want 1", requests)
	}
}

func TestHTTPCallToolMismatchedReceiptIDIsOutcomeUnknown(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"ok"}]}}`, req.ID+1)
	}))
	defer server.Close()

	client := newHTTPCallToolClient(t, server.URL)
	_, _, err := client.CallTool(context.Background(), "search", json.RawMessage(`{}`))
	requireCallOutcome(t, err, callOutcomeUnknown)
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("business requests = %d, want 1", requests)
	}
}

func TestHTTPCallToolReportedJSONRPCErrorIsNotReplayed(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"server rejected"}}`, req.ID)
	}))
	defer server.Close()

	client := newHTTPCallToolClient(t, server.URL)
	_, _, err := client.CallTool(context.Background(), "search", json.RawMessage(`{}`))
	requireCallOutcome(t, err, callReportedError)
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("business requests = %d, want 1", requests)
	}
}

func TestHTTPCallToolInvalidEndpointIsNotSent(t *testing.T) {
	client := newHTTPCallToolClient(t, "http://[::1")
	_, _, err := client.CallTool(context.Background(), "search", json.RawMessage(`{}`))
	requireCallOutcome(t, err, callNotSent)
}

func TestStdioCallToolExitBeforeReceiptIsOutcomeUnknownOnce(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "received.ndjson")
	script := filepath.Join(dir, "no-reply.sh")
	content := "#!/bin/sh\nIFS= read -r line\nprintf '%s\\n' \"$line\" > \"$CHORD_TEST_MARKER\"\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	tr, err := NewStdioTransport(context.Background(), script, nil, []string{"CHORD_TEST_MARKER=" + marker})
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	defer tr.Close()
	client := NewClientWithInfo("stdio-test", tr, testClientInfo)

	_, _, callErr := client.CallTool(context.Background(), "search", json.RawMessage(`{}`))
	requireCallOutcome(t, callErr, callOutcomeUnknown)

	received, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("server side never received the request: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(received)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"tools/call"`) {
		t.Fatalf("server received %q, want exactly one tools/call", received)
	}

	select {
	case <-tr.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stdio reader did not exit")
	}
	// After EOF every further request is provably unsent; the closed pipe
	// cannot hide a delivered request.
	if _, err := tr.Send(context.Background(), JSONRPCRequest{JSONRPC: "2.0", ID: 99, Method: "tools/call"}); err == nil {
		t.Fatal("Send after EOF succeeded")
	} else {
		requireCallOutcome(t, err, callNotSent)
	}
}

func TestStdioCallToolCancelBeforeWriteIsNotSent(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "received.ndjson")
	script := filepath.Join(dir, "drain.sh")
	content := "#!/bin/sh\nwhile IFS= read -r line; do printf '%s\\n' \"$line\" >> \"$CHORD_TEST_MARKER\"; done\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	tr, err := NewStdioTransport(context.Background(), script, nil, []string{"CHORD_TEST_MARKER=" + marker})
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	defer tr.Close()
	client := NewClientWithInfo("stdio-test", tr, testClientInfo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, callErr := client.CallTool(ctx, "search", json.RawMessage(`{}`))
	requireCallOutcome(t, callErr, callNotSent)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("server side received a request despite pre-write cancellation: %v", err)
	}
}

// writeOutcome describes one scripted write to the transport's stdin.
type writeOutcome struct {
	written int
	err     error
}

// scriptedWriteCloser returns preconfigured results for successive writes and
// signals each completed write on wrote.
type scriptedWriteCloser struct {
	mu      sync.Mutex
	results []writeOutcome
	calls   int
	wrote   chan struct{}
	onWrite func([]byte)
}

func (w *scriptedWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	index := w.calls
	w.calls++
	var result writeOutcome
	if index < len(w.results) {
		result = w.results[index]
	} else {
		result = writeOutcome{written: len(p)}
	}
	w.mu.Unlock()
	if w.onWrite != nil {
		w.onWrite(p)
	}
	if w.wrote != nil {
		w.wrote <- struct{}{}
	}
	return result.written, result.err
}

func (w *scriptedWriteCloser) Close() error { return nil }

func newScriptedTransport(writer io.WriteCloser) *StdioTransport {
	return &StdioTransport{
		stdin:   writer,
		pending: make(map[int]chan stdioReply),
		done:    make(chan struct{}),
	}
}

func TestStdioSendClassifiesWriteFailures(t *testing.T) {
	t.Run("zero bytes written is not sent", func(t *testing.T) {
		tr := newScriptedTransport(&scriptedWriteCloser{results: []writeOutcome{{err: errors.New("broken pipe")}}})
		_, err := tr.Send(context.Background(), JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"})
		requireCallOutcome(t, err, callNotSent)
		if len(tr.pending) != 0 {
			t.Fatalf("pending waiter leaked: %d", len(tr.pending))
		}
	})

	t.Run("partial write is unknown", func(t *testing.T) {
		tr := newScriptedTransport(&scriptedWriteCloser{results: []writeOutcome{{written: 3}}})
		_, err := tr.Send(context.Background(), JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"})
		requireCallOutcome(t, err, callOutcomeUnknown)
		if len(tr.pending) != 0 {
			t.Fatalf("pending waiter leaked: %d", len(tr.pending))
		}
	})

	t.Run("full write without receipt is unknown", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writer := &scriptedWriteCloser{wrote: make(chan struct{}, 1)}
		tr := newScriptedTransport(writer)
		go func() {
			<-writer.wrote
			cancel()
		}()
		_, err := tr.Send(ctx, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"})
		requireCallOutcome(t, err, callOutcomeUnknown)
		if len(tr.pending) != 0 {
			t.Fatalf("pending waiter leaked: %d", len(tr.pending))
		}
	})
}

func TestStdioSendReceiptWinsOverCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var tr *StdioTransport
	writer := &scriptedWriteCloser{onWrite: func([]byte) {
		// Deliver the receipt before the cancellation is observable so the
		// select sees both cases ready; the receipt must still win.
		tr.mu.Lock()
		ch := tr.pending[7]
		delete(tr.pending, 7)
		tr.mu.Unlock()
		ch <- stdioReply{resp: JSONRPCResponse{JSONRPC: "2.0", ID: 7, Result: json.RawMessage(`{"content":[]}`)}}
		cancel()
	}}
	tr = newScriptedTransport(writer)

	resp, err := tr.Send(ctx, JSONRPCRequest{JSONRPC: "2.0", ID: 7, Method: "tools/call"})
	if err != nil {
		t.Fatalf("Send returned error despite a complete receipt: %v", err)
	}
	if resp.ID != 7 {
		t.Fatalf("response ID = %d, want 7", resp.ID)
	}
}

func TestStdioSendCancellationIsPerCall(t *testing.T) {
	var tr *StdioTransport
	writer := &scriptedWriteCloser{wrote: make(chan struct{}, 2)}
	tr = newScriptedTransport(writer)

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()

	type result struct {
		resp JSONRPCResponse
		err  error
	}
	aCh := make(chan result, 1)
	go func() {
		resp, err := tr.Send(ctxA, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"})
		aCh <- result{resp: resp, err: err}
	}()
	bCh := make(chan result, 1)
	go func() {
		resp, err := tr.Send(context.Background(), JSONRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/call"})
		bCh <- result{resp: resp, err: err}
	}()

	for range 2 {
		select {
		case <-writer.wrote:
		case <-time.After(5 * time.Second):
			t.Fatal("sends did not write")
		}
	}

	tr.mu.Lock()
	chB := tr.pending[2]
	delete(tr.pending, 2)
	tr.mu.Unlock()
	chB <- stdioReply{resp: JSONRPCResponse{JSONRPC: "2.0", ID: 2, Result: json.RawMessage(`{"content":[]}`)}}
	cancelA()

	select {
	case got := <-aCh:
		requireCallOutcome(t, got.err, callOutcomeUnknown)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled call did not return")
	}
	select {
	case got := <-bCh:
		if got.err != nil {
			t.Fatalf("unrelated call failed: %v", got.err)
		}
		if got.resp.ID != 2 {
			t.Fatalf("unrelated call received response %d", got.resp.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unrelated call did not return")
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.pending) != 0 {
		t.Fatalf("pending waiters leaked: %d", len(tr.pending))
	}
}
