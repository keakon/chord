package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/keakon/golog/log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// MCP protocol types
// ---------------------------------------------------------------------------

// MCPToolDef is a tool definition returned by an MCP server's tools/list.
type MCPToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolsListResult is the result of a tools/list request.
type toolsListResult struct {
	Tools []MCPToolDef `json:"tools"`
}

// toolCallResult is the result of a tools/call request.
type toolCallResult struct {
	Content []toolCallContent `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

// toolCallContent is one content block in a tools/call response.
type toolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// initializeParams are sent in the initialize handshake.
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      clientInfo     `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientInfo identifies the application during MCP initialize handshakes.
type ClientInfo = clientInfo

var defaultClientInfo = clientInfo{Name: "chord", Version: "dev"}

func normalizeClientInfo(info ClientInfo) clientInfo {
	info.Name = strings.TrimSpace(info.Name)
	info.Version = strings.TrimSpace(info.Version)
	if info.Name == "" {
		info.Name = defaultClientInfo.Name
	}
	if info.Version == "" {
		info.Version = defaultClientInfo.Version
	}
	return info
}

// initializeResult is the server's response to initialize.
type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Client manages a connection to one MCP server.
type Client struct {
	name       string
	transport  Transport
	clientInfo clientInfo
	serverInfo initializeResult
	nextID     atomic.Int64
}

// NewClient creates a new MCP client for the named server using the given transport.
func NewClient(name string, transport Transport) *Client {
	return NewClientWithInfo(name, transport, defaultClientInfo)
}

// NewClientWithInfo creates a new MCP client with explicit application metadata
// for the initialize handshake.
func NewClientWithInfo(name string, transport Transport, info ClientInfo) *Client {
	info = normalizeClientInfo(info)
	c := &Client{
		name:       name,
		transport:  transport,
		clientInfo: info,
	}
	// Start IDs at 1 (0 is reserved/ambiguous in JSON-RPC).
	c.nextID.Store(1)
	return c
}

// allocID returns the next unique request ID.
func (c *Client) allocID() int {
	return int(c.nextID.Add(1) - 1)
}

// Initialize performs the MCP handshake (initialize request + initialized notification).
func (c *Client) Initialize(ctx context.Context) error {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.allocID(),
		Method:  "initialize",
		Params: initializeParams{
			ProtocolVersion: "2024-11-05",
			Capabilities:    map[string]any{},
			ClientInfo:      c.clientInfo,
		},
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return fmt.Errorf("mcp initialize %s: %w", c.name, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("mcp initialize %s: %w", c.name, resp.Error)
	}

	if err := json.Unmarshal(resp.Result, &c.serverInfo); err != nil {
		return fmt.Errorf("mcp initialize %s: decode result: %w", c.name, err)
	}

	log.Infof("mcp server initialized name=%v server=%v version=%v protocol=%v", c.name, c.serverInfo.ServerInfo.Name, c.serverInfo.ServerInfo.Version, c.serverInfo.ProtocolVersion)

	// Send the initialized notification (required by MCP spec).
	notif := JSONRPCNotification{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	if err := c.transport.Notify(ctx, notif); err != nil {
		log.Warnf("mcp initialized notification failed name=%v error=%v", c.name, err)
	}

	return nil
}

// ListTools discovers available tools from the MCP server via tools/list.
func (c *Client) ListTools(ctx context.Context) ([]MCPToolDef, error) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.allocID(),
		Method:  "tools/list",
		Params:  map[string]any{},
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("mcp tools/list %s: %w", c.name, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp tools/list %s: %w", c.name, resp.Error)
	}

	var result toolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/list %s: decode: %w", c.name, err)
	}

	log.Infof("mcp tools discovered server=%v count=%v", c.name, len(result.Tools))
	return result.Tools, nil
}

// CallTool invokes a tool on the MCP server via tools/call.
// It returns the concatenated text content from the response.
func (c *Client) CallTool(ctx context.Context, toolName string, args json.RawMessage) (string, error) {
	// Parse args into a map so the MCP server receives a proper object.
	var argsMap map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return "", fmt.Errorf("mcp tools/call: invalid args JSON: %w", err)
		}
	}

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.allocID(),
		Method:  "tools/call",
		Params: map[string]any{
			"name":      toolName,
			"arguments": argsMap,
		},
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return "", fmt.Errorf("mcp tools/call %s/%s: %w", c.name, toolName, err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("mcp tools/call %s/%s: %w", c.name, toolName, resp.Error)
	}

	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("mcp tools/call %s/%s: decode: %w", c.name, toolName, err)
	}

	if result.IsError {
		// Collect error text.
		var errText string
		for _, c := range result.Content {
			if c.Type == "text" {
				errText += c.Text
			}
		}
		return "", fmt.Errorf("mcp tool error: %s", errText)
	}

	// Concatenate all text content blocks.
	var text string
	for _, c := range result.Content {
		if c.Type == "text" {
			if text != "" {
				text += "\n"
			}
			text += c.Text
		}
	}
	return text, nil
}

// Close shuts down the transport (and the child process for stdio).
func (c *Client) Close() error {
	return c.transport.Close()
}

// ServerName returns the display name of the connected MCP server.
func (c *Client) ServerName() string {
	if c.serverInfo.ServerInfo.Name != "" {
		return c.serverInfo.ServerInfo.Name
	}
	return c.name
}

// ---------------------------------------------------------------------------
// ServerConfig — MCP server connection configuration
// ---------------------------------------------------------------------------

// ServerConfig defines how to connect to an MCP server.
// This is the mcp package's own config type. The caller (cmd/chord/main.go)
// maps from config.MCPServerConfig to this type when wiring things up.
//
// Either Command (stdio transport) or URL (HTTP transport) must be set.
//
// YAML example:
//
//	mcp:
//	  servers:
//	    filesystem:
//	      command: "npx"
//	      args: ["-y", "@modelcontextprotocol/server-filesystem", "/path"]
//	    api-server:
//	      url: "http://localhost:8080/mcp"
type ServerConfig struct {
	Name         string   // server identifier
	Command      string   // executable path (for stdio transport)
	Args         []string // command arguments (for stdio transport)
	Env          []string // optional environment variables (for stdio transport)
	URL          string   // HTTP URL (for HTTP transport)
	AllowedTools []string // optional allowlist of remote MCP tool names
}

// ---------------------------------------------------------------------------
// Manager — manages all MCP server connections
// ---------------------------------------------------------------------------

// ServerEndpointStatus records whether one configured MCP endpoint connected.
type ServerEndpointStatus struct {
	Name        string
	OK          bool
	Pending     bool
	Retrying    bool
	Attempt     int
	MaxAttempts int
	Error       string
}

const (
	defaultConnectAttempts = 3
	connectAttemptTimeout  = 20 * time.Second
)

var connectRetryBackoff = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}

// Manager manages connections to multiple MCP servers.
type Manager struct {
	mu               sync.RWMutex
	clients          map[string]*Client
	toolDefs         map[string][]MCPToolDef
	allowedTools     map[string]map[string]struct{}
	endpointStat     []ServerEndpointStatus // sorted by Name, one row per unique config key (last duplicate wins)
	newClientFactory func(context.Context, ServerConfig) (*Client, error)
	clientInfo       clientInfo
}

// NewPendingManager creates a manager that exposes configured endpoints as
// pending before any connection attempt starts. Invalid configs are marked as
// immediate failures.
func NewPendingManager(configs []ServerConfig) *Manager {
	return NewPendingManagerWithClientInfo(configs, defaultClientInfo)
}

// NewPendingManagerWithClientInfo creates a pending manager with explicit
// application metadata for MCP initialize handshakes.
func NewPendingManagerWithClientInfo(configs []ServerConfig, info ClientInfo) *Manager {
	info = normalizeClientInfo(info)
	m := &Manager{
		clients:      make(map[string]*Client),
		toolDefs:     make(map[string][]MCPToolDef),
		allowedTools: makeAllowedToolsByServer(configs),
		newClientFactory: func(ctx context.Context, cfg ServerConfig) (*Client, error) {
			return createClient(ctx, cfg, info)
		},
		clientInfo: info,
	}
	if len(configs) == 0 {
		return m
	}

	byName := make(map[string]ServerEndpointStatus)
	for _, cfg := range configs {
		name := serverConfigName(cfg)
		if cfg.Command == "" && cfg.URL == "" {
			byName[name] = ServerEndpointStatus{
				Name:        name,
				OK:          false,
				Pending:     false,
				Retrying:    false,
				Attempt:     0,
				MaxAttempts: defaultConnectAttempts,
				Error:       "must specify either command or url",
			}
			continue
		}
		byName[name] = ServerEndpointStatus{
			Name:        name,
			OK:          false,
			Pending:     true,
			Retrying:    false,
			Attempt:     0,
			MaxAttempts: defaultConnectAttempts,
			Error:       "",
		}
	}
	m.endpointStat = sortedEndpointStatuses(byName)
	return m
}

// NewManager creates MCP clients from config and initializes them.
// Failed servers are recorded in [Manager.ServerEndpoints]; the manager is still
// returned so the UI can show red status for misconfigured or unreachable MCPs.
func NewManager(ctx context.Context, configs []ServerConfig) (*Manager, error) {
	return NewManagerWithClientInfo(ctx, configs, defaultClientInfo)
}

// NewManagerWithClientInfo creates MCP clients with explicit application
// metadata for initialize handshakes.
func NewManagerWithClientInfo(ctx context.Context, configs []ServerConfig, info ClientInfo) (*Manager, error) {
	m := NewPendingManagerWithClientInfo(configs, info)
	if len(configs) == 0 {
		return m, nil
	}
	m.ConnectAll(ctx, configs)
	return m, nil
}

// ConnectAll initializes every configured server and updates the manager in
// place so callers can expose pending state before the connection completes.
func (m *Manager) ConnectAll(ctx context.Context, configs []ServerConfig) {
	if m == nil || len(configs) == 0 {
		return
	}

	m.mu.Lock()
	m.allowedTools = makeAllowedToolsByServer(configs)
	m.mu.Unlock()

	for _, cfg := range configs {
		name := serverConfigName(cfg)

		m.mu.Lock()
		if old := m.clients[name]; old != nil {
			_ = old.Close()
			delete(m.clients, name)
		}
		delete(m.toolDefs, name)
		m.mu.Unlock()

		if cfg.Command == "" && cfg.URL == "" {
			status := ServerEndpointStatus{
				Name:        name,
				OK:          false,
				Pending:     false,
				Retrying:    false,
				Attempt:     0,
				MaxAttempts: defaultConnectAttempts,
				Error:       "must specify either command or url",
			}
			m.setEndpointStatus(status)
			log.Warnf("mcp server invalid config name=%v error=%v", name, status.Error)
			continue
		}

		client, status, err := m.connectServer(ctx, cfg)
		m.setEndpointStatus(status)
		if err != nil {
			if status.Pending {
				log.Infof("mcp server initialization canceled name=%v error=%v", name, err)
			} else {
				log.Warnf("mcp server initialization failed name=%v attempts=%v error=%v", name, status.Attempt, err)
			}
			continue
		}

		m.mu.Lock()
		m.clients[name] = client
		m.mu.Unlock()
	}
}

func (m *Manager) connectServer(ctx context.Context, cfg ServerConfig) (*Client, ServerEndpointStatus, error) {
	name := serverConfigName(cfg)
	maxAttempts := defaultConnectAttempts
	status := ServerEndpointStatus{
		Name:        name,
		OK:          false,
		Pending:     true,
		Retrying:    false,
		Attempt:     0,
		MaxAttempts: maxAttempts,
		Error:       "",
	}
	m.setEndpointStatus(status)

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		status.Attempt = attempt
		status.Pending = true
		status.Retrying = attempt > 1
		status.Error = ""
		m.setEndpointStatus(status)

		attemptCtx := ctx
		cancel := func() {}
		if cfg.URL != "" {
			attemptCtx, cancel = context.WithTimeout(ctx, connectAttemptTimeout)
		}

		client, err := m.newClientFactory(attemptCtx, cfg)
		if err != nil {
			cancel()
			lastErr = err
			if isContextCanceled(err) || errors.Is(ctx.Err(), context.Canceled) {
				return nil, ServerEndpointStatus{
					Name:        name,
					OK:          false,
					Pending:     true,
					Retrying:    false,
					Attempt:     attempt,
					MaxAttempts: maxAttempts,
					Error:       "",
				}, err
			}
			return nil, ServerEndpointStatus{
				Name:        name,
				OK:          false,
				Pending:     false,
				Retrying:    false,
				Attempt:     attempt,
				MaxAttempts: maxAttempts,
				Error:       err.Error(),
			}, err
		}

		initErr := client.Initialize(attemptCtx)
		cancel()
		if initErr == nil {
			return client, ServerEndpointStatus{
				Name:        name,
				OK:          true,
				Pending:     false,
				Retrying:    false,
				Attempt:     attempt,
				MaxAttempts: maxAttempts,
				Error:       "",
			}, nil
		}

		_ = client.Close()
		lastErr = initErr
		if isContextCanceled(initErr) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, ServerEndpointStatus{
				Name:        name,
				OK:          false,
				Pending:     true,
				Retrying:    false,
				Attempt:     attempt,
				MaxAttempts: maxAttempts,
				Error:       "",
			}, initErr
		}
		if !shouldRetryConnectError(initErr, cfg) || attempt == maxAttempts {
			return nil, ServerEndpointStatus{
				Name:        name,
				OK:          false,
				Pending:     false,
				Retrying:    false,
				Attempt:     attempt,
				MaxAttempts: maxAttempts,
				Error:       initErr.Error(),
			}, initErr
		}
		if !sleepWithContext(ctx, connectRetryBackoff[attempt-1]) {
			return nil, ServerEndpointStatus{
				Name:        name,
				OK:          false,
				Pending:     true,
				Retrying:    false,
				Attempt:     attempt,
				MaxAttempts: maxAttempts,
				Error:       "",
			}, context.Canceled
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("mcp server %q: unknown initialization failure", name)
	}
	return nil, ServerEndpointStatus{
		Name:        name,
		OK:          false,
		Pending:     false,
		Retrying:    false,
		Attempt:     maxAttempts,
		MaxAttempts: maxAttempts,
		Error:       lastErr.Error(),
	}, lastErr
}

func shouldRetryConnectError(err error, cfg ServerConfig) bool {
	if err == nil {
		return false
	}
	if isContextCanceled(err) {
		return false
	}
	if cfg.Command != "" {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "must specify either command or url") {
		return false
	}
	if status, ok := extractHTTPStatusFromError(err); ok {
		switch status {
		case 400, 401, 403, 404:
			return false
		}
	}
	if isHTTPClientErrorStatus(msg, 400, 401, 403, 404) {
		return false
	}
	return true
}

func extractHTTPStatusFromError(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var rpcErr *JSONRPCError
	if errors.As(err, &rpcErr) && rpcErr != nil {
		return rpcErr.Code, true
	}
	msg := strings.ToLower(err.Error())
	for _, status := range []int{400, 401, 403, 404, 429, 500, 502, 503, 504} {
		needle := fmt.Sprintf("server returned %d", status)
		if strings.Contains(msg, needle) {
			return status, true
		}
	}
	return 0, false
}

func isHTTPClientErrorStatus(msg string, statuses ...int) bool {
	for _, status := range statuses {
		if strings.Contains(msg, fmt.Sprintf("server returned %d", status)) {
			return true
		}
	}
	return false
}

func isContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func serverConfigName(cfg ServerConfig) string {
	if strings.TrimSpace(cfg.Name) == "" {
		return "(unnamed)"
	}
	return cfg.Name
}

func makeAllowedToolsByServer(configs []ServerConfig) map[string]map[string]struct{} {
	if len(configs) == 0 {
		return nil
	}
	out := make(map[string]map[string]struct{})
	for _, cfg := range configs {
		allowed := makeAllowedToolsSet(cfg.AllowedTools)
		if len(allowed) > 0 {
			out[serverConfigName(cfg)] = allowed
		} else {
			delete(out, serverConfigName(cfg))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func makeAllowedToolsSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filterToolDefsByAllowed(defs []MCPToolDef, allowed map[string]struct{}) []MCPToolDef {
	if len(defs) == 0 || len(allowed) == 0 {
		return defs
	}
	out := make([]MCPToolDef, 0, len(defs))
	for _, def := range defs {
		if _, ok := allowed[def.Name]; ok {
			out = append(out, def)
		}
	}
	return out
}

func (m *Manager) filterToolDefs(serverName string, defs []MCPToolDef) []MCPToolDef {
	if m == nil || len(defs) == 0 {
		return defs
	}
	m.mu.RLock()
	allowed := m.allowedTools[serverName]
	m.mu.RUnlock()
	filtered := filterToolDefsByAllowed(defs, allowed)
	if len(filtered) == len(defs) {
		return defs
	}
	out := make([]MCPToolDef, len(filtered))
	copy(out, filtered)
	return out
}

func sortedEndpointStatuses(byName map[string]ServerEndpointStatus) []ServerEndpointStatus {
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ServerEndpointStatus, 0, len(names))
	for _, n := range names {
		out = append(out, byName[n])
	}
	return out
}

func (m *Manager) setEndpointStatus(status ServerEndpointStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.endpointStat {
		if m.endpointStat[i].Name == status.Name {
			m.endpointStat[i] = status
			return
		}
	}
	m.endpointStat = append(m.endpointStat, status)
	sort.Slice(m.endpointStat, func(i, j int) bool {
		return m.endpointStat[i].Name < m.endpointStat[j].Name
	})
}

// createClient builds a Client with the appropriate transport based on config.
func createClient(ctx context.Context, cfg ServerConfig, info ClientInfo) (*Client, error) {
	if cfg.Command != "" {
		transport, err := NewStdioTransport(ctx, cfg.Command, cfg.Args, cfg.Env)
		if err != nil {
			return nil, err
		}
		return NewClientWithInfo(cfg.Name, transport, info), nil
	}

	if cfg.URL != "" {
		transport := NewHTTPTransport(cfg.URL)
		return NewClientWithInfo(cfg.Name, transport, info), nil
	}

	return nil, fmt.Errorf("mcp server %q: must specify either command or url", strings.TrimSpace(cfg.Name))
}

// Clients returns all successfully initialized clients.
func (m *Manager) Clients() map[string]*Client {
	if m == nil {
		return nil
	}
	// Return a shallow copy to prevent callers from mutating internal state.
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]*Client, len(m.clients))
	for k, v := range m.clients {
		result[k] = v
	}
	return result
}

// CachedToolDefs returns the last discovered tool defs for a server.
func (m *Manager) CachedToolDefs(serverName string) []MCPToolDef {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	defs := m.toolDefs[serverName]
	if len(defs) == 0 {
		return nil
	}
	out := make([]MCPToolDef, len(defs))
	copy(out, defs)
	return out
}

func (m *Manager) setCachedToolDefs(serverName string, defs []MCPToolDef) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(defs) == 0 {
		delete(m.toolDefs, serverName)
		return
	}
	out := make([]MCPToolDef, len(defs))
	copy(out, defs)
	m.toolDefs[serverName] = out
}

// ServerNames returns sorted config names of successfully connected MCP servers.
func (m *Manager) ServerNames() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.clients) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.clients))
	for name := range m.clients {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ServerEndpoints returns per-server reachability (sorted by name). Nil if m is nil.
func (m *Manager) ServerEndpoints() []ServerEndpointStatus {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.endpointStat) == 0 {
		return nil
	}
	out := make([]ServerEndpointStatus, len(m.endpointStat))
	copy(out, m.endpointStat)
	return out
}

// Close shuts down all MCP server connections.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, c := range m.clients {
		if err := c.Close(); err != nil {
			log.Warnf("mcp server close error name=%v error=%v", name, err)
		}
	}
	m.clients = make(map[string]*Client)
	m.toolDefs = make(map[string][]MCPToolDef)
}
