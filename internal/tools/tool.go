package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
)

// Tool is the interface that every tool must implement.
type Tool interface {
	// Name returns the unique identifier for this tool.
	Name() string
	// Description returns a human-readable description of what the tool does.
	Description() string
	// Parameters returns a JSON Schema object describing the tool's input.
	Parameters() map[string]any
	// Execute runs the tool with the given JSON arguments and returns output or an error.
	Execute(ctx context.Context, args json.RawMessage) (string, error)
	// IsReadOnly returns true if the tool does not modify any state (files, processes, etc.).
	IsReadOnly() bool
}

// ConcurrencyAwareTool can classify a finalized tool call for safe batching.
// Unknown tools are treated conservatively as exclusive by the agent runtime.
type ConcurrencyAwareTool interface {
	Tool
	ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy
}

// DescriptiveTool can tailor its model-facing description using the current
// registry surface. The registry passes the visible tool names that will be
// exposed to the model in the current session/role.
type DescriptiveTool interface {
	Tool
	DescriptionForTools(visible map[string]struct{}) string
}

// AvailableTool can opt out of registration in the LLM-visible tool list even if
// it is present in the registry. This is used for tools whose backing runtime
// provider is not yet configured.
type AvailableTool interface {
	Tool
	IsAvailable() bool
}

// RulesetAwareVisibilityTool can further refine whether a registered tool
// should be exposed to the model under the current ruleset. This is used for
// grouped capabilities where one tool's visibility depends on another tool's
// permission family.
type RulesetAwareVisibilityTool interface {
	Tool
	VisibleWithRuleset(ruleset permission.Ruleset) bool
}

// contextKey is an unexported type used for context value keys in this package.
type contextKey int

const (
	agentIDKey contextKey = iota
	eventSenderKey
	sessionDirKey
	taskIDKey
)

// WithAgentID returns a new context that carries the given agent ID.
func WithAgentID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentIDKey, id)
}

// AgentIDFromContext extracts the agent ID from the context, or returns "" if absent.
func AgentIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(agentIDKey).(string); ok {
		return v
	}
	return ""
}

// WithSessionDir returns a new context that carries the session directory path.
func WithSessionDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionDirKey, dir)
}

// SessionDirFromContext extracts the session directory from the context, or returns "" if absent.
func SessionDirFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(sessionDirKey).(string); ok {
		return v
	}
	return ""
}

// WithTaskID returns a new context that carries the current task ID.
func WithTaskID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, taskIDKey, id)
}

// TaskIDFromContext extracts the task ID from the context, or returns "" if absent.
func TaskIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(taskIDKey).(string); ok {
		return v
	}
	return ""
}

// WithEventSender returns a new context that carries the given event sender.
func WithEventSender(ctx context.Context, sender EventSender) context.Context {
	if sender == nil {
		return ctx
	}
	return context.WithValue(ctx, eventSenderKey, sender)
}

// EventSenderFromContext extracts the event sender from the context, or returns nil if absent.
func EventSenderFromContext(ctx context.Context) EventSender {
	if v, ok := ctx.Value(eventSenderKey).(EventSender); ok {
		return v
	}
	return nil
}

// Registry stores and manages a collection of tools.
// All methods are safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry. If a tool with the same name already exists
// it is silently replaced.
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// ListTools returns all registered tools sorted alphabetically by name.
func (r *Registry) ListTools() []Tool {
	r.mu.RLock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})
	return out
}

func toolNamesSet(tools []Tool) map[string]struct{} {
	visible := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		visible[t.Name()] = struct{}{}
	}
	return visible
}

func toolDescription(t Tool, visible map[string]struct{}) string {
	if descriptive, ok := t.(DescriptiveTool); ok {
		return descriptive.DescriptionForTools(visible)
	}
	return t.Description()
}

// ListDefinitions converts every registered tool into a message.ToolDefinition
// suitable for sending to an LLM API.
func (r *Registry) ListDefinitions() []message.ToolDefinition {
	tools := r.ListTools()
	visible := toolNamesSet(tools)
	defs := make([]message.ToolDefinition, len(tools))
	for i, t := range tools {
		defs[i] = message.ToolDefinition{
			Name:        t.Name(),
			Description: toolDescription(t, visible),
			InputSchema: t.Parameters(),
		}
	}
	return defs
}

// Clone returns a shallow copy of the registry. The new registry contains the
// same tool instances as the original; callers can Register additional tools
// on the clone without affecting the original.
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := NewRegistry()
	for name, t := range r.tools {
		clone.tools[name] = t
	}
	return clone
}

// Execute looks up a tool by name and runs it. Returns an error if the tool is
// not found.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("tool not found: %s", name)
	}
	return t.Execute(ctx, args)
}
