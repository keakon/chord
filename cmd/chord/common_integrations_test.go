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
	rows := lspServerDisplayList(mgr)
	if len(rows) != 1 {
		t.Fatalf("lspServerDisplayList len = %d, want 1", len(rows))
	}
	if rows[0].Name != "gopls" || rows[0].OK || !rows[0].Pending || rows[0].Err != "" || rows[0].Errors != 0 || rows[0].Warnings != 0 {
		t.Fatalf("mapped row = %+v, want pending gopls", rows[0])
	}
}

func TestMCPServerDisplayListMapsManagerStatuses(t *testing.T) {
	mgr := mcp.NewPendingManagerWithClientInfo([]mcp.ServerConfig{{Name: "exa", URL: "https://example.com/mcp"}}, mcp.ClientInfo{Name: "chord-test", Version: "test"})
	rows := mcpServerDisplayList(mgr)
	if len(rows) != 1 {
		t.Fatalf("mcpServerDisplayList len = %d, want 1", len(rows))
	}
	if rows[0].Name != "exa" || rows[0].OK || !rows[0].Pending || rows[0].Retrying || rows[0].Attempt != 0 || rows[0].MaxAttempts != 3 || rows[0].Err != "" {
		t.Fatalf("initial mapped row = %+v, want pending initial state", rows[0])
	}
}
