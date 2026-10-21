package main

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/lsp"
	"github.com/keakon/chord/internal/mcp"
)

func TestLSPServerDisplayListMapsManagerRows(t *testing.T) {
	mgr := lsp.NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, t.TempDir(), nil)
	rows := lspServerDisplayList(mgr, nil)
	if len(rows) != 1 {
		t.Fatalf("lspServerDisplayList len = %d, want 1", len(rows))
	}
	if rows[0].Name != "gopls" || rows[0].OK || !rows[0].Pending || rows[0].Err != "" || rows[0].Errors != 0 || rows[0].Warnings != 0 {
		t.Fatalf("mapped row = %+v, want pending gopls", rows[0])
	}
}

func TestLSPServerDisplayListMarksIdleUnloadedRows(t *testing.T) {
	mgr := lsp.NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {
				Command:   "gopls",
				FileTypes: []string{".go"},
			},
		},
	}, t.TempDir(), nil)
	ctrl := &runtimeResourceController{}
	ctrl.mu.Lock()
	ctrl.idleUnloaded = true
	ctrl.lspUnloaded = true
	ctrl.lspLoaded = map[string]struct{}{"gopls": {}}
	ctrl.mu.Unlock()
	rows := lspServerDisplayList(mgr, ctrl)
	if len(rows) != 1 {
		t.Fatalf("lspServerDisplayList len = %d, want 1", len(rows))
	}
	if rows[0].Name != "gopls" || rows[0].OK || rows[0].Pending || !rows[0].Idle || rows[0].Err != "" {
		t.Fatalf("mapped row = %+v, want idle gopls", rows[0])
	}
}

func TestServerDisplayListsAllowNilRuntimeController(t *testing.T) {
	lspMgr := lsp.NewManager(&config.Config{
		LSP: config.LSPConfig{
			"gopls": {Command: "gopls", FileTypes: []string{".go"}},
		},
	}, t.TempDir(), nil)
	if rows := lspServerDisplayList(lspMgr, nil); len(rows) != 1 {
		t.Fatalf("lspServerDisplayList(nil runtime) len = %d, want 1", len(rows))
	}
	mcpMgr := mcp.NewPendingManagerWithClientInfo(testMCPTargets([]mcp.ServerConfig{{Name: "exa", URL: "https://example.com/mcp"}}), mcp.ClientInfo{Name: "chord-test", Version: "test"}, nil)
	if rows := mcpServerDisplayList(mcpMgr, nil, nil); len(rows) != 1 {
		t.Fatalf("mcpServerDisplayList(nil runtime) len = %d, want 1", len(rows))
	}
}

func TestMCPServerDisplayListMapsManagerStatuses(t *testing.T) {
	mgr := mcp.NewPendingManagerWithClientInfo(testMCPTargets([]mcp.ServerConfig{{Name: "exa", URL: "https://example.com/mcp"}}), mcp.ClientInfo{Name: "chord-test", Version: "test"}, nil)
	rows := mcpServerDisplayList(mgr, nil, nil)
	if len(rows) != 1 {
		t.Fatalf("mcpServerDisplayList len = %d, want 1", len(rows))
	}
	if rows[0].Name != "exa" || rows[0].OK || !rows[0].Pending || rows[0].Retrying || rows[0].Attempt != 0 || rows[0].MaxAttempts != 3 || rows[0].Err != "" {
		t.Fatalf("initial mapped row = %+v, want pending initial state", rows[0])
	}
}

func TestMCPServerDisplayListMarksIdleUnloadedRows(t *testing.T) {
	mgr := mcp.NewPendingManagerWithClientInfo(testMCPTargets([]mcp.ServerConfig{{Name: "exa", URL: "https://example.com/mcp"}}), mcp.ClientInfo{Name: "chord-test", Version: "test"}, nil)
	ctrl := &runtimeResourceController{}
	ctrl.mu.Lock()
	ctrl.idleUnloaded = true
	ctrl.mcpWasLoaded = true
	ctrl.mu.Unlock()
	rows := mcpServerDisplayList(mgr, nil, ctrl)
	if len(rows) != 1 {
		t.Fatalf("mcpServerDisplayList len = %d, want 1", len(rows))
	}
	if rows[0].Name != "exa" || rows[0].OK || rows[0].Pending || !rows[0].Idle || rows[0].Retrying || rows[0].Err != "" {
		t.Fatalf("mapped row = %+v, want idle initial state", rows[0])
	}
}

// TestMCPServerDisplayListTreatsReleasedEndpointAsUnloaded covers the state left
// behind by an idle unload once the runtime idle flag is gone: releasing a
// connection records no failure, so the row has to stay the neutral unloaded
// state instead of falling into the failed bucket.
func TestMCPServerDisplayListTreatsReleasedEndpointAsUnloaded(t *testing.T) {
	mgr := mcp.NewPendingManagerWithClientInfo(testMCPTargets([]mcp.ServerConfig{{Name: "exa", URL: "https://example.com/mcp"}}), mcp.ClientInfo{Name: "chord-test", Version: "test"}, nil)
	mgr.RetireConnection("exa", mcp.RetireIdle)

	rows := mcpServerDisplayList(mgr, nil, nil)
	if len(rows) != 1 {
		t.Fatalf("mcpServerDisplayList len = %d, want 1", len(rows))
	}
	if !rows[0].Idle || rows[0].OK || rows[0].Pending || rows[0].Err != "" {
		t.Fatalf("released row = %+v, want idle unloaded", rows[0])
	}
}

// TestMCPServerDisplayListKeepsRecordedFailuresErroring makes sure the neutral
// unloaded state cannot swallow a server that actually failed to connect.
func TestMCPServerDisplayListKeepsRecordedFailuresErroring(t *testing.T) {
	// No transport configured: the manager records a protocol failure.
	mgr := mcp.NewPendingManagerWithClientInfo(testMCPTargets([]mcp.ServerConfig{{Name: "exa"}}), mcp.ClientInfo{Name: "chord-test", Version: "test"}, nil)

	rows := mcpServerDisplayList(mgr, nil, nil)
	if len(rows) != 1 {
		t.Fatalf("mcpServerDisplayList len = %d, want 1", len(rows))
	}
	if rows[0].Idle || rows[0].Err == "" {
		t.Fatalf("failed row = %+v, want a recorded failure", rows[0])
	}
}
