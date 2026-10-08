package mcp

import (
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func testMCPTool(t *testing.T, mgr *Manager, server, remote string) *MCPTool {
	t.Helper()
	wrapped := wrapToolDefs(server, []MCPToolDef{{Name: remote, InputSchema: map[string]any{"type": "object"}}}, func(srv, remote string) *ExecutionHandle {
		return &ExecutionHandle{mgr: mgr, serverName: srv, remoteName: remote}
	})
	if len(wrapped) != 1 {
		t.Fatalf("wrapToolDefs() returned %d tools, want 1", len(wrapped))
	}
	return wrapped[0].(*MCPTool)
}
func testMCPManager(t *testing.T, configs ...ServerConfig) *Manager {
	t.Helper()
	return NewPendingManagerWithClientInfo(configs, ClientInfo{Name: "chord-test", Version: "test"})
}

func TestMCPToolDefaultConcurrencyPolicy(t *testing.T) {
	mgr := testMCPManager(t, ServerConfig{Name: "search", Command: "mcp-server"})
	tool := testMCPTool(t, mgr, "search", "alpha")

	if policy := tool.ConcurrencyPolicy(nil); policy.Resource != "mcp:search" || policy.Mode != tools.ConcurrencyModeConcurrent || policy.AbortSiblingsOnError {
		t.Fatalf("policy = %#v, want mcp:search concurrent without sibling aborts", policy)
	}
	if !tool.ConcurrencyBatchable(nil) {
		t.Fatal("a default MCP tool must be batchable")
	}
	registry := tools.NewRegistry()
	registry.Register(tool)
	if got := tools.ConcurrencyClassForTool(registry, tool.Name(), nil); got != tools.ToolConcurrencyClassConcurrent {
		t.Fatalf("class = %v, want Concurrent", got)
	}
	// Two calls to one server share the resource; the batch builder can merge
	// them without turning MCP into a read-only class.
	other := testMCPTool(t, mgr, "search", "beta")
	if tools.ConcurrencyConflict(tool.ConcurrencyPolicy(nil), other.ConcurrencyPolicy(nil)) {
		t.Fatal("two calls to the same server must not conflict")
	}
}
