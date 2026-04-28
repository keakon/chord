package mcp

import (
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestServerConfigsFromConfigCopiesAllowedTools(t *testing.T) {
	configs := ServerConfigsFromConfig(config.MCPConfig{
		"search": {
			URL:          "https://mcp.test/mcp",
			AllowedTools: []string{"alpha_tool", "beta_tool"},
		},
	})
	if len(configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(configs))
	}
	if !reflect.DeepEqual(configs[0].AllowedTools, []string{"alpha_tool", "beta_tool"}) {
		t.Fatalf("AllowedTools = %#v", configs[0].AllowedTools)
	}
}
