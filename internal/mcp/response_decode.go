package mcp

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
)

// decodeJSONRPCResponse decodes a JSON-RPC 2.0 response with v2 strict
// semantics: duplicate object members and invalid UTF-8 are rejected, including
// inside the captured result/error bodies.
//
// When the strict decode rejects the record, a lenient second pass recovers
// whatever ID the envelope carried, so transports can settle the matching
// caller with the parse error instead of discarding the record (which would
// leave the call waiting for its context deadline).
func decodeJSONRPCResponse(raw []byte) (JSONRPCResponse, error) {
	var wire struct {
		JSONRPC *string        `json:"jsonrpc"`
		ID      *int           `json:"id"`
		Result  jsontext.Value `json:"result"`
		Error   jsontext.Value `json:"error"`
	}
	var resp JSONRPCResponse
	if err := jsonv2.Unmarshal(raw, &wire); err != nil {
		// The strict pass owns validation, so this error is expected to
		// propagate. Recover the ID best-effort: v2 may leave the destination
		// partially populated, and any ID the lenient pass reaches can still be
		// correlated. Duplicate members and invalid UTF-8 are tolerated here
		// only because the strict pass has already rejected them.
		var lenient struct {
			ID *int `json:"id"`
		}
		_ = jsonv2.Unmarshal(raw, &lenient, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
		if lenient.ID != nil {
			resp.ID = *lenient.ID
		}
		return resp, fmt.Errorf("decode JSON-RPC response: %w", err)
	}
	if wire.ID != nil {
		resp.ID = *wire.ID
	}
	if wire.JSONRPC == nil || *wire.JSONRPC != "2.0" || wire.ID == nil {
		return resp, fmt.Errorf("JSON-RPC response requires version 2.0 and a matching integer id")
	}
	if (wire.Result == nil) == (wire.Error == nil) {
		return resp, fmt.Errorf("JSON-RPC response requires exactly one of result or error")
	}
	if wire.Error != nil {
		var detail struct {
			Code    *int            `json:"code"`
			Message *string         `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := jsonv2.Unmarshal(wire.Error, &detail); err != nil {
			return resp, fmt.Errorf("decode JSON-RPC error: %w", err)
		}
		if detail.Code == nil || detail.Message == nil {
			return resp, fmt.Errorf("JSON-RPC error requires an integer code and a string message")
		}
		resp.Error = &JSONRPCError{Code: *detail.Code, Message: *detail.Message}
		if detail.Data != nil {
			if err := jsonv2.Unmarshal(detail.Data, &resp.Error.Data); err != nil {
				return resp, fmt.Errorf("decode JSON-RPC error data: %w", err)
			}
		}
	} else {
		// jsontext.Value captures copy the bytes, so the result stays valid
		// after the input buffer (a reused scanner line for stdio) moves on.
		resp.Result = json.RawMessage(wire.Result)
	}
	resp.JSONRPC = *wire.JSONRPC
	return resp, nil
}
