package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

// blockingTransport records the first Send and then blocks until the request
// context is done, simulating a hung server.
type blockingTransport struct {
	entered chan struct{}
}

func (b *blockingTransport) Send(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	<-ctx.Done()
	return JSONRPCResponse{}, ctx.Err()
}

func (b *blockingTransport) Notify(context.Context, JSONRPCNotification) error { return nil }
func (b *blockingTransport) Close() error                                      { return nil }

// deadlineProbeTransport reports whether the context CallTool handed down
// carried a deadline, then fails the call immediately.
type deadlineProbeTransport struct {
	hasDeadline chan bool
}

func (p *deadlineProbeTransport) Send(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
	_, ok := ctx.Deadline()
	p.hasDeadline <- ok
	return JSONRPCResponse{}, context.Canceled
}

func (p *deadlineProbeTransport) Notify(context.Context, JSONRPCNotification) error { return nil }
func (p *deadlineProbeTransport) Close() error                                      { return nil }

func TestClientCallToolDeadlineSettlesUnknown(t *testing.T) {
	client := NewClientWithInfo("slow", &blockingTransport{entered: make(chan struct{})}, testClientInfo)
	client.callTimeout = 50 * time.Millisecond

	_, _, err := client.CallTool(context.Background(), "slow_tool", nil)
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	// The agent classifies through classifyCallFailure; unrecognized failures
	// must land on the conservative callOutcomeUnknown.
	if outcome := classifyCallFailure(err); outcome != callOutcomeUnknown {
		t.Fatalf("outcome = %d, want callOutcomeUnknown", outcome)
	}
}

// A configured deadline must reach the transport's Send context; without one,
// CallTool must hand down the caller's context untouched so the transport
// preset (HTTP 120s, stdio unlimited) stays the only deadline.
func TestClientCallToolDeadlineReachesSendContext(t *testing.T) {
	for _, tc := range []struct {
		name        string
		callTimeout time.Duration
		want        bool
	}{
		{"unconfigured", 0, false},
		{"configured", 45 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &deadlineProbeTransport{hasDeadline: make(chan bool, 1)}
			client := NewClientWithInfo("probe", probe, testClientInfo)
			client.callTimeout = tc.callTimeout

			client.CallTool(context.Background(), "probe_tool", nil)
			select {
			case got := <-probe.hasDeadline:
				if got != tc.want {
					t.Fatalf("Send ctx deadline = %v, want %v", got, tc.want)
				}
			default:
				t.Fatal("Send was not called")
			}
		})
	}
}

func TestServerCallTimeoutValidation(t *testing.T) {
	unconfigured := ServerConfig{Name: "srv"}
	if d, err := unconfigured.CallTimeout(); err != nil || d != 0 {
		t.Fatalf("CallTimeout(0) = %v, %v; want 0, nil", d, err)
	}
	configured := ServerConfig{Name: "srv", TimeoutMs: 45_000}
	if d, err := configured.CallTimeout(); err != nil || d != 45*time.Second {
		t.Fatalf("CallTimeout(45000) = %v, %v; want 45s, nil", d, err)
	}
	for _, ms := range []int{-1, maxToolCallTimeoutMs + 1} {
		invalid := ServerConfig{Name: "srv", TimeoutMs: ms}
		_, err := invalid.CallTimeout()
		if err == nil || !strings.Contains(err.Error(), "invalid timeout_ms") {
			t.Fatalf("CallTimeout(%d) error = %v, want invalid timeout_ms message", ms, err)
		}
	}
}

func TestCreateClientRejectsInvalidTimeout(t *testing.T) {
	for _, cfg := range []ServerConfig{
		{Name: "srv", Command: "sample-tool", TimeoutMs: -1},
		{Name: "srv", URL: "https://example.invalid/mcp", TimeoutMs: maxToolCallTimeoutMs + 1},
	} {
		_, err := createClient(context.Background(), cfg, testClientInfo)
		if err == nil || !strings.Contains(err.Error(), "invalid timeout_ms") {
			t.Fatalf("createClient(%+v) error = %v, want invalid timeout_ms message", cfg, err)
		}
	}
}

func TestNewHTTPTransportToolCallTimeout(t *testing.T) {
	const endpoint = "https://example.invalid/mcp"

	tr := NewHTTPTransport(HTTPTransportOptions{Endpoint: endpoint})
	if tr.client.Timeout != 120*time.Second {
		t.Fatalf("default Timeout = %v, want 120s", tr.client.Timeout)
	}

	// A configured tool deadline is separate from the HTTP client preset, so
	// the timeout does not extend initialization and tools/list requests.
	tr = NewHTTPTransport(HTTPTransportOptions{Endpoint: endpoint, ToolCallTimeout: 5 * time.Minute})
	if tr.client.Timeout != 120*time.Second {
		t.Fatalf("tool-call client Timeout = %v, want unchanged 120s preset", tr.client.Timeout)
	}
	if tr.toolCallTimeout != 5*time.Minute {
		t.Fatalf("tool-call timeout = %v, want 5m", tr.toolCallTimeout)
	}

	shared := &http.Client{Timeout: 30 * time.Second}
	tr = NewHTTPTransport(HTTPTransportOptions{Endpoint: endpoint, Client: shared})
	if tr.client.Timeout != 30*time.Second {
		t.Fatalf("shared-client Timeout = %v, want 30s", tr.client.Timeout)
	}
	if tr.client == shared {
		t.Fatal("transport must not alias the shared auth client")
	}

	tr = NewHTTPTransport(HTTPTransportOptions{Endpoint: endpoint, Client: shared, ToolCallTimeout: 45 * time.Second})
	if tr.client.Timeout != 30*time.Second {
		t.Fatalf("shared-client Timeout = %v, want preserved 30s", tr.client.Timeout)
	}
	if shared.Timeout != 30*time.Second {
		t.Fatalf("shared client Timeout = %v, want unchanged 30s", shared.Timeout)
	}
	if tr.toolCallTimeout != 45*time.Second {
		t.Fatalf("shared transport tool-call timeout = %v, want 45s", tr.toolCallTimeout)
	}
}

func TestCreateTargetClientAppliesToolCallTimeout(t *testing.T) {
	target := Target{Config: ServerConfig{Name: "remote", URL: "https://example.invalid/mcp", Auth: config.MCPAuthNone, TimeoutMs: 300_000}}
	client, err := createTargetClient(context.Background(), target, nil, testClientInfo, ExpectedIdentity{}, 0)
	if err != nil {
		t.Fatalf("createTargetClient: %v", err)
	}
	transport, ok := client.transport.(*HTTPTransport)
	if !ok {
		t.Fatalf("transport type = %T, want *HTTPTransport", client.transport)
	}
	if client.callTimeout != 5*time.Minute {
		t.Fatalf("client call timeout = %v, want 5m", client.callTimeout)
	}
	if transport.toolCallTimeout != 5*time.Minute {
		t.Fatalf("transport tool-call timeout = %v, want 5m", transport.toolCallTimeout)
	}
	if transport.client.Timeout != 120*time.Second {
		t.Fatalf("transport client timeout = %v, want 120s handshake preset", transport.client.Timeout)
	}
}

func TestCreateTargetClientToolCallTimeoutExpires(t *testing.T) {
	srv := slowJSONRPCServer(2 * time.Second)
	defer srv.Close()
	target := Target{Config: ServerConfig{Name: "remote", URL: srv.URL, Auth: config.MCPAuthNone, TimeoutMs: 100}}
	client, err := createTargetClient(context.Background(), target, nil, testClientInfo, ExpectedIdentity{}, 0)
	if err != nil {
		t.Fatalf("createTargetClient: %v", err)
	}
	began := time.Now()
	_, _, err = client.CallTool(context.Background(), "slow_tool", nil)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("call took %v, want the configured deadline", elapsed)
	}
	if outcome := classifyCallFailure(err); outcome != callOutcomeUnknown {
		t.Fatalf("outcome = %d, want callOutcomeUnknown", outcome)
	}
}

// slowJSONRPCServer answers tools/call after a fixed delay, long enough for
// any configured deadline below it to fire first.
func slowJSONRPCServer(delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"late"}]}}`))
	}))
}

// TestClientCallToolHTTPTimeoutExpires proves the configured per-server
// deadline actually cuts an HTTP call short and settles it as outcome-unknown:
// a slower server reply never arrives, and the preset never clamps the
// configured value (with wiring broken the call would succeed after the
// server's 2s delay instead).
func TestClientCallToolHTTPTimeoutExpires(t *testing.T) {
	srv := slowJSONRPCServer(2 * time.Second)
	defer srv.Close()

	for _, tc := range []struct {
		name             string
		callTimeout      time.Duration
		transportTimeout time.Duration
	}{
		{name: "client-call-deadline", callTimeout: 100 * time.Millisecond, transportTimeout: 100 * time.Millisecond},
		{name: "transport-call-deadline", transportTimeout: 100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewHTTPTransport(HTTPTransportOptions{Endpoint: srv.URL, ToolCallTimeout: tc.transportTimeout})
			client := NewClientWithInfo("slow", tr, testClientInfo)
			client.callTimeout = tc.callTimeout

			began := time.Now()
			_, _, err := client.CallTool(context.Background(), "slow_tool", nil)
			if err == nil {
				t.Fatal("expected timeout error, got nil")
			}
			if elapsed := time.Since(began); elapsed > time.Second {
				t.Fatalf("call took %v, want the 100ms configured deadline", elapsed)
			}
			// A transport timeout carries no JSON-RPC receipt and must not be
			// read as callNotSent: the request may have reached the server.
			var urlErr *url.Error
			if !errors.As(err, &urlErr) || !urlErr.Timeout() {
				t.Fatalf("error = %v, want a url.Error with Timeout()==true", err)
			}
			if outcome := classifyCallFailure(err); outcome != callOutcomeUnknown {
				t.Fatalf("outcome = %d, want callOutcomeUnknown", outcome)
			}
		})
	}
}
