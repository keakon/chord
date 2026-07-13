package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
)

func TestGetOrCreateAgentMCPRejectsInheritedServerName(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.RegisterMainMCPServers([]string{"exa"})

	_, err := a.getOrCreateAgentMCP("explorer", config.MCPConfig{
		"exa": {URL: "https://agent.example/mcp"},
	})
	if err == nil || !strings.Contains(err.Error(), `agent "explorer" declares MCP server "exa"`) {
		t.Fatalf("getOrCreateAgentMCP error = %v, want inherited-name conflict", err)
	}
}

func TestGetOrCreateAgentMCPRejectsManualServer(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	_, err := a.getOrCreateAgentMCP("explorer", config.MCPConfig{
		"search": {URL: "https://agent.example/mcp", Manual: true},
	})
	if err == nil || !strings.Contains(err.Error(), `agent "explorer" MCP server "search" sets manual: true`) {
		t.Fatalf("getOrCreateAgentMCP error = %v, want unsupported manual server", err)
	}
	if len(a.mcpServerCache) != 0 {
		t.Fatalf("mcpServerCache = %#v, want no cached disabled manager", a.mcpServerCache)
	}
}

func TestGetOrCreateAgentMCPIsolatesSameNameAcrossAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	explorer := &mcpServerEntry{Mgr: mcp.NewPendingManager([]mcp.ServerConfig{{Name: "search", URL: "https://explorer.example/mcp"}})}
	browser := &mcpServerEntry{Mgr: mcp.NewPendingManager([]mcp.ServerConfig{{Name: "search", URL: "https://browser.example/mcp"}})}
	a.mcpServerCache = map[string]*mcpServerEntry{
		agentMCPServerCacheKey("explorer", "search"): explorer,
		agentMCPServerCacheKey("browser", "search"):  browser,
	}

	if _, err := a.getOrCreateAgentMCP("explorer", config.MCPConfig{"search": {URL: "https://explorer.example/mcp"}}); err != nil {
		t.Fatalf("getOrCreateAgentMCP(explorer): %v", err)
	}
	if _, err := a.getOrCreateAgentMCP("browser", config.MCPConfig{"search": {URL: "https://browser.example/mcp"}}); err != nil {
		t.Fatalf("getOrCreateAgentMCP(browser): %v", err)
	}
	if a.mcpServerCache[agentMCPServerCacheKey("explorer", "search")] != explorer || a.mcpServerCache[agentMCPServerCacheKey("browser", "search")] != browser {
		t.Fatal("same-named private MCP cache entries were conflated across agents")
	}
	if explorer.Mgr == browser.Mgr {
		t.Fatal("same-named private MCP servers unexpectedly share a manager across agents")
	}
}

func TestGetOrCreateAgentMCPReusesManagerWithinAgentDefinition(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	entry := &mcpServerEntry{Mgr: mcp.NewPendingManager([]mcp.ServerConfig{{Name: "search", URL: "https://exa.example/mcp"}})}
	key := agentMCPServerCacheKey("explorer", "search")
	a.mcpServerCache = map[string]*mcpServerEntry{key: entry}
	cfg := config.MCPConfig{"search": {URL: "https://exa.example/mcp"}}

	if _, err := a.getOrCreateAgentMCP("explorer", cfg); err != nil {
		t.Fatalf("first getOrCreateAgentMCP: %v", err)
	}
	if _, err := a.getOrCreateAgentMCP("explorer", cfg); err != nil {
		t.Fatalf("second getOrCreateAgentMCP: %v", err)
	}
	if a.mcpServerCache[key] != entry {
		t.Fatal("agent instances did not reuse the agent definition's MCP cache entry")
	}
	if a.mcpServerCache[key].Mgr != entry.Mgr {
		t.Fatal("agent instances did not reuse the agent definition's MCP manager")
	}
}
