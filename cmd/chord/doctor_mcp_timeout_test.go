package main

import (
	"testing"

	"github.com/keakon/chord/internal/mcp"
)

func TestMCPToolCallTimeoutLabel(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  mcp.ServerConfig
		want string
	}{
		{"http preset", mcp.ServerConfig{Name: "remote", URL: "https://mcp.test/mcp"}, "120s (http preset)"},
		{"stdio unlimited", mcp.ServerConfig{Name: "local", Command: "sample-mcp"}, "unlimited (stdio)"},
		{"configured", mcp.ServerConfig{Name: "remote", URL: "https://mcp.test/mcp", TimeoutMs: 300_000}, "5m0s (configured)"},
		{"negative", mcp.ServerConfig{Name: "remote", URL: "https://mcp.test/mcp", TimeoutMs: -1}, "invalid (-1ms)"},
		{"above ceiling", mcp.ServerConfig{Name: "remote", URL: "https://mcp.test/mcp", TimeoutMs: 24*60*60*1000 + 1}, "invalid (86400001ms)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpToolCallTimeoutLabel(tc.cfg); got != tc.want {
				t.Fatalf("label = %q, want %q", got, tc.want)
			}
		})
	}
}
