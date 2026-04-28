package mcp

import "github.com/keakon/chord/internal/config"

// ServerConfigsFromConfig converts config.MCPConfig (keyed by server name)
// into a flat slice of ServerConfig suitable for NewManager.
func ServerConfigsFromConfig(mc config.MCPConfig) []ServerConfig {
	if len(mc) == 0 {
		return nil
	}
	configs := make([]ServerConfig, 0, len(mc))
	for name, sc := range mc {
		configs = append(configs, ServerConfig{
			Name:         name,
			Command:      sc.Command,
			Args:         sc.Args,
			Env:          sc.Env,
			URL:          sc.URL,
			AllowedTools: sc.AllowedTools,
		})
	}
	return configs
}
