package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AgentInfo holds the name and description of an available SubAgent type.
type AgentInfo struct {
	Name        string
	Description string
}

type TaskHandle struct {
	Status            string `json:"status"`
	TaskID            string `json:"task_id"`
	AgentID           string `json:"agent_id"`
	PreviousAgentID   string `json:"previous_agent_id,omitempty"`
	Rehydrated        bool   `json:"rehydrated,omitempty"`
	Message           string `json:"message"`
	PlanTaskRef       string `json:"plan_task_ref,omitempty"`
	SemanticTaskKey   string `json:"semantic_task_key,omitempty"`
	SuggestedTaskID   string `json:"suggested_task_id,omitempty"`
	SuggestedAgentID  string `json:"suggested_agent_id,omitempty"`
	SuggestedAction   string `json:"suggested_action,omitempty"`
	DuplicateDetected bool   `json:"duplicate_detected,omitempty"`
}

// SubAgentRequest is one delegation request. It is a struct rather than a
// positional parameter list because the delegation surface keeps growing (the
// result contract is one such field), and every added parameter would otherwise
// rewrite the argument order at each call site.
type SubAgentRequest struct {
	Description     string
	AgentType       string
	PlanTaskRef     string
	SemanticTaskKey string
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
		"The result is delivered asynchronously and flows back to you automatically; do not poll for or retrieve SubAgent results. " +
		"The returned task_id is the stable durable handle for that delegate and identifies the same task across follow-up attempts."
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
		sb.WriteByte('\n')
	}

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
				"description": "Optional identity for a deliverable that may be delegated again. Reusing a key that is still active is rejected as already_exists so the existing task can be continued; distinct deliverables must use distinct keys, and one-off work should omit it.",
			},
			"result_schema": map[string]any{
				"type":        "object",
				"description": "Optional result contract the worker's reported result must satisfy. Declare it as a JSON-schema-like object using only type, required, properties, items, enum, and description; the top-level type must be object. A supported schema within the size limits is enforced on both the inline result and a result stored by save_artifact and passed as result_ref. Unsupported keywords, an unsupported type name, or a schema nested too deeply are rejected here before the worker starts. Omit it for a task with no required result shape.",
			},
			"agent_type": map[string]any{
				"type":        "string",
				"description": sb.String(),
				"enum":        enum,
			},
		},
		"required":             []string{"description", "agent_type"},
		"additionalProperties": false,
	}
}

func (DelegateTool) IsReadOnly() bool { return false }

func (t *DelegateTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a delegateArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Description == "" {
		return "", fmt.Errorf("description is required")
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
		Description:     a.Description,
		AgentType:       a.AgentType,
		PlanTaskRef:     a.PlanTaskRef,
		SemanticTaskKey: a.SemanticTaskKey,
		ResultSchema:    resultSchema,
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
