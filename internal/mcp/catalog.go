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

	cacheMu     sync.RWMutex
	desired     map[string]struct{}
	serverTools map[string][]tools.Tool
}

// NewCatalog creates a catalog for the given connection manager.
func NewCatalog(mgr *Manager) *Catalog {
	if mgr == nil {
		mgr = NewPendingManagerWithClientInfo(nil, ClientInfo{}, nil)
	}
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if mgr.catalog != nil {
		return mgr.catalog
	}
	c := &Catalog{mgr: mgr, desired: make(map[string]struct{}), serverTools: make(map[string][]tools.Tool)}
	mgr.catalog = c
	return c
}

// SetDesiredEnabled changes admission intent and retires the previous
// connection atomically. An admitted call retains its lease until it finishes.
func (c *Catalog) SetDesiredEnabled(server string, enabled bool) {
	c.SetDesiredEnabledBatch(map[string]bool{server: enabled})
}

// SetDesiredEnabledBatch gives a session intent update one admission boundary.
func (c *Catalog) SetDesiredEnabledBatch(desired map[string]bool) {
	if c == nil || c.mgr == nil {
		return
	}
	m := c.mgr
	m.mu.Lock()
	var clients []*Client
	changed := false
	for server, enabled := range desired {
		server = strings.TrimSpace(server)
		entry := m.entries[server]
		if entry == nil || !entry.target.Config.Manual {
			continue
		}
		_, old := c.desired[server]
		if old == enabled {
			continue
		}
		if enabled {
			c.desired[server] = struct{}{}
		} else {
			delete(c.desired, server)
		}
		reason := RetireReconnect
		if !enabled {
			reason = RetireDisabled
		}
		if client := m.retireLocked(server, reason); client != nil {
			clients = append(clients, client)
		}
		changed = true
	}
	if changed {
		m.version++
	}
	m.mu.Unlock()
	for _, client := range clients {
		closeMCPClient(client)
	}
	if changed {
		m.publish()
	}
}

func (c *Catalog) DesiredEnabled(server string) bool {
	if c == nil || c.mgr == nil {
		return false
	}
	c.mgr.mu.RLock()
	defer c.mgr.mu.RUnlock()
	return c.mgr.desiredLocked(strings.TrimSpace(server))
}

func (c *Catalog) EnabledServerNames() []string {
	if c == nil || c.mgr == nil {
		return nil
	}
	c.mgr.mu.RLock()
	names := make([]string, 0, len(c.desired))
	for name := range c.desired {
		names = append(names, name)
	}
	c.mgr.mu.RUnlock()
	sort.Strings(names)
	return names
}

func (c *Catalog) rememberServerTools(server string, serverTools []tools.Tool) {
	if c == nil || server == "" || len(serverTools) == 0 {
		return
	}
	c.cacheMu.Lock()
	if c.serverTools == nil {
		c.serverTools = make(map[string][]tools.Tool)
	}
	c.serverTools[server] = append([]tools.Tool(nil), serverTools...)
	c.cacheMu.Unlock()
}

func (c *Catalog) cachedServerTools(server string) []tools.Tool {
	if c == nil {
		return nil
	}
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
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
	identity   HandleIdentity
	schemaHash string
}

// Call invokes the remote tool through the current connection, enforcing
// desired-enabled state, the configured allowlist, and connectedness. A
// disabled or disconnected server returns a tool error without touching the
// remote transport.
func (h *ExecutionHandle) Call(ctx context.Context, args json.RawMessage) (string, []message.ContentPart, error) {
	if h == nil {
		return "", nil, fmt.Errorf("MCP tool: execution handle unavailable")
	}
	if h.mgr != nil {
		lease, err := h.mgr.AcquireTool(h.identity, h.remoteName, h.schemaHash)
		if err != nil {
			return "", nil, newTransportFailure(err.Error(), SendNotSent, err)
		}
		defer lease.Release()
		callCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(h.mgr.ctx, cancel)
		defer stop()
		defer cancel()
		return lease.Client.CallTool(callCtx, h.remoteName, args)
	}
	if h.client == nil {
		return "", nil, fmt.Errorf("MCP server %q is not connected", h.serverName)
	}
	return h.client.CallTool(ctx, h.remoteName, args)
}
