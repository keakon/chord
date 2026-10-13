package tools

import (
	"maps"
	"strings"

	"github.com/keakon/chord/internal/toolname"
)

// ReplaceMCPTools publishes one complete MCP directory while preserving built-in
// tools. Readers observe either complete directory under the registry lock.
func (r *Registry) ReplaceMCPTools(replacement []Tool) {
	next := make(map[string]Tool, len(replacement))
	for _, tool := range replacement {
		if tool == nil {
			continue
		}
		name := NormalizeName(tool.Name())
		if strings.HasPrefix(name, toolname.MCPToolPrefix) {
			next[name] = tool
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.tools {
		if strings.HasPrefix(name, toolname.MCPToolPrefix) {
			delete(r.tools, name)
		}
	}
	maps.Copy(r.tools, next)
}
