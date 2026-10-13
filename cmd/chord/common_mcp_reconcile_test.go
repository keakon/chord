package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
)

func TestRuntimeReconnectStagesNewTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		var result any
		switch q.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "sample", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}
		default:
			t.Errorf("unexpected method %s", q.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result})
	}))
	defer server.Close()
	ac := newTestAppContext(t)
	targets, err := mcp.ResolveTargets(t.Context(), mcp.ResolveTargetOptions{ContentRoot: t.TempDir(), Main: config.MCPConfig{"sample": {URL: server.URL, Auth: config.MCPAuthNone}}})
	if err != nil {
		t.Fatal(err)
	}
	ac.MCPTargets = targets
	ac.MCPAuth = mcp.NewAuthService(mcp.AuthOptions{ConfigHome: t.TempDir()})
	defer ac.MCPAuth.Close()
	ac.MCPMgr = mcp.NewPendingManagerWithClientInfo(targets, mcp.ClientInfo{}, ac.MCPAuth)
	defer ac.MCPMgr.Close()
	ac.MCPCatalog = mcp.NewCatalog(ac.MCPMgr)
	ac.mcpStartOnce.Do(func() {})
	// Startup has completed without a catalog; an explicit reconnect installs it.
	if err := ac.MCPMgr.ConnectOne(t.Context(), targets[0]); err != nil {
		t.Fatal(err)
	}
	if err := reconcileRuntimeMCPCredentials(t.Context(), ac); err != nil {
		t.Fatal(err)
	}
	current, _ := ac.MCPMgr.ToolSnapshot()
	if len(current) != 1 {
		t.Fatalf("manager tools=%d", len(current))
	}
	staged := reflect.ValueOf(ac.MainAgent).Elem().FieldByName("pendingMCPTools").Len()
	if staged != 1 {
		t.Fatalf("manager tools=1, pending model tools=%d", staged)
	}
	// A stable preparation must not republish and emit another status event.
	for len(ac.MainAgent.Events()) > 0 {
		<-ac.MainAgent.Events()
	}
	if err := reconcileRuntimeMCPCredentials(t.Context(), ac); err != nil {
		t.Fatal(err)
	}
	if len(ac.MainAgent.Events()) != 0 {
		t.Fatal("stable surface was republished")
	}
	// A new session generation still needs the same manager version published.
	ac.MainAgent.ResetMCPReady()
	if err := reconcileRuntimeMCPCredentials(t.Context(), ac); err != nil {
		t.Fatal(err)
	}
	if len(ac.MainAgent.Events()) == 0 {
		t.Fatal("new session did not receive the surface")
	}
}
