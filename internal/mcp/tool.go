package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/tools"
)

// MCPTool wraps an MCP server tool definition as a tools.Tool implementation.
// It proxies Execute calls through the MCP Client.
type MCPTool struct {
	client         *Client
	serverName     string
	registeredName string // LLM-facing id, e.g. mcp_exa_web_search
	remoteName     string // name sent to MCP tools/call
	description    string
	schema         map[string]any
}

// Verify MCPTool implements tools.Tool at compile time.
var _ tools.Tool = (*MCPTool)(nil)

func (t *MCPTool) Name() string               { return t.registeredName }
func (t *MCPTool) Description() string        { return t.description }
func (t *MCPTool) Parameters() map[string]any { return t.schema }

// IsReadOnly returns false (conservative default for external MCP tools).
func (t *MCPTool) IsReadOnly() bool { return false }

// Execute calls the tool on the MCP server and returns the result.
func (t *MCPTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	result, err := t.client.CallTool(ctx, t.remoteName, args)
	if err != nil {
		return "", fmt.Errorf("MCP tool %s/%s: %w", t.serverName, t.remoteName, err)
	}
	return result, nil
}

// DiscoverTools queries the MCP client for available tools and wraps each
// one as an MCPTool implementing the tools.Tool interface.
func DiscoverTools(ctx context.Context, client *Client) ([]tools.Tool, error) {
	defs, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	return wrapToolDefs(client, defs), nil
}

func wrapToolDefs(client *Client, defs []MCPToolDef) []tools.Tool {
	result := make([]tools.Tool, 0, len(defs))
	for _, def := range defs {
		// Validate required fields.
		if def.Name == "" {
			log.Warnf("mcp: skipping tool with empty name server=%v", client.name)
			continue
		}

		reg := RegisteredMCPToolName(client.name, def.Name)
		tool := &MCPTool{
			client:         client,
			serverName:     client.name,
			registeredName: reg,
			remoteName:     def.Name,
			description:    def.Description,
			schema:         def.InputSchema,
		}
		result = append(result, tool)
	}
	return result
}

// DiscoverAllTools queries all clients in a Manager for tools and returns
// the combined list. Registered names are unique per server (mcp_<srv>_<tool>).
func DiscoverAllTools(ctx context.Context, mgr *Manager) ([]tools.Tool, error) {
	var allTools []tools.Tool
	seen := make(map[string]struct{})

	for serverName, client := range mgr.Clients() {
		defs, err := client.ListTools(ctx)
		if err != nil {
			log.Warnf("mcp tool discovery failed for server server=%v error=%v", serverName, err)
			continue
		}
		defs = mgr.filterToolDefs(serverName, defs)
		mgr.setCachedToolDefs(serverName, defs)
		serverTools := wrapToolDefs(client, defs)

		for _, t := range serverTools {
			n := t.Name()
			if _, dup := seen[n]; dup {
				log.Warnf("mcp registered tool name collision after sanitize, skipping name=%v server=%v", n, serverName)
				continue
			}
			seen[n] = struct{}{}
			allTools = append(allTools, t)
		}
	}
	return allTools, nil
}
