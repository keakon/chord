package mcp

import "github.com/keakon/chord/internal/config"

func testTarget(cfg ServerConfig) Target {
	if cfg.Auth == "" {
		cfg.Auth = config.MCPAuthNone
	}
	return Target{Ref: ServerRef{WorkspaceID: "test-workspace", Owner: MainOwner, ServerName: serverConfigName(cfg)}, Config: cfg, ConfigGeneration: 1, TargetID: identityHash(cfg.URL), ProfileID: identityHash(cfg.Name)}
}
func testTargets(configs []ServerConfig) []Target {
	var targets []Target
	for _, cfg := range configs {
		targets = append(targets, testTarget(cfg))
	}
	return targets
}
