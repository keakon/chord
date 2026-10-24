// Package notifymcp implements the private MCP tool surface Chord exposes to an
// external Claude Code process through the SDK control channel. It handles the
// JSON-RPC protocol in process; Claude Code never launches a child MCP server
// and Chord opens no socket for it.
package notifymcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	ServerName = "chord"
	ToolName   = "notify"

	protocolVersion = "2024-11-05"
	// MaxMessageBytes bounds one notify message and its structured payload.
	MaxMessageBytes = 32 * 1024
)

// Request is one notification accepted from the notify tool.
type Request struct {
	Kind    string          `json:"kind,omitempty"`
	Message string          `json:"message"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// DeliverFunc forwards a validated notification to the owning worker.
type DeliverFunc func(context.Context, Request) error

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Handle processes one JSON-RPC message from Claude Code and returns the
// response to wrap in the control frame's mcp_response field.
func Handle(ctx context.Context, message json.RawMessage, deliver DeliverFunc) json.RawMessage {
	var request rpcRequest
	if err := json.Unmarshal(message, &request); err != nil {
		return errorResponse(nil, -32700, "parse error")
	}
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
		return errorResponse(request.ID, -32600, "invalid request")
	}
	if len(bytes.TrimSpace(request.ID)) == 0 {
		// Notifications never receive a JSON-RPC response. The SDK control
		// transport still waits for one control reply, so acknowledge with an
		// empty result that the MCP client ignores.
		return notificationResponse()
	}
	switch request.Method {
	case "initialize":
		return resultResponse(request.ID, initializeResult(request.Params))
	case "ping":
		return resultResponse(request.ID, json.RawMessage(`{}`))
	case "tools/list":
		return resultResponse(request.ID, toolsListResult())
	case "tools/call":
		result, rpcErr := callTool(ctx, request.Params, deliver)
		if rpcErr != nil {
			return errorResponse(request.ID, rpcErr.Code, rpcErr.Message)
		}
		return resultResponse(request.ID, result)
	default:
		return errorResponse(request.ID, -32601, "method not found")
	}
}

// Unavailable answers a request that reached a server Chord does not serve, or
// a tool the role disabled. The request id is echoed so the client can
// correlate the failure instead of waiting for a reply keyed to null.
func Unavailable(message json.RawMessage, reason string) json.RawMessage {
	var request rpcRequest
	_ = json.Unmarshal(message, &request)
	return errorResponse(request.ID, -32601, reason)
}

func initializeResult(params json.RawMessage) json.RawMessage {
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &request)
	version := strings.TrimSpace(request.ProtocolVersion)
	if version == "" {
		version = protocolVersion
	}
	result, _ := json.Marshal(map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "chord-notify", "version": "1"},
	})
	return result
}

func toolsListResult() json.RawMessage {
	tool := map[string]any{
		"name": ToolName,
		"description": "Send a short, non-blocking notice to the Chord owner agent. " +
			"Use kind=decision only when the owner must choose or intervene before you continue. " +
			"The final task result still belongs in your closing answer, not in this tool.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Concise update or decision request for the owner agent.",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"notice", "decision"},
					"default":     "notice",
					"description": "notice for a non-blocking update; decision when the owner must act.",
				},
				"payload": map[string]any{
					"type":        "object",
					"description": "Optional JSON object with structured details, limited to 32 KiB.",
				},
			},
			"required":             []string{"message"},
			"additionalProperties": false,
		},
	}
	result, _ := json.Marshal(map[string]any{"tools": []any{tool}})
	return result
}

func callTool(ctx context.Context, params json.RawMessage, deliver DeliverFunc) (json.RawMessage, *rpcError) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid tool call params"}
	}
	if call.Name != ToolName {
		return nil, &rpcError{Code: -32602, Message: "unknown tool"}
	}
	var arguments struct {
		Message string          `json:"message"`
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid notify arguments"}
	}
	arguments.Message = strings.TrimSpace(arguments.Message)
	if arguments.Message == "" {
		return toolError("message must not be empty"), nil
	}
	if len(arguments.Message) > MaxMessageBytes {
		return toolError(fmt.Sprintf("message exceeds %d bytes", MaxMessageBytes)), nil
	}
	kind := strings.TrimSpace(arguments.Kind)
	if kind == "" {
		kind = "notice"
	}
	if kind != "notice" && kind != "decision" {
		return toolError("kind must be notice or decision"), nil
	}
	payload := bytes.TrimSpace(arguments.Payload)
	if len(payload) > 0 && string(payload) != "null" {
		if len(payload) > MaxMessageBytes || payload[0] != '{' {
			return toolError("payload must be a JSON object within 32 KiB"), nil
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(payload, &object); err != nil || object == nil {
			return toolError("payload must be a JSON object within 32 KiB"), nil
		}
	} else {
		payload = nil
	}
	if deliver == nil {
		return toolError("notify is unavailable"), nil
	}
	if err := deliver(ctx, Request{Kind: kind, Message: arguments.Message, Payload: payload}); err != nil {
		return toolError(err.Error()), nil
	}
	return toolContent("Notification delivered."), nil
}

func resultResponse(id json.RawMessage, result json.RawMessage) json.RawMessage {
	data, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
	return data
}

func notificationResponse() json.RawMessage {
	data, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", Result: json.RawMessage(`{}`)})
	return data
}

func errorResponse(id json.RawMessage, code int, message string) json.RawMessage {
	if len(bytes.TrimSpace(id)) == 0 {
		id = json.RawMessage("null")
	}
	data, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
	return data
}

func toolContent(text string) json.RawMessage {
	result, _ := json.Marshal(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
	return result
}

func toolError(text string) json.RawMessage {
	result, _ := json.Marshal(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": true,
	})
	return result
}
