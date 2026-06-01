package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"
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

type ToolConcurrencyClass int

const (
	ToolConcurrencyClassUnknown ToolConcurrencyClass = iota
	ToolConcurrencyClassReadOnly
	ToolConcurrencyClassMutating
	ToolConcurrencyClassExclusive
)

// ConcurrencyClassForTool returns a conservative high-level batching class.
// This is shared by speculative and finalized orchestration so both layers
// preserve the same core rule: only consecutive concurrency-safe read-only
// tools may run together; everything else becomes a serialization boundary.
func ConcurrencyClassForTool(registry *Registry, toolName string, args json.RawMessage) ToolConcurrencyClass {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return ToolConcurrencyClassExclusive
	}
	if registry == nil {
		return ToolConcurrencyClassExclusive
	}
	tool, ok := registry.Get(name)
	if !ok {
		return ToolConcurrencyClassExclusive
	}
	policy := PolicyForTool(registry, name, args)
	policy = normalizeConcurrencyPolicy(name, policy)
	if policy.Mode == ConcurrencyModeExclusive && !isReadOnlyConcurrencySafeTool(name, args) {
		return ToolConcurrencyClassExclusive
	}
	if isReadOnlyConcurrencySafeTool(name, args) {
		return ToolConcurrencyClassReadOnly
	}
	if !tool.IsReadOnly() {
		return ToolConcurrencyClassMutating
	}
	if policy.Mode == ConcurrencyModeExclusive {
		return ToolConcurrencyClassExclusive
	}
	return ToolConcurrencyClassExclusive
}

func isReadOnlyConcurrencySafeTool(toolName string, args json.RawMessage) bool {
	switch strings.TrimSpace(toolName) {
	case NameRead, NameGrep, NameGlob, NameLsp, NameWebFetch, NameReadArtifact, NameSkill, NameSpawnStatus:
		return true
	case NameShell:
		return shellReadOnlyCommandAllowed(args)
	default:
		return false
	}
}

func shellReadOnlyCommandAllowed(args json.RawMessage) bool {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return false
	}
	command := strings.TrimSpace(parsed.Command)
	if command == "" || containsShellMetachar(command) {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "pwd", "ls", "cat", "which":
		return true
	case "git":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "status", "log", "diff", "show", "branch", "rev-parse":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func containsShellMetachar(command string) bool {
	for _, r := range command {
		switch r {
		case '|', '&', ';', '>', '<', '$', '`', '\\', '(', ')', '*', '?', '[', ']', '{', '}', '\n', '\r':
			return true
		}
	}
	return false
}

func pathContainsResourcePath(basePath, targetPath string) bool {
	basePath = filepath.Clean(strings.TrimSpace(basePath))
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if basePath == "" || targetPath == "" {
		return false
	}
	if basePath == "." {
		return true
	}
	if basePath == targetPath {
		return true
	}
	rel, err := filepath.Rel(basePath, targetPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
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
	r.tools[NormalizeName(tool.Name())] = tool
}

// Unregister removes a tool from the registry by name.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, NormalizeName(name))
}

// UnregisterPrefix removes every tool whose name has the given prefix.
// It returns the number of removed tools.
func (r *Registry) UnregisterPrefix(prefix string) int {
	if prefix == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			removed++
		}
	}
	return removed
}

// Get looks up a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[NormalizeName(name)]
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
		visible[NormalizeName(t.Name())] = struct{}{}
	}
	return visible
}

func toolDescription(t Tool, visible map[string]struct{}) string {
	if descriptive, ok := t.(DescriptiveTool); ok {
		return descriptive.DescriptionForTools(visible)
	}
	return t.Description()
}

func visibleToolNamesIfNeeded(tools []Tool) map[string]struct{} {
	for _, t := range tools {
		if _, ok := t.(DescriptiveTool); ok {
			return toolNamesSet(tools)
		}
	}
	return nil
}

// ListDefinitions converts every registered tool into a message.ToolDefinition
// suitable for sending to an LLM API.
func (r *Registry) ListDefinitions() []message.ToolDefinition {
	tools := r.ListTools()
	visible := visibleToolNamesIfNeeded(tools)
	defs := make([]message.ToolDefinition, len(tools))
	for i, t := range tools {
		defs[i] = message.ToolDefinition{
			Name:        NormalizeName(t.Name()),
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
	maps.Copy(clone.tools, r.tools)
	return clone
}

// Execute looks up a tool by name and runs it. Returns an error if the tool is
// not found.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	name = NormalizeName(name)
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("tool not found: %s", name)
	}
	return t.Execute(ctx, args)
}
