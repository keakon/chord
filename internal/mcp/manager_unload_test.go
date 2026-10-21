package mcp

import (
	"context"
	"errors"
	"testing"
)

// serveSampleTarget makes one target connect successfully with a single tool.
func serveSampleTarget(m *Manager) {
	m.newClientFactory = func(ctx context.Context, target ServerConfig) (*Client, error) {
		transport := newFakeTransport()
		transport.onMethod("initialize", initializeResult{ProtocolVersion: protocolVersion})
		transport.onMethod("tools/list", toolsListResult{Tools: []MCPToolDef{{Name: "sample", InputSchema: map[string]any{"type": "object"}}}})
		client := NewClientWithInfo(target.Name, transport, testClientInfo)
		return client, client.Initialize(ctx)
	}
}

// TestManagerUnloadKeepsManagerReusable is the regression guard for the runtime
// idle unload: releasing connections must not close the manager, or every later
// reconnect and tool call fails with context.Canceled.
func TestManagerUnloadKeepsManagerReusable(t *testing.T) {
	cfg := ServerConfig{Name: "sample", URL: "https://example.invalid/mcp", Manual: true}
	m := NewPendingManagerWithClientInfo(testTargets([]ServerConfig{cfg}), testClientInfo, nil)
	serveSampleTarget(m)
	defer m.Close()

	catalog := NewCatalog(m)
	catalog.SetDesiredEnabled(cfg.Name, true)
	if err := m.ConnectOne(t.Context(), testTarget(cfg)); err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	if len(m.Clients()) != 1 {
		t.Fatalf("clients after connect = %d, want 1", len(m.Clients()))
	}

	m.Unload()
	if len(m.Clients()) != 0 {
		t.Fatalf("clients after Unload = %d, want 0", len(m.Clients()))
	}

	if err := m.ConnectOne(t.Context(), testTarget(cfg)); err != nil {
		t.Fatalf("reconnect after Unload = %v, want the manager to stay usable", err)
	}
	tools, err := catalog.DiscoverAllTools(t.Context())
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools after reconnect = %v, error = %v, want 1 tool", tools, err)
	}
	tool, ok := tools[0].(*MCPTool)
	if !ok || !tool.IsAvailable() {
		t.Fatalf("tool after reconnect = %#v, want available", tools[0])
	}
}

func TestManagerCloseStaysTerminal(t *testing.T) {
	cfg := ServerConfig{Name: "sample", URL: "https://example.invalid/mcp", Manual: true}
	m := NewPendingManagerWithClientInfo(testTargets([]ServerConfig{cfg}), testClientInfo, nil)
	NewCatalog(m).SetDesiredEnabled(cfg.Name, true)
	m.Close()

	if err := m.ConnectOne(context.Background(), testTarget(cfg)); !errors.Is(err, context.Canceled) {
		t.Fatalf("ConnectOne after Close = %v, want context.Canceled", err)
	}
	if _, err := m.AcquireTool(HandleIdentity{ServerName: cfg.Name}, "sample", "hash"); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireTool after Close = %v, want context.Canceled", err)
	}
}
