package main

import (
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/mcp"
)

func testMCPTargets(configs []mcp.ServerConfig) []mcp.Target {
	var targets []mcp.Target
	for _, cfg := range configs {
		if cfg.Auth == "" {
			cfg.Auth = config.MCPAuthNone
		}
		targets = append(targets, mcp.Target{Ref: mcp.ServerRef{WorkspaceID: "test-workspace", Owner: mcp.MainOwner, ServerName: cfg.Name}, Config: cfg, ConfigGeneration: 1})
	}
	return targets
}
