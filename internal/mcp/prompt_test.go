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

func TestServersPromptBlockRenderParseRoundTrip(t *testing.T) {
	servers := []ServerTools{
		{Name: "search", Tools: []string{"mcp_search_alpha_tool", "mcp_search_beta_tool"}},
		{Name: "broken", Note: "(could not list tools: connection refused)"},
		{Name: "idle", Note: "(no tools)"},
	}
	block := RenderServersPromptBlock(servers)
	parsed := ParseServersPromptBlock(block)
	if len(parsed) != len(servers) {
		t.Fatalf("round trip lost rows: got %d, want %d\nblock: %q", len(parsed), len(servers), block)
	}
	for i, want := range servers {
		got := parsed[i]
		if got.Name != want.Name || strings.Join(got.Tools, ",") != strings.Join(want.Tools, ",") || got.Note != want.Note {
			t.Fatalf("round trip row %d = %+v, want %+v", i, got, want)
		}
	}
	if ParseServersPromptBlock("MCP original prompt") != nil {
		t.Fatal("custom block without the renderer header must parse as nil (pass-through)")
	}
	if ParseServersPromptBlock("") != nil {
		t.Fatal("empty block must parse as nil")
	}
}
