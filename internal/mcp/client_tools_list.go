package mcp

import (
	"context"
	"fmt"

	"github.com/keakon/golog/log"
)

const (
	maxToolListPages = 1000
	maxListedTools   = 10000
	maxToolListBytes = 32 << 20
)

type toolsListResult struct {
	Tools      []MCPToolDef `json:"tools"`
	NextCursor *string      `json:"nextCursor,omitempty"`
}

// ListTools discovers the complete directory. A failed page never publishes
// partial definitions to callers or replaces their last complete cache.
func (c *Client) ListTools(ctx context.Context) ([]MCPToolDef, error) {
	var defs []MCPToolDef
	cursors := make(map[string]struct{})
	names := make(map[string]struct{})
	var cursor *string
	totalBytes := 0
	for range maxToolListPages {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("mcp tools/list %s: %w", c.name, err)
		}
		params := map[string]any{}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		resp, err := c.transport.Send(ctx, JSONRPCRequest{
			JSONRPC: "2.0", ID: c.allocID(), Method: "tools/list", Params: params,
		})
		if err != nil {
			return nil, fmt.Errorf("mcp tools/list %s: %w", c.name, err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp tools/list %s: %w", c.name, resp.Error)
		}
		if len(resp.Result) > maxToolListBytes-totalBytes {
			return nil, fmt.Errorf("mcp tools/list %s: exceeded %d response bytes", c.name, maxToolListBytes)
		}
		totalBytes += len(resp.Result)
		var result toolsListResult
		if err := mcpLongLivedJSON.Unmarshal(resp.Result, &result); err != nil {
			return nil, fmt.Errorf("mcp tools/list %s: decode: %w", c.name, err)
		}
		if result.Tools == nil {
			return nil, fmt.Errorf("mcp tools/list %s: missing tools array", c.name)
		}
		if len(result.Tools) > maxListedTools-len(defs) {
			return nil, fmt.Errorf("mcp tools/list %s: exceeded %d tools", c.name, maxListedTools)
		}
		for _, def := range result.Tools {
			if def.Name == "" {
				// The wrapper skips unnamed tools.
				continue
			}
			if _, dup := names[def.Name]; dup {
				return nil, fmt.Errorf("mcp tools/list %s: duplicate tool name %q", c.name, def.Name)
			}
			names[def.Name] = struct{}{}
		}
		defs = append(defs, result.Tools...)
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("mcp tools/list %s: %w", c.name, err)
		}
		if result.NextCursor == nil {
			log.Debugf("mcp tools discovered server=%v count=%v", c.name, len(defs))
			return defs, nil
		}
		cursor = result.NextCursor
		if _, dup := cursors[*cursor]; dup {
			return nil, fmt.Errorf("mcp tools/list %s: repeated cursor", c.name)
		}
		cursors[*cursor] = struct{}{}
	}
	return nil, fmt.Errorf("mcp tools/list %s: exceeded %d pages", c.name, maxToolListPages)
}
