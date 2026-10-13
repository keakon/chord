package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	sonicjson "github.com/bytedance/sonic"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// ---------------------------------------------------------------------------
// MCP protocol types
// ---------------------------------------------------------------------------

// MCPToolDef is a tool definition returned by an MCP server's tools/list.
type MCPToolDef struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  map[string]any  `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  map[string]any  `json:"annotations,omitempty"`
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

const protocolVersion = "2025-06-18"

const missingServerTransportMessage = "must specify either command or url"

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
	name             string
	transport        Transport
	clientInfo       clientInfo
	serverInfo       initializeResult
	nextID           atomic.Int64
	outputValidators atomic.Pointer[toolOutputValidators]
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
			ProtocolVersion: protocolVersion,
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

	if err := mcpLongLivedJSON.Unmarshal(resp.Result, &c.serverInfo); err != nil {
		return fmt.Errorf("mcp initialize %s: decode result: %w", c.name, err)
	}

	if c.serverInfo.ProtocolVersion != protocolVersion {
		return fmt.Errorf("mcp initialize %s: unsupported protocol version %q (expected %s)", c.name, c.serverInfo.ProtocolVersion, protocolVersion)
	}
	if transport, ok := c.transport.(*HTTPTransport); ok {
		transport.setProtocolVersion(c.serverInfo.ProtocolVersion)
	}

	log.Debugf("mcp server initialized name=%v server=%v version=%v protocol=%v", c.name, c.serverInfo.ServerInfo.Name, c.serverInfo.ServerInfo.Version, c.serverInfo.ProtocolVersion)

	// Send the initialized notification (required by MCP spec).
	notif := JSONRPCNotification{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	if err := c.transport.Notify(ctx, notif); err != nil {
		return fmt.Errorf("mcp initialized notification %s: %w", c.name, err)
	}

	return nil
}

// CallTool invokes a tool on the MCP server via tools/call.
// It returns the concatenated text content plus any image content blocks
// (decoded from base64) so the runtime can re-inject images into model context.
func (c *Client) CallTool(ctx context.Context, toolName string, args json.RawMessage) (string, []message.ContentPart, error) {
	// Bind validation to the directory visible when the call starts.
	validators := c.outputValidators.Load()
	// Parse args into a map so the MCP server receives a proper object.
	var argsMap map[string]any
	if len(args) > 0 {
		if err := sonicjson.ConfigDefault.Unmarshal(args, &argsMap); err != nil {
			return "", nil, fmt.Errorf("mcp tools/call: invalid args JSON: %w", err)
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
		return "", nil, fmt.Errorf("mcp tools/call %s/%s: %w", c.name, toolName, err)
	}
	if resp.Error != nil {
		return "", nil, fmt.Errorf("mcp tools/call %s/%s: %w", c.name, toolName, resp.Error)
	}

	if raw := bytes.TrimSpace(resp.Result); len(raw) == 0 || raw[0] != '{' {
		return "", nil, fmt.Errorf("mcp tools/call %s/%s: result must be an object", c.name, toolName)
	}

	var result toolCallResult
	if err := mcpLongLivedJSON.Unmarshal(resp.Result, &result); err != nil {
		return "", nil, fmt.Errorf("mcp tools/call %s/%s: decode: %w", c.name, toolName, err)
	}

	text, images, err := c.normalizeToolCallResult(ctx, toolName, result)
	if err != nil {
		return text, images, err
	}
	if warning := validators.warning(toolName, result.StructuredContent); warning != "" {
		text = warning + "\n\n" + text
	}
	return text, images, nil
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
	Auth         string
	OAuth        *config.MCPOAuthConfig
	Name         string            // server identifier
	Command      string            // executable path (for stdio transport)
	Args         []string          // command arguments (for stdio transport)
	Env          []string          // optional environment variables (for stdio transport)
	URL          string            // HTTP URL (for HTTP transport)
	Headers      map[string]string // extra HTTP headers (for HTTP transport); values starting with $ are expanded from the environment
	AllowedTools []string          // optional allowlist of remote MCP tool names
	Manual       bool              // when true, do not auto-connect on startup
	Deferred     bool              // load individual schemas through tool_search
}

// ---------------------------------------------------------------------------
// Manager — manages all MCP server connections
// ---------------------------------------------------------------------------

// ServerEndpointStatus records whether one configured MCP endpoint connected.
type ServerEndpointStatus struct {
	Name        string
	OK          bool
	Pending     bool
	Disabled    bool
	Manual      bool
	Retrying    bool
	Attempt     int
	MaxAttempts int
	Error       string
}

const defaultConnectAttempts = 3

// connectAttemptTimeout bounds one connect attempt. It is a var so tests can
// shorten the window instead of waiting it out.
var (
	connectAttemptTimeout = 20 * time.Second
	connectRetryBackoff   = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}
)

type clientFactoryResult struct {
	client *Client
	err    error
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
	if _, ok := errors.AsType[*authFailure](err); ok {
		return false
	}
	if _, ok := errors.AsType[*CredentialStoreError](err); ok {
		return false
	}
	if _, ok := errors.AsType[*DiscoveryError](err); ok {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, missingServerTransportMessage) {
		return false
	}
	if httpErr, ok := errors.AsType[*HTTPError](err); ok {
		if httpErr.SendEvidence == SendNotSent || (httpErr.StatusCode == 0 && httpErr.SendEvidence == SendResponseReceived) {
			return false
		}
		switch httpErr.StatusCode {
		case 400, 401, 403, 404:
			return false
		}
	}
	return true
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

// createClient builds a Client with the appropriate transport based on config.
func createClient(ctx context.Context, cfg ServerConfig, info ClientInfo) (*Client, error) {
	if cfg.Command != "" {
		if len(cfg.Headers) > 0 {
			return nil, fmt.Errorf("mcp server %q: headers apply only to remote (url) servers; stdio servers receive no HTTP requests", strings.TrimSpace(cfg.Name))
		}
		transport, err := NewStdioTransport(ctx, cfg.Command, cfg.Args, cfg.Env)
		if err != nil {
			return nil, err
		}
		return NewClientWithInfo(cfg.Name, transport, info), nil
	}

	if cfg.URL != "" {
		headers, err := expandHeaders(cfg.Name, cfg.Headers)
		if err != nil {
			return nil, err
		}
		transport := NewHTTPTransport(HTTPTransportOptions{Endpoint: cfg.URL, Headers: headers})
		return NewClientWithInfo(cfg.Name, transport, info), nil
	}

	return nil, fmt.Errorf("mcp server %q: %s", strings.TrimSpace(cfg.Name), missingServerTransportMessage)
}

// expandHeaders validates the configured headers and resolves values that
// start with "$" from the environment, mirroring the auth config convention so
// API keys do not need to be written into config files. A "$"-prefixed value
// whose environment variables are all unset expands to an empty string, which
// would silently authenticate with a blank credential, so that is an error.
func expandHeaders(server string, headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	server = strings.TrimSpace(server)
	expanded := make(map[string]string, len(headers))
	for k, v := range headers {
		if !validHeaderName(k) {
			return nil, fmt.Errorf("mcp server %q: header name %q is not a valid HTTP header name", server, k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("mcp server %q: header %q value must not contain CR or LF", server, k)
		}
		if strings.HasPrefix(v, "$") {
			v = os.ExpandEnv(v)
			if v == "" {
				return nil, fmt.Errorf("mcp server %q: header %q expands from the environment to an empty value; check that the referenced variable is set", server, k)
			}
		}
		expanded[k] = v
	}
	return expanded, nil
}

// validHeaderName reports whether name is a valid HTTP field name (an RFC 7230
// token): non-empty and free of separators and control characters.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r <= ' ' || r >= 0x7F || strings.ContainsRune(`"(),/:;<=>?@[\]{}`, r) {
			return false
		}
	}
	return true
}
