package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestCatalogExecutionBoundary verifies that a manual server's tools:
//   - are blocked (without touching the transport) while desired-enabled is false;
//   - execute through the current client once enabled;
//   - return a clean "not connected" error after disconnect without touching
//     the transport.
func TestCatalogExecutionBoundary(t *testing.T) {
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{{Name: "echo", Description: "Echo tool", InputSchema: map[string]any{"type": "object"}}},
	})
	ft.onMethod("tools/call", toolCallResult{Content: []toolCallContent{{Type: "text", Text: "ok"}}})

	ctx := context.Background()
	cfg := ServerConfig{Name: "manual-srv", URL: "https://mcp.test/mcp", Manual: true}
	mgr := NewPendingManagerWithClientInfo([]ServerConfig{cfg}, testClientInfo)
	mgr.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		c := NewClientWithInfo("manual-srv", ft, testClientInfo)
		return c, c.Initialize(ctx)
	}
	cat := NewCatalog(mgr)
	if cat.DesiredEnabled("manual-srv") {
		t.Fatal("manual server should default to disabled")
	}

	if err := mgr.ConnectOne(ctx, cfg); err != nil {
		t.Fatalf("ConnectOne: %v", err)
	}
	tools, err := cat.DiscoverAllTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverAllTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("discovered %d tools, want 1", len(tools))
	}

	before := ft.requestCount()
	if _, err := tools[0].Execute(ctx, json.RawMessage(`{"msg":"x"}`)); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled execute error = %v, want disabled error", err)
	}
	if got := ft.requestCount(); got != before {
		t.Fatalf("disabled execute made %d transport requests, want %d", got-before, 0)
	}

	cat.SetDesiredEnabled("manual-srv", true)
	if got, err := tools[0].Execute(ctx, json.RawMessage(`{"msg":"x"}`)); err != nil || got != "ok" {
		t.Fatalf("enabled execute = (%q, %v), want (ok, nil)", got, err)
	}

	mgr.Disconnect("manual-srv")
	if _, err := tools[0].Execute(ctx, json.RawMessage(`{"msg":"x"}`)); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("disconnected execute error = %v, want not connected", err)
	}
	cat.SetDesiredEnabled("manual-srv", false)
	rediscovered, err := cat.DiscoverAllTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverAllTools after disconnect: %v", err)
	}
	if len(rediscovered) != 1 || rediscovered[0].Name() != tools[0].Name() {
		t.Fatalf("tools after disconnect = %#v, want retained execution handle", rediscovered)
	}
	if available, ok := rediscovered[0].(interface{ IsAvailable() bool }); !ok || available.IsAvailable() {
		t.Fatalf("disconnected tool availability = (%v, %v), want unavailable", available, ok)
	}
	if _, err := rediscovered[0].Execute(ctx, json.RawMessage(`{"msg":"x"}`)); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("retained disabled execute error = %v, want disabled error", err)
	}
}

// TestCatalogEnabledServerNames tracks only explicitly enabled manual servers.
func TestCatalogEnabledServerNames(t *testing.T) {
	mgr := NewPendingManager([]ServerConfig{
		{Name: "auto", URL: "https://mcp.test/mcp"},
		{Name: "manual-a", URL: "https://mcp.test/mcp", Manual: true},
		{Name: "manual-b", URL: "https://mcp.test/mcp", Manual: true},
	})
	cat := NewCatalog(mgr)
	if got := cat.EnabledServerNames(); len(got) != 0 {
		t.Fatalf("EnabledServerNames = %v, want empty", got)
	}
	cat.SetDesiredEnabled("manual-b", true)
	cat.SetDesiredEnabled("manual-a", true)
	cat.SetDesiredEnabled("auto", true) // automatic: intent is not persisted
	if got := cat.EnabledServerNames(); len(got) != 2 || got[0] != "manual-a" || got[1] != "manual-b" {
		t.Fatalf("EnabledServerNames = %v, want [manual-a manual-b]", got)
	}
	cat.SetDesiredEnabled("manual-a", false)
	if got := cat.EnabledServerNames(); len(got) != 1 || got[0] != "manual-b" {
		t.Fatalf("EnabledServerNames = %v, want [manual-b]", got)
	}
}
