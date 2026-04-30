package mcp

import (
	"context"
	"strings"
	"testing"
)

func TestConnectedServersPromptBlockFiltersAllowedTools(t *testing.T) {
	ctx := context.Background()
	ft := newFakeTransport()
	ft.onMethod("initialize", initializeResult{})
	ft.onMethod("tools/list", toolsListResult{
		Tools: []MCPToolDef{
			{Name: "alpha_tool", Description: "Search"},
			{Name: "beta_tool", Description: "Fetch"},
			{Name: "legacy_tool", Description: "Legacy"},
		},
	})
	cfgs := []ServerConfig{{Name: "search", URL: "https://mcp.test/mcp", AllowedTools: []string{"alpha_tool", "beta_tool"}}}
	mgr := NewPendingManagerWithClientInfo(cfgs, testClientInfo)
	mgr.newClientFactory = func(context.Context, ServerConfig) (*Client, error) {
		client := NewClientWithInfo("search", ft, testClientInfo)
		return client, client.Initialize(ctx)
	}
	mgr.ConnectAll(ctx, cfgs)

	block := ConnectedServersPromptBlock(ctx, mgr)
	if !strings.Contains(block, "mcp_search_alpha_tool") {
		t.Fatalf("prompt block missing allowed search tool: %q", block)
	}
	if !strings.Contains(block, "mcp_search_beta_tool") {
		t.Fatalf("prompt block missing allowed fetch tool: %q", block)
	}
	if strings.Contains(block, "mcp_search_legacy_tool") {
		t.Fatalf("prompt block contained filtered tool: %q", block)
	}
}
