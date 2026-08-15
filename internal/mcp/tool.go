package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/tools"
)

// MCPTool wraps an MCP server tool definition as a tools.Tool implementation.
// It proxies Execute calls through an ExecutionHandle that resolves the current
// connection at call time, so a Disconnect() can never leave a stale closed
// transport behind.
type MCPTool struct {
	handle         *ExecutionHandle
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
func (t *MCPTool) MCPServerName() string      { return t.serverName }

// IsManual reports whether the tool belongs to a manual server, i.e. one that
// can be enabled/disabled at runtime. Manual-server tools are mounted through
// the provider's dynamic channel (when supported) instead of the stable
// top-level tool surface; automatic-server tools stay stable.
func (t *MCPTool) IsManual() bool {
	if t == nil || t.handle == nil || t.handle.catalog == nil || t.handle.catalog.mgr == nil {
		return false
	}
	return t.handle.catalog.mgr.IsManualServer(t.serverName)
}

// IsAvailable keeps a disconnected or disabled manual tool in the registry so
// historical calls can produce a precise runtime error, without advertising it
// to the model until its server is connected and enabled again.
func (t *MCPTool) IsAvailable() bool {
	if t == nil || t.handle == nil {
		return false
	}
	if t.handle.catalog != nil && !t.handle.catalog.DesiredEnabled(t.serverName) {
		return false
	}
	if t.handle.mgr != nil {
		return t.handle.mgr.Client(t.serverName) != nil && t.handle.mgr.ToolAllowed(t.serverName, t.remoteName)
	}
	return t.handle.client != nil
}

// IsReadOnly returns false (conservative default for external MCP tools).
func (t *MCPTool) IsReadOnly() bool { return false }

// Execute calls the tool on the MCP server and returns the textual result.
// Any image content blocks returned by the server are pushed to the image sink
// in the context (if present) so the runtime can attach them to model context.
func (t *MCPTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	result, images, err := t.handle.Call(ctx, args)
	if err != nil {
		return "", fmt.Errorf("MCP tool %s/%s: %w", t.serverName, t.remoteName, err)
	}
	if len(images) > 0 {
		if sink, ok := tools.ImageSinkFromContext(ctx); ok {
			for _, img := range images {
				sink.AddImage(img)
			}
		}
	}
	return result, nil
}

// DiscoverTools queries the MCP client for available tools and wraps each one
// as an MCPTool bound directly to that client. It is used by direct-client
// callers; manager-driven callers use DiscoverAllTools.
func DiscoverTools(ctx context.Context, client *Client) ([]tools.Tool, error) {
	defs, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	return wrapToolDefs(client.name, defs, func(_, remoteName string) *ExecutionHandle {
		return &ExecutionHandle{client: client, serverName: client.name, remoteName: remoteName}
	}), nil
}

// wrapToolDefs converts discovered defs for serverName into tools bound to
// handles produced by handleFor.
func wrapToolDefs(serverName string, defs []MCPToolDef, handleFor func(serverName, remoteName string) *ExecutionHandle) []tools.Tool {
	result := make([]tools.Tool, 0, len(defs))
	for _, def := range defs {
		// Validate required fields.
		if def.Name == "" {
			log.Warnf("mcp: skipping tool with empty name server=%v", serverName)
			continue
		}

		reg := RegisteredMCPToolName(serverName, def.Name)
		tool := &MCPTool{
			handle:         handleFor(serverName, def.Name),
			serverName:     serverName,
			registeredName: reg,
			remoteName:     def.Name,
			description:    def.Description,
			schema:         def.InputSchema,
		}
		result = append(result, tool)
	}
	return result
}

// DiscoverAllTools queries all clients in a Manager for tools and returns the
// combined list bound to handles that resolve the manager's current connection
// at execution time. Registered names are unique per server
// (mcp_<srv>_<tool>).
func DiscoverAllTools(ctx context.Context, mgr *Manager) ([]tools.Tool, error) {
	return discoverAllTools(ctx, mgr, nil)
}

func discoverAllTools(ctx context.Context, mgr *Manager, catalog *Catalog) ([]tools.Tool, error) {
	if mgr == nil {
		return nil, nil
	}
	var allTools []tools.Tool
	seen := make(map[string]struct{})

	clients := mgr.Clients()
	serverNames := make([]string, 0, len(clients))
	for serverName := range clients {
		serverNames = append(serverNames, serverName)
	}
	sort.Strings(serverNames)
	for _, serverName := range serverNames {
		client := clients[serverName]
		defs, err := client.ListTools(ctx)
		if err != nil {
			defs = mgr.CachedToolDefs(serverName)
			if len(defs) == 0 {
				if catalog != nil {
					cached := catalog.cachedServerTools(serverName)
					for _, t := range cached {
						n := t.Name()
						if _, dup := seen[n]; dup {
							continue
						}
						seen[n] = struct{}{}
						allTools = append(allTools, t)
					}
				}
				log.Warnf("mcp tool discovery failed for server server=%v error=%v", serverName, err)
				continue
			}
			log.Warnf("mcp tool discovery failed; using cached definitions server=%v error=%v", serverName, err)
		} else {
			defs = mgr.filterToolDefs(serverName, defs)
			mgr.setCachedToolDefs(serverName, defs)
		}
		serverTools := wrapToolDefs(serverName, defs, func(srv, remote string) *ExecutionHandle {
			return &ExecutionHandle{mgr: mgr, catalog: catalog, serverName: srv, remoteName: remote}
		})
		if catalog != nil {
			catalog.rememberServerTools(serverName, serverTools)
		}

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
	if catalog != nil {
		for _, status := range mgr.ServerEndpoints() {
			if _, connected := clients[status.Name]; connected {
				continue
			}
			for _, t := range catalog.cachedServerTools(status.Name) {
				n := t.Name()
				if _, dup := seen[n]; dup {
					continue
				}
				seen[n] = struct{}{}
				allTools = append(allTools, t)
			}
		}
	}
	return allTools, nil
}
