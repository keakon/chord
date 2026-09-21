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
	Name        string
	Description string
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

// SubAgentRequest is one delegation request. It is a struct rather than a
// positional parameter list because the delegation surface keeps growing (the
// result contract is one such field), and every added parameter would otherwise
// rewrite the argument order at each call site.
type SubAgentRequest struct {
	Description        string
	AgentType          string
	PlanTaskRef        string
	SemanticTaskKey    string
	ExpectedWriteScope WriteScope
	// ResultSchema is the canonical encoding of the delegated result contract
	// as validated by CompileResultSchema, or empty for a task with no
	// contract.
	ResultSchema json.RawMessage
}

// SubAgentCreator is the interface used by TaskTool to create SubAgents.
// Defined here (in the tools package) to avoid circular imports — the agent
// package imports tools, so tools cannot import agent. MainAgent implements
// this interface and is injected at construction time.
type SubAgentCreator interface {
	// CreateSubAgent creates a new SubAgent for the given task.
	// Returns a structured handle for the created worker, or an error.
	CreateSubAgent(ctx context.Context, req SubAgentRequest) (TaskHandle, error)
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
	// Optional result contract the worker's reported result must satisfy. The
	// declared schema is only an object at the tool surface; the accepted
	// subset is compiled and checked here before a worker is started.
	ResultSchema json.RawMessage `json:"result_schema,omitempty"`
}

func (DelegateTool) Name() string { return NameDelegate }

func (DelegateTool) Description() string {
	return "Delegate a task to a SubAgent for parallel execution. " +
		"The SubAgent runs independently with its own context and tool access, and reports back when done. " +
		"Your system prompt's delegation workflow governs task selection, follow-up, and safe parallelism. " +
		"IMPORTANT: The result is delivered asynchronously and flows back to you automatically — do NOT poll or retrieve SubAgent results. " +
		"The returned task_id is the stable durable handle for that delegate and identifies the same task across follow-up attempts. " +
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
		// Every row states the role's empty-scope rule so the model does not
		// need to read the expected_write_scope prose to pick a role. A role
		// that registers no file-writing tools accepts an empty scope; a
		// write-capable role requires a non-empty declaration.
		sb.WriteString(" [")
		if delegateTargetRoleRegistersNoFileWriteTools(t.creator, a.Name) {
			sb.WriteString("empty_scope=allowed")
		} else {
			sb.WriteString("non_empty_scope=required")
		}
		sb.WriteByte(']')
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
			"result_schema": map[string]any{
				"type":        "object",
				"description": "Optional result contract the worker's reported result must satisfy. Declare it as a JSON-schema-like object using only type, required, properties, items, enum, and description; the top-level type must be object. A supported schema within the size limits is enforced on both the inline result and a result stored by save_artifact and passed as result_ref. Unsupported keywords, an unsupported type name, or a schema nested too deeply are rejected here before the worker starts. Omit it for a task with no required result shape.",
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

	// Reject an unusable contract here, before any worker is admitted: a bad
	// schema must not cost a whole worker round to discover. Compilation also
	// canonicalizes what is handed to the creator and persisted with the task.
	_, resultSchema, err := CompileResultSchema(a.ResultSchema)
	if err != nil {
		return "", err
	}

	handle, err := t.creator.CreateSubAgent(ctx, SubAgentRequest{
		Description:        a.Description,
		AgentType:          a.AgentType,
		PlanTaskRef:        a.PlanTaskRef,
		SemanticTaskKey:    a.SemanticTaskKey,
		ExpectedWriteScope: expectedWriteScope,
		ResultSchema:       resultSchema,
	})
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
