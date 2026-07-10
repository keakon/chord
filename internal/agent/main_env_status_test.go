package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestIntegrationStatusRespectsActiveRolePermissions(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig = &config.Config{MCP: config.MCPConfig{
		"exa": {AllowedTools: []string{"web_search_exa"}},
	}}
	a.SetLSPStatusFunc(func() []LSPServerDisplay {
		return []LSPServerDisplay{{Name: "gopls", OK: true}}
	})
	a.SetMCPStatusFunc(func() []MCPServerDisplay {
		return []MCPServerDisplay{{Name: "exa", Manual: true}}
	})

	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}}
	if got := a.LSPServerList(); len(got) != 0 {
		t.Fatalf("LSPServerList() = %#v, want no rows when lsp is denied", got)
	}
	if got := a.MCPServerList(); len(got) != 0 {
		t.Fatalf("MCPServerList() = %#v, want no rows when all Exa tools are denied", got)
	}
	if got := a.visibleMCPServersPromptBlock(); got != "" {
		t.Fatalf("visibleMCPServersPromptBlock() = %q, want empty without MCP prompt", got)
	}

	a.mcpServersPrompt = "## MCP (Model Context Protocol) integrations\n- **exa** — tools: mcp_exa_web_search_exa"
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "mcp_*", Pattern: "*", Action: permission.ActionAllow},
	}
	if got := a.MCPServerList(); len(got) != 1 || got[0].Name != "exa" {
		t.Fatalf("MCPServerList() = %#v, want visible Exa row", got)
	}
	if got := a.LSPServerList(); len(got) != 0 {
		t.Fatalf("LSPServerList() = %#v, want lsp to remain hidden", got)
	}
	if got := a.visibleMCPServersPromptBlock(); !strings.Contains(got, "mcp_exa_web_search_exa") {
		t.Fatalf("visibleMCPServersPromptBlock() = %q, want visible Exa tool", got)
	}

	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "lsp", Pattern: "*", Action: permission.ActionAllow},
	}
	if got := a.LSPServerList(); len(got) != 1 || got[0].Name != "gopls" {
		t.Fatalf("LSPServerList() = %#v, want visible gopls row", got)
	}
}

func TestMCPCommandHidesDeniedServers(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig = &config.Config{MCP: config.MCPConfig{
		"exa": {AllowedTools: []string{"web_search_exa"}},
	}}
	a.SetMCPStatusFunc(func() []MCPServerDisplay {
		return []MCPServerDisplay{{Name: "exa", Manual: true}}
	})
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}}

	a.handleMCPCommand("/mcp")
	raw := <-a.Events()
	info, ok := raw.(InfoEvent)
	if !ok || info.Message != "No MCP servers available for the active role" {
		t.Fatalf("event = %#v, want unavailable MCP info event", raw)
	}
}

func TestMCPPromptFiltersDeniedToolsWithinVisibleServer(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(dummyTool{name: "mcp_exa_search"})
	a.tools.Register(dummyTool{name: "mcp_exa_admin"})
	a.mcpServersPrompt = "## MCP (Model Context Protocol) integrations\nThe following external servers are connected.\n\n- **exa** — tools: mcp_exa_admin, mcp_exa_search\n"
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "mcp_exa_search", Pattern: "*", Action: permission.ActionAllow},
	}

	got := a.visibleMCPServersPromptBlock()
	if !strings.Contains(got, "The following external servers are connected.") {
		t.Fatalf("visibleMCPServersPromptBlock() dropped MCP guidance: %q", got)
	}
	if !strings.Contains(got, "mcp_exa_search") {
		t.Fatalf("visibleMCPServersPromptBlock() = %q, want allowed tool", got)
	}
	if strings.Contains(got, "mcp_exa_admin") {
		t.Fatalf("visibleMCPServersPromptBlock() = %q, denied tool leaked", got)
	}
}

func TestLazyMCPServerVisibilityRespectsExactAllow(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetMCPStatusFunc(func() []MCPServerDisplay {
		return []MCPServerDisplay{{Name: "exa", Manual: true, Disabled: true}}
	})
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "mcp_exa_search", Pattern: "*", Action: permission.ActionAllow},
	}

	if got := a.MCPServerList(); len(got) != 1 || got[0].Name != "exa" {
		t.Fatalf("MCPServerList() = %#v, want exact-allowed lazy server", got)
	}
}

func TestMCPVisibilityHandlesUnderscoreServerNames(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.tools.Register(dummyMCPTool{dummyTool: dummyTool{name: "mcp_search_api_query"}, server: "search_api"})
	a.globalConfig = &config.Config{MCP: config.MCPConfig{"search": {AllowedTools: []string{"admin"}}}}
	a.SetMCPStatusFunc(func() []MCPServerDisplay {
		return []MCPServerDisplay{{Name: "search_api"}, {Name: "search"}}
	})
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}, {Permission: "mcp_search_api_query", Pattern: "*", Action: permission.ActionAllow}}

	got := a.MCPServerList()
	if len(got) != 1 || got[0].Name != "search_api" {
		t.Fatalf("MCPServerList() = %#v, want longest matching server prefix", got)
	}
}

func TestLazyMCPVisibilityUsesLongestMatchingServerPrefix(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetMCPStatusFunc(func() []MCPServerDisplay {
		return []MCPServerDisplay{{Name: "search", Manual: true}, {Name: "search_api", Manual: true}}
	})
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "mcp_search_api_query", Pattern: "*", Action: permission.ActionAllow},
	}

	got := a.MCPServerList()
	if len(got) != 1 || got[0].Name != "search_api" {
		t.Fatalf("MCPServerList() = %#v, want only longest matching lazy server", got)
	}
}

func BenchmarkMCPServerListPermissionFiltering(b *testing.B) {
	a := &MainAgent{tools: tools.NewRegistry(), globalConfig: &config.Config{}}
	rows := make([]MCPServerDisplay, 12)
	for i := range rows {
		server := fmt.Sprintf("server_%02d", i)
		rows[i] = MCPServerDisplay{Name: server}
		for j := range 8 {
			name := fmt.Sprintf("mcp_%s_tool_%02d", server, j)
			a.tools.Register(dummyMCPTool{dummyTool: dummyTool{name: name}, server: server})
		}
	}
	a.SetMCPStatusFunc(func() []MCPServerDisplay { return rows })
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionDeny}, {Permission: "mcp_*", Pattern: "*", Action: permission.ActionAllow}}
	b.ReportAllocs()
	for b.Loop() {
		if got := len(a.MCPServerList()); got != len(rows) {
			b.Fatalf("MCPServerList() len = %d, want %d", got, len(rows))
		}
	}
}
