package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func privateMCPTestServer(t *testing.T, entered, release chan struct{}, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			if calls.Add(1) == 1 && entered != nil {
				close(entered)
			}
			if release != nil {
				<-release
			}
			result = map[string]any{"protocolVersion": request.Params["protocolVersion"], "serverInfo": map[string]string{"name": "sample", "version": "test"}, "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "lookup", "description": "Look up a value", "inputSchema": map[string]any{"type": "object"}}}}
		default:
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPrivateMCPPreparationSharedAndCancellationIndependent(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := privateMCPTestServer(t, entered, release, &calls)
	cfg := config.MCPConfig{"sample": {URL: server.URL, Auth: config.MCPAuthNone}}
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := a.prepareSubAgentMCP(ctx, "worker", cfg, nil); first <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("private initialization did not begin")
	}
	if !a.mcpServerCacheMu.TryLock() {
		close(release)
		t.Fatal("network I/O holds the cache lock")
	}
	a.mcpServerCacheMu.Unlock()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		close(release)
		t.Fatalf("wait cancellation: %v", err)
	}
	second := make(chan MCPRequestSnapshot, 1)
	failure := make(chan error, 1)
	go func() {
		snapshot, err := a.prepareSubAgentMCP(t.Context(), "worker", cfg, nil)
		second <- snapshot
		failure <- err
	}()
	close(release)
	snapshot := <-second
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Tools) != 1 || calls.Load() != 1 {
		t.Fatalf("private connection not shared: tools=%d connects=%d", len(snapshot.Tools), calls.Load())
	}
	if _, err := a.prepareSubAgentMCP(t.Context(), "worker", cfg, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("stable private connection reinitialized")
	}
	a.closeSubAgentMCPServers()
}

func TestPrivateMCPDeniedServerDoesNotConnect(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	var calls atomic.Int32
	server := privateMCPTestServer(t, nil, nil, &calls)
	rules := permission.Ruleset{{Permission: "mcp_sample_*", Pattern: "*", Action: permission.ActionDeny}}
	snapshot, err := a.prepareSubAgentMCP(t.Context(), "worker", config.MCPConfig{"sample": {URL: server.URL, Auth: config.MCPAuthNone}}, rules)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || len(snapshot.Tools) != 0 {
		t.Fatal("denied server was prepared")
	}
	a.closeSubAgentMCPServers()
}

func TestSubAgentMCPPreparationUpdatesCompleteFrozenSurface(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "sample-task")
	current := MCPRequestSnapshot{Version: "first", Tools: []tools.Tool{anchoredManualMCPTool{name: "mcp_sample_first", description: "First tool"}}}
	sub.mcpPreparation = func(context.Context, permission.Ruleset) (MCPRequestSnapshot, error) { return current, nil }
	if err := sub.prepareMCPRequest(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := sub.tools.Get("mcp_sample_first"); !ok {
		t.Fatal("first snapshot missing")
	}
	first := &sub.frozenToolDefs[0]
	if err := sub.prepareMCPRequest(t.Context()); err != nil {
		t.Fatal(err)
	}
	if &sub.frozenToolDefs[0] != first {
		t.Fatal("unchanged surface rebuilt")
	}
	current = MCPRequestSnapshot{Version: "second", Tools: []tools.Tool{anchoredManualMCPTool{name: "mcp_sample_second", description: "Second tool"}}}
	if err := sub.prepareMCPRequest(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := sub.tools.Get("mcp_sample_first"); ok {
		t.Fatal("old MCP tool retained")
	}
	if _, ok := sub.tools.Get("mcp_sample_second"); !ok {
		t.Fatal("replacement MCP tool missing")
	}
	if _, ok := sub.tools.Get(tools.NameComplete); !ok {
		t.Fatal("built-in tool lost")
	}
	for _, def := range sub.frozenToolDefs {
		if def.Name == "mcp_sample_first" {
			t.Fatal("old definition retained")
		}
	}
}

func TestGetOrCreateAgentMCPRejectsInheritedServerName(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	if _, err := a.getOrCreateAgentMCP("explorer", config.MCPConfig{"search": {URL: "https://explorer.example/mcp"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.getOrCreateAgentMCP("browser", config.MCPConfig{"search": {URL: "https://browser.example/mcp"}}); err != nil {
		t.Fatal(err)
	}
	explorer := a.mcpServerCache[agentMCPServerCacheKey("explorer", "search")]
	browser := a.mcpServerCache[agentMCPServerCacheKey("browser", "search")]

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
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	key := agentMCPServerCacheKey("explorer", "search")
	cfg := config.MCPConfig{"search": {URL: "https://exa.example/mcp"}}
	if _, err := a.getOrCreateAgentMCP("explorer", cfg); err != nil {
		t.Fatal(err)
	}
	entry := a.mcpServerCache[key]

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
