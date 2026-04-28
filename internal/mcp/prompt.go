package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ConnectedServersPromptBlock returns a system-prompt section listing each
// connected MCP server and its tool names so the model can answer questions
// like "which MCPs are available" without renaming tools.
func ConnectedServersPromptBlock(ctx context.Context, mgr *Manager) string {
	if mgr == nil {
		return ""
	}
	names := mgr.ServerNames()
	if len(names) == 0 {
		return ""
	}
	clients := mgr.Clients()
	if ctx == nil {
		ctx = context.Background()
	}
	var b strings.Builder
	b.WriteString("## MCP (Model Context Protocol) integrations\n")
	b.WriteString("The following external servers are connected. Each MCP tool is named **`mcp_<server>_<tool>`** (use that exact id when calling). ")
	b.WriteString("When the user asks which MCPs you have, list **server names** and the **registered tool ids** under each.\n\n")
	for _, srv := range names {
		c, ok := clients[srv]
		if !ok {
			continue
		}
		toolDefs := mgr.CachedToolDefs(srv)
		if len(toolDefs) == 0 {
			var err error
			toolDefs, err = c.ListTools(ctx)
			if err != nil {
				fmt.Fprintf(&b, "- **%s** — (could not list tools: %v)\n", srv, err)
				continue
			}
			toolDefs = mgr.filterToolDefs(srv, toolDefs)
			mgr.setCachedToolDefs(srv, toolDefs)
		}
		tn := make([]string, 0, len(toolDefs))
		for _, t := range toolDefs {
			if t.Name != "" {
				tn = append(tn, RegisteredMCPToolName(srv, t.Name))
			}
		}
		sort.Strings(tn)
		if len(tn) == 0 {
			fmt.Fprintf(&b, "- **%s** — (no tools)\n", srv)
		} else {
			fmt.Fprintf(&b, "- **%s** — tools: %s\n", srv, strings.Join(tn, ", "))
		}
	}
	return b.String()
}
