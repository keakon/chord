package main

import (
	"testing"

	"github.com/keakon/chord/internal/mcp"
)

func TestMCPServerDisplayListMapsManagerStatuses(t *testing.T) {
	mgr := mcp.NewPendingManager([]mcp.ServerConfig{{Name: "exa", URL: "https://example.com/mcp"}})
	rows := mcpServerDisplayList(mgr)
	if len(rows) != 1 {
		t.Fatalf("mcpServerDisplayList len = %d, want 1", len(rows))
	}
	if rows[0].Name != "exa" || rows[0].OK || !rows[0].Pending || rows[0].Retrying || rows[0].Attempt != 0 || rows[0].MaxAttempts != 3 || rows[0].Err != "" {
		t.Fatalf("initial mapped row = %+v, want pending initial state", rows[0])
	}
}
