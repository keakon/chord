package mcp

import sonicjson "github.com/bytedance/sonic"

// MCP wire responses come from external servers and are decoded at a JSON-RPC
// protocol boundary. Keep this stricter than the LLM/session import hot paths:
// malformed strings should fail here instead of being accepted for provider
// compatibility.
var mcpWireJSON = sonicjson.Config{
	ValidateString: true,
}.Froze()

// MCP tool definitions and call results can be cached in the long-running TUI
// session; copy decoded strings so small fields do not pin entire JSON-RPC
// response buffers. ValidateString also preserves the stricter protocol-boundary
// behavior from mcpWireJSON.
var mcpLongLivedJSON = sonicjson.Config{
	CopyString:     true,
	ValidateString: true,
}.Froze()
