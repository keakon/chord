package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// AgentInfo holds the name and description of an available SubAgent type.
type AgentInfo struct {
	Name             string
	Description      string
	Capabilities     []string
	PreferredTasks   []string
	WriteMode        string
	DelegationPolicy string
}

// WriteScope declares the paths a delegated task expects to modify. Whether
// the worker may modify files at all is decided by its role's permission
// ruleset (a role that denies write/edit/delete/apply_patch registers none of
// those tools). The declaration is advisory: it gates no tool execution, and
// only feeds sibling-overlap hints and coordination, so a worker may still
// write any file its permission rules allow.
type WriteScope struct {
	Files      []string `json:"files,omitempty"`
	PathPrefix []string `json:"path_prefix,omitempty"`
	Modules    []string `json:"modules,omitempty"`
}

// writeScopePathProperties returns the schema for the path lists an
// expected_write_scope declares. The map is freshly allocated because callers
// extend their own copy.
func writeScopePathProperties() map[string]any {
	return map[string]any{
		"files":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"path_prefix": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"modules":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}
}

func (s WriteScope) Normalized() WriteScope {
	return WriteScope{
		Files:      dedupeTrimmedStrings(s.Files),
		PathPrefix: dedupeTrimmedStrings(s.PathPrefix),
		Modules:    dedupeTrimmedStrings(s.Modules),
	}
}

func (s WriteScope) Empty() bool {
	s = s.Normalized()
	return len(s.Files) == 0 && len(s.PathPrefix) == 0 && len(s.Modules) == 0
}

func (s WriteScope) Summary() string {
	s = s.Normalized()
	parts := make([]string, 0, 3)
	if len(s.Files) > 0 {
		parts = append(parts, "files="+strings.Join(s.Files, ","))
	}
	if len(s.PathPrefix) > 0 {
		parts = append(parts, "paths="+strings.Join(s.PathPrefix, ","))
	}
	if len(s.Modules) > 0 {
		parts = append(parts, "modules="+strings.Join(s.Modules, ","))
	}
	return strings.Join(parts, "; ")
}

func dedupeTrimmedStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type TaskHandle struct {
	Status             string     `json:"status"`
	TaskID             string     `json:"task_id"`
	AgentID            string     `json:"agent_id"`
	PreviousAgentID    string     `json:"previous_agent_id,omitempty"`
	Rehydrated         bool       `json:"rehydrated,omitempty"`
	Message            string     `json:"message"`
	PlanTaskRef        string     `json:"plan_task_ref,omitempty"`
	SemanticTaskKey    string     `json:"semantic_task_key,omitempty"`
	ExpectedWriteScope WriteScope `json:"expected_write_scope"`
	ScopeConflict      bool       `json:"scope_conflict,omitempty"`
	SuggestedTaskID    string     `json:"suggested_task_id,omitempty"`
	SuggestedAgentID   string     `json:"suggested_agent_id,omitempty"`
	SuggestedAction    string     `json:"suggested_action,omitempty"`
	DuplicateDetected  bool       `json:"duplicate_detected,omitempty"`
}

// SubAgentCreator is the interface used by TaskTool to create SubAgents.
// Defined here (in the tools package) to avoid circular imports — the agent
// package imports tools, so tools cannot import agent. MainAgent implements
// this interface and is injected at construction time.
type SubAgentCreator interface {
	// CreateSubAgent creates a new SubAgent for the given task.
	// Returns a structured handle for the created worker, or an error.
	CreateSubAgent(ctx context.Context, description, agentType string, planTaskRef, semanticTaskKey string, expectedWriteScope WriteScope) (TaskHandle, error)
	// AvailableSubAgents returns the list of subagent-mode agents that can be
	// used with the Delegate tool. Used to populate the agent_type description.
	AvailableSubAgents() []AgentInfo
}

// DelegateTool delegates a task to a SubAgent for parallel execution. Only
// available to the MainAgent.
type DelegateTool struct {
	creator SubAgentCreator
}

// NewDelegateTool creates a DelegateTool backed by the given SubAgentCreator.
func NewDelegateTool(creator SubAgentCreator) *DelegateTool {
	return &DelegateTool{creator: creator}
}

type delegateArgs struct {
	Description     string `json:"description"`
	AgentType       string `json:"agent_type"`
	PlanTaskRef     string `json:"plan_task_ref,omitempty"`
	SemanticTaskKey string `json:"semantic_task_key,omitempty"`
	// Pointer so an omitted scope is distinguishable from an explicitly empty
	// one: the field is required, so omission is a missing-argument error,
	// while an empty object is valid only for roles whose surface registers no
	// file-modifying tools.
	ExpectedWriteScope *WriteScope `json:"expected_write_scope"`
}

func (DelegateTool) Name() string { return NameDelegate }

func (DelegateTool) Description() string {
	return "Delegate a task to a SubAgent for parallel execution. " +
		"The SubAgent runs independently with its own context and tool access, and reports back when done. " +
		"Prefer using Read, Grep, and Shell directly when one or a few tool calls suffice; " +
		"use Delegate only for substantial sub-work that benefits from a dedicated agent (e.g. multi-file edits or independent plan items). " +
		"Your system prompt's delegation workflow section governs when to continue an existing task with Notify versus creating a new delegate, and when parallel delegates are safe. " +
		"IMPORTANT: The result is delivered asynchronously and flows back to you automatically — do NOT poll or retrieve SubAgent results. " +
		"The returned task_id is the stable durable handle for that delegate; reuse it with Notify or Cancel for follow-up instead of creating a duplicate delegate. " +
		"Roles that can write files must declare a non-empty expected_write_scope; a read-only delegation pairs a read-only role with an empty scope object {}."
}

// IsAvailable reports whether the DelegateTool should be registered.
// Returns false when no subagent-mode agents are configured, so the tool
// is omitted entirely from the LLM's tool list.
func (t *DelegateTool) IsAvailable() bool {
	if t.creator == nil {
		return false
	}
	return len(t.creator.AvailableSubAgents()) > 0
}

func (t *DelegateTool) Parameters() map[string]any {
	var sb strings.Builder
	sb.WriteString("Agent type to use for this task. Available types:\n")

	agents := t.creator.AvailableSubAgents()
	enum := make([]string, len(agents))
	for i, a := range agents {
		enum[i] = a.Name
		sb.WriteString("- ")
		sb.WriteString(a.Name)
		if a.Description != "" {
			sb.WriteString(": ")
			sb.WriteString(a.Description)
		}
		meta := make([]string, 0, 5)
		if len(a.Capabilities) > 0 {
			meta = append(meta, "capabilities="+strings.Join(a.Capabilities, ","))
		}
		if len(a.PreferredTasks) > 0 {
			meta = append(meta, "preferred="+strings.Join(a.PreferredTasks, ","))
		}
		if a.WriteMode != "" {
			meta = append(meta, "write_mode="+a.WriteMode)
		}
		if a.DelegationPolicy != "" {
			meta = append(meta, "delegation_policy="+a.DelegationPolicy)
		}
		// Every row states the role's empty-scope rule so the model does not
		// need to read the expected_write_scope prose to pick a role. A role
		// that registers no file-writing tools accepts an empty scope; a
		// write-capable role requires a non-empty declaration.
		if delegateTargetRoleRegistersNoFileWriteTools(t.creator, a.Name) {
			meta = append(meta, "empty_scope=allowed")
		} else {
			meta = append(meta, "non_empty_scope=required")
		}
		if len(meta) > 0 {
			sb.WriteString(" [")
			sb.WriteString(strings.Join(meta, "; "))
			sb.WriteString("]")
		}
		sb.WriteByte('\n')
	}

	scopeProperties := writeScopePathProperties()

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{
				"type":        "string",
				"description": "Detailed task description and requirements for the SubAgent. Be specific about what needs to be done, which files to modify, and any constraints.",
			},
			"plan_task_ref": map[string]any{
				"type":        "string",
				"description": "Optional stable plan task reference for this deliverable. Reuse it when continuing the same plan item; leave empty for ad-hoc work.",
			},
			"semantic_task_key": map[string]any{
				"type":        "string",
				"description": "Optional semantic key for duplicate detection. Use a concise stable identifier for the same deliverable, not for unrelated new work.",
			},
			"expected_write_scope": map[string]any{
				"type":                 "object",
				"description":          "Required declaration of the paths this task expects to modify. It is a coordination declaration, not an enforced boundary: the runtime does not block the worker's file tools outside it, and which tools the worker may actually use is decided by the role's permission rules. The declaration feeds sibling-overlap hints (a started handle may carry scope_conflict with suggested_task_id) and your own planning, so declare the narrowest files/path_prefix/modules that honestly cover the work. A task that will not modify files should pick an agent_type whose role registers no file-writing tools (its permission rules deny write, edit, delete, and apply_patch) and pass an empty object {}: the empty scope is accepted only for such roles, because a role that can write files must still declare what it plans to touch.",
				"properties":           scopeProperties,
				"additionalProperties": false,
			},
			"agent_type": map[string]any{
				"type":        "string",
				"description": sb.String(),
				"enum":        enum,
			},
		},
		"required":             []string{"description", "agent_type", "expected_write_scope"},
		"additionalProperties": false,
	}
}

func (DelegateTool) IsReadOnly() bool { return false }

// AgentFileWriteSurface is implemented by SubAgentCreators that can report
// whether a target agent definition's role registers any file-modifying tool.
// Delegate uses it to accept an empty expected_write_scope: a task whose role
// registers no file-modifying tools cannot write files, so it has nothing to
// declare, while a role that can write files must still declare what the task
// plans to touch so sibling-overlap hints stay meaningful. Creators without
// this capability are treated conservatively as write-capable.
type AgentFileWriteSurface interface {
	// AgentRoleRegistersNoFileWriteTools reports whether the role behind the
	// given agent_type registers none of write, edit, delete, or apply_patch.
	AgentRoleRegistersNoFileWriteTools(agentType string) bool
}

func delegateTargetRoleRegistersNoFileWriteTools(creator SubAgentCreator, agentType string) bool {
	if creator == nil || strings.TrimSpace(agentType) == "" {
		return false
	}
	surface, ok := creator.(AgentFileWriteSurface)
	return ok && surface.AgentRoleRegistersNoFileWriteTools(agentType)
}

// delegateWriteScopeRequiredError builds the operator-facing repair
// instruction for a Delegate call that declares no usable write scope for a
// role that can write files. The schema marks the field required, but a model
// can still send `{}`; an empty scope is valid only when the chosen
// agent_type's role registers no file-modifying tools (see
// AgentFileWriteSurface). When the creator exposes read-only agent types, the
// instruction names them as the alternative that accepts an empty scope.
func delegateWriteScopeRequiredError(creator SubAgentCreator) error {
	msg := "expected_write_scope is required: this task must either declare at least one of files/path_prefix/modules covering what it plans to write, " +
		"or use an agent_type whose role registers no file-writing tools and pass an empty object {}"
	var readOnlyAgentTypes []string
	if creator != nil {
		for _, a := range creator.AvailableSubAgents() {
			if delegateTargetRoleRegistersNoFileWriteTools(creator, a.Name) {
				readOnlyAgentTypes = append(readOnlyAgentTypes, a.Name)
			}
		}
	}
	if len(readOnlyAgentTypes) > 0 {
		msg += " Available read-only agent types: " + strings.Join(readOnlyAgentTypes, ", ")
	}
	return errors.New(msg)
}

func (t *DelegateTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a delegateArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Description == "" {
		return "", fmt.Errorf("description is required")
	}
	if a.ExpectedWriteScope == nil {
		return "", delegateWriteScopeRequiredError(t.creator)
	}
	expectedWriteScope := a.ExpectedWriteScope.Normalized()
	if expectedWriteScope.Empty() && !delegateTargetRoleRegistersNoFileWriteTools(t.creator, a.AgentType) {
		return "", delegateWriteScopeRequiredError(t.creator)
	}

	if t.creator == nil {
		return "", fmt.Errorf("task creation not available (no SubAgentCreator configured)")
	}

	handle, err := t.creator.CreateSubAgent(ctx, a.Description, a.AgentType, a.PlanTaskRef, a.SemanticTaskKey, expectedWriteScope)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(handle.Status) == "" {
		handle.Status = "started"
	}
	if strings.TrimSpace(handle.Message) == "" {
		handle.Message = "running in background"
	}
	out, err := json.Marshal(handle)
	if err != nil {
		return "", fmt.Errorf("marshal task handle: %w", err)
	}
	return string(out), nil
}
