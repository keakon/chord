package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// Catalog tracks runtime enable intent for Chord-executed MCP tools and
// resolves execution handles that never outlive a client. Connection state and
// wire schemas remain owned by Manager; the Catalog records whether a manual
// server should be enabled and retains its wrapped handles after disconnect so
// historical calls fail precisely instead of becoming unknown tools.
type Catalog struct {
	mgr *Manager

	mu          sync.RWMutex
	desired     map[string]struct{}
	serverTools map[string][]tools.Tool
}

// NewCatalog creates a catalog for the given connection manager.
func NewCatalog(mgr *Manager) *Catalog {
	return &Catalog{
		mgr:         mgr,
		desired:     make(map[string]struct{}),
		serverTools: make(map[string][]tools.Tool),
	}
}

// SetDesiredEnabled records the durable user/resume intent for a manual server.
// It does not connect or disconnect anything.
func (c *Catalog) SetDesiredEnabled(server string, enabled bool) {
	server = strings.TrimSpace(server)
	if server == "" {
		return
	}
	if c.mgr != nil && !c.mgr.IsManualServer(server) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.desired == nil {
		c.desired = make(map[string]struct{})
	}
	if enabled {
		c.desired[server] = struct{}{}
	} else {
		delete(c.desired, server)
	}
}

// DesiredEnabled reports whether server should currently be enabled. Manual
// servers default to disabled until explicitly enabled; automatic servers are
// always enabled.
func (c *Catalog) DesiredEnabled(server string) bool {
	server = strings.TrimSpace(server)
	if server == "" {
		return false
	}
	c.mu.RLock()
	_, enabled := c.desired[server]
	c.mu.RUnlock()
	if enabled {
		return true
	}
	return c.mgr == nil || !c.mgr.IsManualServer(server)
}

// EnabledServerNames returns the sorted names of manual servers whose desired
// state is enabled. Automatic servers are intentionally excluded: their intent
// is config-driven and never persisted.
func (c *Catalog) EnabledServerNames() []string {
	c.mu.RLock()
	names := make([]string, 0, len(c.desired))
	for name := range c.desired {
		names = append(names, name)
	}
	c.mu.RUnlock()
	sort.Strings(names)
	return names
}

func (c *Catalog) rememberServerTools(server string, serverTools []tools.Tool) {
	if c == nil || server == "" || len(serverTools) == 0 {
		return
	}
	c.mu.Lock()
	if c.serverTools == nil {
		c.serverTools = make(map[string][]tools.Tool)
	}
	c.serverTools[server] = append([]tools.Tool(nil), serverTools...)
	c.mu.Unlock()
}

func (c *Catalog) cachedServerTools(server string) []tools.Tool {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]tools.Tool(nil), c.serverTools[server]...)
}

// DiscoverAllTools discovers tools for every connected server and binds each
// tool to an execution handle that enforces the catalog's enabled intent at
// call time, so disable/disconnect can never execute through a stale transport.
func (c *Catalog) DiscoverAllTools(ctx context.Context) ([]tools.Tool, error) {
	if c == nil {
		return DiscoverAllTools(ctx, nil)
	}
	return discoverAllTools(ctx, c.mgr, c)
}

// ExecutionHandle resolves the current client for one remote tool at call time.
// It never retains a *Client: reconnect-safe execution always re-reads the
// manager's current connection.
type ExecutionHandle struct {
	mgr        *Manager
	catalog    *Catalog // nil => always enabled (subagent / direct-client path)
	client     *Client  // static client when mgr == nil (direct-client path)
	serverName string
	remoteName string
}

// Call invokes the remote tool through the current connection, enforcing
// desired-enabled state, the configured allowlist, and connectedness. A
// disabled or disconnected server returns a tool error without touching the
// remote transport.
func (h *ExecutionHandle) Call(ctx context.Context, args json.RawMessage) (string, []message.ContentPart, error) {
	if h == nil {
		return "", nil, fmt.Errorf("MCP tool: execution handle unavailable")
	}
	if h.catalog != nil && !h.catalog.DesiredEnabled(h.serverName) {
		return "", nil, fmt.Errorf("MCP server %q is disabled", h.serverName)
	}
	client := h.client
	if h.mgr != nil {
		client = h.mgr.Client(h.serverName)
		if client == nil {
			return "", nil, fmt.Errorf("MCP server %q is not connected", h.serverName)
		}
		if !h.mgr.ToolAllowed(h.serverName, h.remoteName) {
			return "", nil, fmt.Errorf("MCP tool %s/%s is not allowed", h.serverName, h.remoteName)
		}
	} else if client == nil {
		return "", nil, fmt.Errorf("MCP server %q is not connected", h.serverName)
	}
	return client.CallTool(ctx, h.remoteName, args)
}
