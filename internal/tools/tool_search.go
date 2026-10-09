package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

const (
	DiscoveryNotFound        = "not_found"
	DiscoveryDenied          = "permission_denied"
	DiscoveryDisabled        = "disabled"
	DiscoveryUnavailable     = "unavailable"
	DiscoveryBudgetExceeded  = "budget_exceeded"
	DiscoveryMaxResults      = 5
	DiscoveryMaxResultTokens = 4096
	DiscoveryMaxResultBytes  = 12000
	discoveryMaxQueryChars   = 1000
)

// DeferredTool opts into discovery without changing connection or permission.
type DeferredTool interface {
	Tool
	IsDeferred() bool
}

func IsDeferredTool(t Tool) bool {
	d, ok := t.(DeferredTool)
	return ok && d.IsDeferred()
}

type ToolSearchBackend interface {
	HasDiscoverableTools() bool
	SearchTools(context.Context, string, []string) (message.ToolDiscoveryResult, error)
}

type ToolSearchTool struct{ backend ToolSearchBackend }

func NewToolSearchTool(backend ToolSearchBackend) ToolSearchTool {
	return ToolSearchTool{backend: backend}
}
func (ToolSearchTool) Name() string     { return NameToolSearch }
func (ToolSearchTool) IsReadOnly() bool { return true }
func (t ToolSearchTool) IsAvailable() bool {
	return t.backend != nil && t.backend.HasDiscoverableTools()
}
func (ToolSearchTool) Description() string {
	return "Find and load available MCP tools using a natural-language query or exact tool_names. Loads a small set of definitions for subsequent calls; it does not execute tools or enable disconnected/disabled servers. Definitions stay loaded while their successful discovery records remain in history. If compaction removes a record, load that tool again by name."
}
func (ToolSearchTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"query":      map[string]any{"type": "string", "maxLength": discoveryMaxQueryChars},
		"tool_names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": DiscoveryMaxResults},
	}}
}
func (t ToolSearchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Query     string   `json:"query"`
		ToolNames []string `json:"tool_names"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid discovery arguments: %w", err)
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" && len(args.ToolNames) == 0 {
		return "", fmt.Errorf("query or tool_names is required")
	}
	if utf8.RuneCountInString(args.Query) > discoveryMaxQueryChars || len(args.ToolNames) > DiscoveryMaxResults {
		return "", fmt.Errorf("discovery input exceeds limit")
	}
	if t.backend == nil {
		return "", fmt.Errorf("tool discovery is unavailable")
	}
	result, err := t.backend.SearchTools(ctx, args.Query, args.ToolNames)
	if err != nil {
		return "", err
	}
	raw, err = json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode discovery result: %w", err)
	}
	return string(raw), nil
}
