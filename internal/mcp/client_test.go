package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fakeTransport — in-memory Transport for unit tests
// ---------------------------------------------------------------------------

// testClientInfo is the application metadata used by all unit tests when
// constructing MCP clients or managers.
var testClientInfo = ClientInfo{Name: "chord-test", Version: "test"}

// fakeTransport is a test double that records requests and returns
// pre-configured responses.
type fakeTransport struct {
	mu         sync.Mutex
	responses  map[string]json.RawMessage // method → result JSON
	requests   []JSONRPCRequest
	notifs     []JSONRPCNotification
	closed     bool
	sendErrs   map[string][]error
	notifyErrs []error
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{
		responses: make(map[string]json.RawMessage),
		sendErrs:  make(map[string][]error),
	}
}

func (f *fakeTransport) onMethod(method string, result any) {
	data, _ := json.Marshal(result)
	f.mu.Lock()
	f.responses[method] = data
	f.mu.Unlock()
}

func (f *fakeTransport) onSendError(method string, errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendErrs[method] = append([]error(nil), errs...)
}

func (f *fakeTransport) Send(_ context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, req)
	if errs := f.sendErrs[req.Method]; len(errs) > 0 {
		err := errs[0]
		f.sendErrs[req.Method] = errs[1:]
		return JSONRPCResponse{}, err
	}

	result, ok := f.responses[req.Method]
	if !ok {
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &JSONRPCError{Code: -32601, Message: "method not found"},
		}, nil
	}

	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}, nil
}

func (f *fakeTransport) Notify(_ context.Context, notif JSONRPCNotification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notifs = append(f.notifs, notif)
	if len(f.notifyErrs) > 0 {
		err := f.notifyErrs[0]
		f.notifyErrs = f.notifyErrs[1:]
		return err
	}
	return nil
}

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeTransport) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeTransport) notifCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.notifs)
}

// ---------------------------------------------------------------------------
// Client tests
// ---------------------------------------------------------------------------

func TestClient_Initialize(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities:    map[string]any{},
		ServerInfo: struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{Name: "test-server", Version: "1.0"},
	})

	client := NewClientWithInfo("test", ft, testClientInfo)
	ctx := context.Background()

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	if client.ServerName() != "test-server" {
		t.Errorf("ServerName() = %q, want %q", client.ServerName(), "test-server")
	}

	// Should have sent one request and one notification.
	if ft.requestCount() != 1 {
		t.Errorf("request count = %d, want 1", ft.requestCount())
	}
	if ft.notifCount() != 1 {
		t.Errorf("notification count = %d, want 1", ft.notifCount())
	}
}

func TestClient_ListTools(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{
			{Name: "read_file", Description: "Read a file", InputSchema: map[string]any{"type": "object"}},
			{Name: "write_file", Description: "Write a file", InputSchema: map[string]any{"type": "object"}},
		},
	})

	client := NewClientWithInfo("test", ft, testClientInfo)
	ctx := context.Background()
	_ = client.Initialize(ctx)

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("ListTools returned %d tools, want 2", len(tools))
	}
	if tools[0].Name != "read_file" {
		t.Errorf("tools[0].Name = %q, want %q", tools[0].Name, "read_file")
	}
	if tools[1].Name != "write_file" {
		t.Errorf("tools[1].Name = %q, want %q", tools[1].Name, "write_file")
	}
}

func TestClient_CallTool(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/call", toolCallResult{
		Content: []toolCallContent{
			{Type: "text", Text: "hello world"},
		},
	})

	client := NewClientWithInfo("test", ft, testClientInfo)
	ctx := context.Background()
	_ = client.Initialize(ctx)

	result, err := client.CallTool(ctx, "greet", json.RawMessage(`{"name":"user"}`))
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result != "hello world" {
		t.Errorf("CallTool result = %q, want %q", result, "hello world")
	}
}

func TestClient_CallTool_MultipleContent(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/call", toolCallResult{
		Content: []toolCallContent{
			{Type: "text", Text: "line1"},
			{Type: "image", Text: "ignored"},
			{Type: "text", Text: "line2"},
		},
	})

	client := NewClientWithInfo("test", ft, testClientInfo)
	ctx := context.Background()
	_ = client.Initialize(ctx)

	result, err := client.CallTool(ctx, "multi", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result != "line1\nline2" {
		t.Errorf("CallTool result = %q, want %q", result, "line1\nline2")
	}
}

func TestClient_CallTool_Error(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/call", toolCallResult{
		IsError: true,
		Content: []toolCallContent{
			{Type: "text", Text: "file not found"},
		},
	})

	client := NewClientWithInfo("test", ft, testClientInfo)
	ctx := context.Background()
	_ = client.Initialize(ctx)

	_, err := client.CallTool(ctx, "bad", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("CallTool should have returned an error")
	}
	if got := err.Error(); got != "mcp tool error: file not found" {
		t.Errorf("error = %q, want to contain 'file not found'", got)
	}
}

func TestClient_AllocID_Monotonic(t *testing.T) {
	ft := newFakeTransport()
	client := NewClientWithInfo("test", ft, testClientInfo)

	ids := make(map[int]bool)
	for i := 0; i < 100; i++ {
		id := client.allocID()
		if ids[id] {
			t.Fatalf("duplicate ID: %d", id)
		}
		ids[id] = true
	}
}

func TestClient_Close(t *testing.T) {
	ft := newFakeTransport()
	client := NewClientWithInfo("test", ft, testClientInfo)

	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !ft.closed {
		t.Error("transport was not closed")
	}
}

// ---------------------------------------------------------------------------
// MCPTool (bridge) tests
// ---------------------------------------------------------------------------

func TestMCPTool_Interface(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{
			{Name: "test_tool", Description: "A test tool", InputSchema: map[string]any{"type": "object"}},
		},
	})

	client := NewClientWithInfo("test-server", ft, testClientInfo)
	ctx := context.Background()
	_ = client.Initialize(ctx)

	discovered, err := DiscoverTools(ctx, client)
	if err != nil {
		t.Fatalf("DiscoverTools failed: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("DiscoverTools returned %d tools, want 1", len(discovered))
	}

	tool := discovered[0]
	wantName := RegisteredMCPToolName("test-server", "test_tool")
	if tool.Name() != wantName {
		t.Errorf("Name() = %q, want %q", tool.Name(), wantName)
	}
	if tool.Description() != "A test tool" {
		t.Errorf("Description() = %q, want %q", tool.Description(), "A test tool")
	}
	if tool.IsReadOnly() != false {
		t.Error("IsReadOnly() should be false for MCP tools")
	}
	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters() should not be nil")
	}
}

func TestMCPTool_Execute(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{
			{Name: "echo", Description: "Echo tool", InputSchema: map[string]any{"type": "object"}},
		},
	})
	ft.onMethod("tools/call", toolCallResult{
		Content: []toolCallContent{
			{Type: "text", Text: "echoed: test"},
		},
	})

	client := NewClientWithInfo("test", ft, testClientInfo)
	ctx := context.Background()
	_ = client.Initialize(ctx)

	discovered, _ := DiscoverTools(ctx, client)
	tool := discovered[0]

	result, err := tool.Execute(ctx, json.RawMessage(`{"msg":"test"}`))
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result != "echoed: test" {
		t.Errorf("Execute result = %q, want %q", result, "echoed: test")
	}
}

func TestDiscoverTools_SkipsEmptyName(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{
			{Name: "", Description: "Bad tool", InputSchema: nil},
			{Name: "good_tool", Description: "Good tool", InputSchema: map[string]any{"type": "object"}},
		},
	})

	client := NewClientWithInfo("test", ft, testClientInfo)
	ctx := context.Background()
	_ = client.Initialize(ctx)

	tools, err := DiscoverTools(ctx, client)
	if err != nil {
		t.Fatalf("DiscoverTools failed: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (skipped empty name), got %d", len(tools))
	}
	want := RegisteredMCPToolName("test", "good_tool")
	if tools[0].Name() != want {
		t.Errorf("tool name = %q, want %q", tools[0].Name(), want)
	}
}

func TestDiscoverAllToolsFiltersAllowedTools(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{
			{Name: "alpha_tool", Description: "Search"},
			{Name: "beta_tool", Description: "Fetch"},
			{Name: "legacy_tool", Description: "Legacy"},
		},
	})

	ctx := context.Background()
	cfgs := []ServerConfig{{Name: "search", URL: "https://mcp.test/mcp", AllowedTools: []string{"alpha_tool", "beta_tool"}}}
	mgr := NewPendingManagerWithClientInfo(cfgs, testClientInfo)
	mgr.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		client := NewClientWithInfo("search", ft, testClientInfo)
		return client, client.Initialize(ctx)
	}
	mgr.ConnectAll(ctx, cfgs)

	discovered, err := DiscoverAllTools(ctx, mgr)
	if err != nil {
		t.Fatalf("DiscoverAllTools: %v", err)
	}
	if len(discovered) != 2 {
		t.Fatalf("got %d discovered tools, want 2", len(discovered))
	}
	gotNames := []string{discovered[0].Name(), discovered[1].Name()}
	sort.Strings(gotNames)
	wantNames := []string{"mcp_search_alpha_tool", "mcp_search_beta_tool"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("tool names = %#v, want %#v", gotNames, wantNames)
	}
	cached := mgr.CachedToolDefs("search")
	if len(cached) != 2 {
		t.Fatalf("cached defs = %#v, want 2 allowed tools", cached)
	}
	cachedNames := []string{cached[0].Name, cached[1].Name}
	sort.Strings(cachedNames)
	if !reflect.DeepEqual(cachedNames, []string{"alpha_tool", "beta_tool"}) {
		t.Fatalf("cached names = %#v, want allowed tools", cachedNames)
	}
}

func TestDiscoverAllToolsWithoutAllowedToolsKeepsAllTools(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{
			{Name: "alpha_tool", Description: "Search"},
			{Name: "beta_tool", Description: "Fetch"},
		},
	})

	ctx := context.Background()
	cfgs := []ServerConfig{{Name: "search", URL: "https://mcp.test/mcp"}}
	mgr := NewPendingManagerWithClientInfo(cfgs, testClientInfo)
	mgr.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		client := NewClientWithInfo("search", ft, testClientInfo)
		return client, client.Initialize(ctx)
	}
	mgr.ConnectAll(ctx, cfgs)

	discovered, err := DiscoverAllTools(ctx, mgr)
	if err != nil {
		t.Fatalf("DiscoverAllTools: %v", err)
	}
	if len(discovered) != 2 {
		t.Fatalf("got %d discovered tools, want all 2", len(discovered))
	}
}

func TestConnectAllRefreshesAllowedTools(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{
			{Name: "alpha_tool", Description: "Search"},
			{Name: "beta_tool", Description: "Fetch"},
		},
	})

	ctx := context.Background()
	initialCfgs := []ServerConfig{{Name: "search", URL: "https://mcp.test/mcp", AllowedTools: []string{"alpha_tool"}}}
	mgr := NewPendingManagerWithClientInfo(initialCfgs, testClientInfo)
	mgr.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		client := NewClientWithInfo("search", ft, testClientInfo)
		return client, client.Initialize(ctx)
	}
	mgr.ConnectAll(ctx, []ServerConfig{{Name: "search", URL: "https://mcp.test/mcp", AllowedTools: []string{"beta_tool"}}})

	discovered, err := DiscoverAllTools(ctx, mgr)
	if err != nil {
		t.Fatalf("DiscoverAllTools: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("got %d discovered tools, want 1", len(discovered))
	}
	if got, want := discovered[0].Name(), "mcp_search_beta_tool"; got != want {
		t.Fatalf("tool name = %q, want %q", got, want)
	}
	cached := mgr.CachedToolDefs("search")
	if len(cached) != 1 || cached[0].Name != "beta_tool" {
		t.Fatalf("cached defs = %#v, want only refreshed allowed tool", cached)
	}
}

// ---------------------------------------------------------------------------
// HTTPTransport tests (using httptest)
// ---------------------------------------------------------------------------

func TestHTTPTransport_Send(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
		}

		switch req.Method {
		case "initialize":
			result, _ := json.Marshal(initializeResult{
				ProtocolVersion: "2024-11-05",
			})
			resp.Result = result
		case "tools/list":
			result, _ := json.Marshal(toolsListResult{
				Tools: []MCPToolDef{
					{Name: "http_tool", Description: "HTTP test tool"},
				},
			})
			resp.Result = result
		default:
			resp.Error = &JSONRPCError{Code: -32601, Message: "not found"}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	transport := NewHTTPTransport(server.URL)
	client := NewClientWithInfo("http-test", transport, testClientInfo)
	ctx := context.Background()

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "http_tool" {
		t.Errorf("unexpected tools: %+v", tools)
	}
}

func TestHTTPTransport_SSE_Stream(t *testing.T) {
	var sessionOnSecond bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Session-Id") == "sess-sse-1" {
			sessionOnSecond = true
		}
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "sess-sse-1")
		}
		resp := JSONRPCResponse{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			b, _ := json.Marshal(initializeResult{ProtocolVersion: "2024-11-05"})
			resp.Result = b
		case "tools/list":
			b, _ := json.Marshal(toolsListResult{Tools: []MCPToolDef{{Name: "from_sse", Description: "ok"}}})
			resp.Result = b
		default:
			resp.Error = &JSONRPCError{Code: -32601, Message: "unknown"}
		}
		line, _ := json.Marshal(resp)
		_, _ = fmt.Fprintf(w, "event: message\r\ndata: %s\n\n", line)
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	transport := NewHTTPTransport(server.URL)
	client := NewClientWithInfo("sse-test", transport, testClientInfo)
	ctx := context.Background()

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := transport.Notify(ctx, JSONRPCNotification{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "from_sse" {
		t.Fatalf("tools: %+v", tools)
	}
	if !sessionOnSecond {
		t.Error("expected Mcp-Session-Id on request after initialize")
	}
}

func TestHTTPTransport_ContextCancellation(t *testing.T) {
	// Use a channel to unblock the handler when the test is done.
	done := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until test signals completion.
		<-done
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(handler)
	defer func() {
		close(done)
		server.Close()
	}()

	transport := NewHTTPTransport(server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := transport.Send(ctx, JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "test",
	})
	if err == nil {
		t.Fatal("expected error due to context cancellation")
	}
}

func TestHTTPTransport_ServerError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	transport := NewHTTPTransport(server.URL)
	ctx := context.Background()

	_, err := transport.Send(ctx, JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "test",
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestManagerConnectAll_RetriesTransientInitializeErrorAndRecovers(t *testing.T) {
	cfg := ServerConfig{Name: "remote", URL: "https://mcp.test/mcp"}
	mgr := NewPendingManagerWithClientInfo([]ServerConfig{cfg}, testClientInfo)

	attempts := 0
	mgr.newClientFactory = func(_ context.Context, cfg ServerConfig) (*Client, error) {
		attempts++
		ft := newFakeTransport()
		ft.onMethod("initialize", initializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities:    map[string]any{},
			ServerInfo: struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}{Name: cfg.Name, Version: "1.0"},
		})
		if attempts == 1 {
			ft.onSendError("initialize", fmt.Errorf("mcp http: send request: net/http: TLS handshake timeout"))
		}
		return NewClientWithInfo(cfg.Name, ft, testClientInfo), nil
	}

	mgr.ConnectAll(context.Background(), []ServerConfig{cfg})

	if attempts != 2 {
		fatalfAttempts(t, attempts, 2)
	}
	if got := mgr.ServerNames(); len(got) != 1 || got[0] != "remote" {
		t.Fatalf("ServerNames() = %v, want [remote]", got)
	}
	status := onlyEndpointStatus(t, mgr)
	if !status.OK || status.Pending || status.Retrying {
		t.Fatalf("final status = %+v, want OK=true Pending=false Retrying=false", status)
	}
	if status.Attempt != 2 || status.MaxAttempts != defaultConnectAttempts {
		t.Fatalf("final attempts = %d/%d, want 2/%d", status.Attempt, status.MaxAttempts, defaultConnectAttempts)
	}
	if status.Error != "" {
		t.Fatalf("final error = %q, want empty", status.Error)
	}
}

func TestManagerConnectAll_CanceledInitializeStaysPending(t *testing.T) {
	cfg := ServerConfig{Name: "remote", URL: "https://mcp.test/mcp"}
	mgr := NewPendingManagerWithClientInfo([]ServerConfig{cfg}, testClientInfo)
	mgr.newClientFactory = func(_ context.Context, cfg ServerConfig) (*Client, error) {
		ft := newFakeTransport()
		ft.onSendError("initialize", context.Canceled)
		return NewClientWithInfo(cfg.Name, ft, testClientInfo), nil
	}

	mgr.ConnectAll(context.Background(), []ServerConfig{cfg})

	if got := mgr.ServerNames(); len(got) != 0 {
		t.Fatalf("ServerNames() = %v, want none", got)
	}
	status := onlyEndpointStatus(t, mgr)
	if !status.Pending || status.OK || status.Retrying {
		t.Fatalf("final status = %+v, want pending canceled state", status)
	}
	if status.Error != "" {
		t.Fatalf("canceled status error = %q, want empty", status.Error)
	}
	if status.Attempt != 1 || status.MaxAttempts != defaultConnectAttempts {
		t.Fatalf("attempts = %d/%d, want 1/%d", status.Attempt, status.MaxAttempts, defaultConnectAttempts)
	}
}

func TestManagerConnectAll_FinalFailureStopsRetryingAndSetsError(t *testing.T) {
	cfg := ServerConfig{Name: "remote", URL: "https://mcp.test/mcp"}
	mgr := NewPendingManagerWithClientInfo([]ServerConfig{cfg}, testClientInfo)

	attempts := 0
	mgr.newClientFactory = func(_ context.Context, cfg ServerConfig) (*Client, error) {
		attempts++
		ft := newFakeTransport()
		ft.onMethod("initialize", initializeResult{})
		ft.onSendError("initialize", fmt.Errorf("mcp http: send request: net/http: TLS handshake timeout"))
		return NewClientWithInfo(cfg.Name, ft, testClientInfo), nil
	}

	mgr.ConnectAll(context.Background(), []ServerConfig{cfg})

	if attempts != defaultConnectAttempts {
		fatalfAttempts(t, attempts, defaultConnectAttempts)
	}
	if got := mgr.ServerNames(); len(got) != 0 {
		t.Fatalf("ServerNames() = %v, want none", got)
	}
	status := onlyEndpointStatus(t, mgr)
	if status.OK || status.Pending || status.Retrying {
		t.Fatalf("final status = %+v, want hard failure", status)
	}
	if status.Attempt != defaultConnectAttempts || status.MaxAttempts != defaultConnectAttempts {
		t.Fatalf("attempts = %d/%d, want %d/%d", status.Attempt, status.MaxAttempts, defaultConnectAttempts, defaultConnectAttempts)
	}
	if !strings.Contains(status.Error, "TLS handshake timeout") {
		t.Fatalf("final error = %q, want TLS handshake timeout", status.Error)
	}
}

func TestShouldRetryConnectErrorSkipsClientHTTPStatuses(t *testing.T) {
	cfg := ServerConfig{Name: "remote", URL: "https://mcp.test/mcp"}
	for _, status := range []int{400, 401, 403, 404} {
		err := fmt.Errorf("mcp http: send request: server returned %d", status)
		if shouldRetryConnectError(err, cfg) {
			t.Fatalf("shouldRetryConnectError(%d) = true, want false", status)
		}
	}
}

func TestExtractHTTPStatusFromError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
		ok   bool
	}{
		{err: fmt.Errorf("mcp http: send request: server returned 503"), want: 503, ok: true},
		{err: &JSONRPCError{Code: 404, Message: "not found"}, want: 404, ok: true},
		{err: fmt.Errorf("tls handshake timeout"), want: 0, ok: false},
	} {
		got, ok := extractHTTPStatusFromError(tc.err)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("extractHTTPStatusFromError(%v) = (%d, %v), want (%d, %v)", tc.err, got, ok, tc.want, tc.ok)
		}
	}
}

func onlyEndpointStatus(t *testing.T, mgr *Manager) ServerEndpointStatus {
	t.Helper()
	statuses := mgr.ServerEndpoints()
	if len(statuses) != 1 {
		t.Fatalf("ServerEndpoints() len = %d, want 1 (%+v)", len(statuses), statuses)
	}
	return statuses[0]
}

func fatalfAttempts(t *testing.T, got, want int) {
	t.Helper()
	t.Fatalf("attempt count = %d, want %d", got, want)
}

// ---------------------------------------------------------------------------
// JSON-RPC types tests
// ---------------------------------------------------------------------------

func TestJSONRPCError_Error(t *testing.T) {
	e := &JSONRPCError{Code: -32600, Message: "invalid request"}
	want := "JSON-RPC error -32600: invalid request"
	if got := e.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestJSONRPCRequest_Marshal(t *testing.T) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  map[string]any{"key": "value"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", decoded["jsonrpc"])
	}
	if decoded["method"] != "initialize" {
		t.Errorf("method = %v, want initialize", decoded["method"])
	}
}

// ---------------------------------------------------------------------------
// Concurrent ID allocation test
// ---------------------------------------------------------------------------

func TestClient_AllocID_Concurrent(t *testing.T) {
	ft := newFakeTransport()
	client := NewClientWithInfo("test", ft, testClientInfo)

	const goroutines = 10
	const idsPerGoroutine = 100

	var mu sync.Mutex
	allIDs := make(map[int]bool)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			localIDs := make([]int, 0, idsPerGoroutine)
			for i := 0; i < idsPerGoroutine; i++ {
				localIDs = append(localIDs, client.allocID())
			}
			mu.Lock()
			for _, id := range localIDs {
				if allIDs[id] {
					t.Errorf("duplicate ID: %d", id)
				}
				allIDs[id] = true
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	expected := goroutines * idsPerGoroutine
	if len(allIDs) != expected {
		t.Errorf("got %d unique IDs, want %d", len(allIDs), expected)
	}
}

// ---------------------------------------------------------------------------
// Integration-style: Client + MCPTool full workflow
// ---------------------------------------------------------------------------

func TestFullWorkflow_FakeTransport(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo: struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{Name: "fake-mcp", Version: "0.1"},
	})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{
			{
				Name:        "list_files",
				Description: "List files in a directory",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []string{"path"},
				},
			},
		},
	})
	ft.onMethod("tools/call", toolCallResult{
		Content: []toolCallContent{
			{Type: "text", Text: "file1.txt\nfile2.txt\nfile3.txt"},
		},
	})

	ctx := context.Background()
	client := NewClientWithInfo("test-server", ft, testClientInfo)

	// Step 1: Initialize
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Step 2: Discover tools
	discovered, err := DiscoverTools(ctx, client)
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(discovered))
	}

	tool := discovered[0]
	want := RegisteredMCPToolName("test-server", "list_files")
	if tool.Name() != want {
		t.Errorf("tool name = %q, want %q", tool.Name(), want)
	}

	// Step 3: Execute tool
	result, err := tool.Execute(ctx, json.RawMessage(`{"path": "/tmp"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	expected := "file1.txt\nfile2.txt\nfile3.txt"
	if result != expected {
		t.Errorf("result = %q, want %q", result, expected)
	}

	// Step 4: Close
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify all expected calls were made.
	// initialize + tools/list + tools/call = 3 requests
	if ft.requestCount() != 3 {
		t.Errorf("total requests = %d, want 3", ft.requestCount())
	}

	// 1 initialized notification
	if ft.notifCount() != 1 {
		t.Errorf("total notifications = %d, want 1", ft.notifCount())
	}

	t.Log("Full MCP workflow test passed")
}
