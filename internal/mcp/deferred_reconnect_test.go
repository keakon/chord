package mcp

import (
	"context"
	"testing"
)

func TestManualDeferredServerRetainsPolicyAfterReconnect(t *testing.T) {
	cfg := ServerConfig{Name: "manual-sample", URL: "https://example.invalid/mcp", Manual: true, Deferred: true}
	m := NewPendingManagerWithClientInfo([]ServerConfig{cfg}, testClientInfo)
	m.newClientFactory = func(ctx context.Context, cfg ServerConfig) (*Client, error) {
		transport := newFakeTransport()
		transport.onMethod("initialize", initializeResult{ProtocolVersion: protocolVersion})
		transport.onMethod("tools/list", toolsListResult{Tools: []MCPToolDef{{Name: "sample", InputSchema: map[string]any{"type": "object"}}}})
		client := NewClientWithInfo(cfg.Name, transport, testClientInfo)
		return client, client.Initialize(ctx)
	}
	defer m.Close()
	// Runtime reconnect omits manual servers that have not been enabled.
	m.ConnectAll(t.Context(), []ServerConfig{{Name: "other-manual", Manual: true}})
	catalog := NewCatalog(m)
	for _, deferred := range []bool{true, false} {
		cfg.Deferred = deferred
		if err := m.ConnectOne(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		tools, err := catalog.DiscoverAllTools(t.Context())
		if err != nil || len(tools) != 1 {
			t.Fatalf("tools = %v, error = %v", tools, err)
		}
		policy, ok := tools[0].(interface{ IsDeferred() bool })
		if !ok || policy.IsDeferred() != deferred {
			t.Fatalf("deferred policy after manual enable = %v, want %v", policy, deferred)
		}
	}
}
