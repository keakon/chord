package mcp

import (
	"encoding/json"
	"fmt"
)

// decodeJSONRPCResponse retains a decoded ID even on validation failure so
// stream transports can settle the matching caller instead of losing its reply.
func decodeJSONRPCResponse(raw []byte) (JSONRPCResponse, error) {
	var wire struct {
		JSONRPC *string         `json:"jsonrpc"`
		ID      *int            `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	var resp JSONRPCResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return resp, fmt.Errorf("decode JSON-RPC response: %w", err)
	}
	if wire.ID != nil {
		resp.ID = *wire.ID
	}
	if wire.JSONRPC == nil || *wire.JSONRPC != "2.0" || wire.ID == nil {
		return resp, fmt.Errorf("JSON-RPC response requires version 2.0 and a matching integer id")
	}
	resp.JSONRPC = *wire.JSONRPC
	if (wire.Result == nil) == (wire.Error == nil) {
		return resp, fmt.Errorf("JSON-RPC response requires exactly one of result or error")
	}
	if wire.Error != nil {
		var detail struct {
			Code    *int            `json:"code"`
			Message *string         `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(wire.Error, &detail); err != nil {
			return resp, fmt.Errorf("decode JSON-RPC error: %w", err)
		}
		if detail.Code == nil || detail.Message == nil {
			return resp, fmt.Errorf("JSON-RPC error requires an integer code and a string message")
		}
		resp.Error = &JSONRPCError{Code: *detail.Code, Message: *detail.Message}
		if detail.Data != nil {
			if err := json.Unmarshal(detail.Data, &resp.Error.Data); err != nil {
				return resp, fmt.Errorf("decode JSON-RPC error data: %w", err)
			}
		}
	} else {
		resp.Result = wire.Result
	}
	return resp, nil
}
