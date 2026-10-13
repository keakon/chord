package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

func managerFixture(t *testing.T, manual bool, tr Transport) (*Manager, *Catalog, Target) {
	t.Helper()
	target := testTarget(ServerConfig{Name: "sample", URL: "https://example.invalid/mcp", Manual: manual})
	manager := NewPendingManagerWithClientInfo([]Target{target}, testClientInfo, nil)
	manager.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		return NewClientWithInfo("sample", tr, testClientInfo), nil
	}
	catalog := NewCatalog(manager)
	if manual {
		catalog.SetDesiredEnabled("sample", true)
	}
	t.Cleanup(manager.Close)
	return manager, catalog, target
}
func readyFakeTransport() *fakeTransport {
	tr := newFakeTransport()
	tr.onMethod("initialize", initializeResult{ProtocolVersion: protocolVersion})
	tr.onMethod("tools/list", toolsListResult{Tools: []MCPToolDef{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}})
	tr.onMethod("tools/call", toolCallResult{Content: []toolCallContent{{Type: "text", Text: "ok"}}})
	return tr
}

func TestCandidateCannotInstallAfterDisable(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	base := readyFakeTransport()
	tr := &scriptedMCPTransport{fakeTransport: base}
	tr.send = func(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
		if req.Method == "initialize" {
			close(entered)
			<-release
		}
		return base.Send(ctx, req)
	}
	mgr, cat, target := managerFixture(t, true, tr)
	done := make(chan error, 1)
	go func() { done <- mgr.ConnectOne(t.Context(), target) }()
	<-entered
	cat.SetDesiredEnabled("sample", false)
	before := mgr.Snapshot()
	close(release)
	if err := <-done; diagnoseAuth(err).ErrorClass != ErrorIdentityChanged {
		t.Fatalf("late candidate: %v", err)
	}
	after := mgr.Snapshot()
	if after.Version != before.Version || after.Servers[0].Connection != ConnectionDisabled || mgr.Client("sample") != nil {
		t.Fatalf("late candidate changed current status: %+v", after)
	}
	base.mu.Lock()
	closed := base.closed
	base.mu.Unlock()
	if !closed {
		t.Fatal("rejected candidate was not closed")
	}
}

func TestDisableWaitsForAdmittedLease(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	base := readyFakeTransport()
	tr := &scriptedMCPTransport{fakeTransport: base}
	tr.send = func(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
		if req.Method == "tools/call" {
			close(entered)
			<-release
		}
		return base.Send(ctx, req)
	}
	mgr, cat, target := managerFixture(t, true, tr)
	if err := mgr.ConnectOne(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	wrapped, err := cat.DiscoverAllTools(t.Context())
	if err != nil || len(wrapped) != 1 {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := wrapped[0].Execute(t.Context(), json.RawMessage(`{}`)); done <- err }()
	<-entered
	cat.SetDesiredEnabled("sample", false)
	if _, err := wrapped[0].Execute(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("new admission: %v", err)
	}
	base.mu.Lock()
	closed := base.closed
	base.mu.Unlock()
	if closed {
		t.Fatal("disable closed an admitted call")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	base.mu.Lock()
	closed = base.closed
	base.mu.Unlock()
	if !closed {
		t.Fatal("last release did not close retired client")
	}
}

func TestHandleRejectsChangedSchemaAndIdentity(t *testing.T) {
	for _, identityChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "schema", true: "identity"}[identityChange], func(t *testing.T) {
			base := readyFakeTransport()
			mgr, cat, target := managerFixture(t, false, base)
			if err := mgr.ConnectOne(t.Context(), target); err != nil {
				t.Fatal(err)
			}
			old, err := cat.DiscoverAllTools(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			next := readyFakeTransport()
			want := ErrorCatalogChanged
			if identityChange {
				target.TargetID = identityHash("another-target")
				want = ErrorIdentityChanged
			} else {
				next.onMethod("tools/list", toolsListResult{Tools: []MCPToolDef{{Name: "echo", Description: "Changed definition", InputSchema: map[string]any{"type": "object"}}}})
			}
			mgr.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
				return NewClientWithInfo("sample", next, testClientInfo), nil
			}
			if err := mgr.ConnectOne(t.Context(), target); err != nil {
				t.Fatal(err)
			}
			before := next.requestCount()
			_, err = old[0].Execute(t.Context(), json.RawMessage(`{}`))
			if diagnoseAuth(err).ErrorClass != want || next.requestCount() != before {
				t.Fatalf("stale handle: %v requests=%d", err, next.requestCount()-before)
			}
		})
	}
}

func TestFailedDirectoryCannotExecuteCachedSchema(t *testing.T) {
	tr := readyFakeTransport()
	mgr, cat, target := managerFixture(t, false, tr)
	if err := mgr.ConnectOne(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	old, err := cat.DiscoverAllTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next := readyFakeTransport()
	next.onSendError("tools/list", errors.New("directory unavailable"))
	mgr.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		return NewClientWithInfo("sample", next, testClientInfo), nil
	}
	if err := mgr.ConnectOne(t.Context(), target); err == nil {
		t.Fatal("directory failure lost")
	}
	status := mgr.Snapshot().Servers[0]
	if status.Connection != ConnectionConnected || status.Catalog != CatalogError || mgr.Client("sample") == nil {
		t.Fatalf("state=%+v", status)
	}
	before := next.requestCount()
	if _, err := old[0].Execute(t.Context(), nil); err == nil {
		t.Fatal("cached tool executed")
	}
	rediscovered, err := cat.DiscoverAllTools(t.Context())
	if err != nil || len(rediscovered) != 1 || next.requestCount() != before {
		t.Fatalf("discovery retried failed catalog: %v", err)
	}
}

func TestCandidateChecksCredentialGenerationAtPublication(t *testing.T) {
	_, service, target, _ := transportAuthFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		body := `{"tools":[]}`
		if req.Method == "initialize" {
			body = `{"protocolVersion":"` + protocolVersion + `"}`
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, body)
	}))
	manager := NewPendingManagerWithClientInfo([]Target{target}, testClientInfo, service)
	defer manager.Close()
	ticket := ConnectionTicket{Ref: target.Ref, ConfigGeneration: target.ConfigGeneration, ConnectionGeneration: 1}
	candidate := manager.BuildCandidate(t.Context(), target, ticket)
	if !candidate.Report.OK {
		t.Fatalf("candidate=%+v", candidate.Report)
	}
	if _, err := service.Logout(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := manager.InstallCandidate(ticket, candidate); diagnoseAuth(err).ErrorClass != ErrorIdentityChanged {
		t.Fatalf("candidate publication after credential logout: %v", err)
	}
	if manager.Client(target.Config.Name) != nil || manager.Snapshot().Servers[0].Catalog == CatalogReady {
		t.Fatal("logged-out identity became executable")
	}
}

func TestCandidatePublicationPreservesCredentialStoreFailure(t *testing.T) {
	_, service, target, _ := transportAuthFixture(t, http.NotFoundHandler())
	manager := NewPendingManagerWithClientInfo([]Target{target}, testClientInfo, service)
	defer manager.Close()
	path, err := service.store.path(target)
	if err != nil {
		t.Fatal(err)
	}
	manager.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		if err := os.WriteFile(path, []byte(`{"invalid":`), 0o600); err != nil {
			return nil, err
		}
		return NewClientWithInfo(target.Config.Name, readyFakeTransport(), testClientInfo), nil
	}
	if err := manager.ConnectOne(t.Context(), target); diagnoseAuth(err).ErrorClass != ErrorCredentialStoreCorrupt {
		t.Fatalf("credential storage failure lost: %v", err)
	}
	view := manager.Snapshot().Servers[0]
	if view.Diagnostic.ErrorClass != ErrorCredentialStoreCorrupt || view.Connection != ConnectionError || view.Catalog == CatalogReady {
		t.Fatalf("publication failure diagnostic lost: %+v", view)
	}
	if manager.Client(target.Config.Name) != nil {
		t.Fatal("candidate published after credential storage failure")
	}
}
