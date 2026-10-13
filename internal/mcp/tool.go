package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
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

func (t *MCPTool) IsDeferred() bool {
	if t == nil || t.handle == nil || t.handle.mgr == nil {
		return false
	}
	return t.handle.mgr.IsDeferredServer(t.serverName)
}

func (t *MCPTool) DiscoveryStatus() string {
	if t.handle != nil && t.handle.catalog != nil && !t.handle.catalog.DesiredEnabled(t.serverName) {
		return tools.DiscoveryDisabled
	}
	if !t.IsAvailable() {
		return tools.DiscoveryUnavailable
	}
	return message.ToolDiscoveryLoaded
}

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
		return t.handle.mgr.toolAvailable(t.handle.identity, t.remoteName, t.handle.schemaHash)
	}
	return t.handle.client != nil
}

// IsReadOnly returns false (conservative default for external MCP tools).
func (t *MCPTool) IsReadOnly() bool { return false }

// ConcurrencyPolicy scopes MCP calls to their server. Calls emitted together
// in one model response run concurrently as selected by the model.
func (t *MCPTool) ConcurrencyPolicy(args json.RawMessage) tools.ConcurrencyPolicy {
	return tools.ConcurrencyPolicy{Resource: "mcp:" + t.serverName, Mode: tools.ConcurrencyModeConcurrent}
}

// ConcurrencyBatchable admits the call to a batch selected by the model. MCP
// calls keep their started journal record and never run speculatively.
func (t *MCPTool) ConcurrencyBatchable(args json.RawMessage) bool { return true }

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

	connected := make(map[string]bool)
	for _, surface := range mgr.toolSurfaces() {
		serverName := surface.name
		connected[serverName] = true
		serverTools := mgr.wrapSurface(surface, catalog)
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
			if connected[status.Name] {
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
