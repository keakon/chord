package tools

import (
	"context"
	"encoding/json"
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

type WriteScope struct {
	Files      []string `json:"files,omitempty"`
	PathPrefix []string `json:"path_prefix,omitempty"`
	Modules    []string `json:"modules,omitempty"`
	ReadOnly   bool     `json:"read_only,omitempty"`
}

func (s WriteScope) Normalized() WriteScope {
	return WriteScope{
		Files:      dedupeTrimmedStrings(s.Files),
		PathPrefix: dedupeTrimmedStrings(s.PathPrefix),
		Modules:    dedupeTrimmedStrings(s.Modules),
		ReadOnly:   s.ReadOnly,
	}
}

func (s WriteScope) Empty() bool {
	s = s.Normalized()
	return !s.ReadOnly && len(s.Files) == 0 && len(s.PathPrefix) == 0 && len(s.Modules) == 0
}

func (s WriteScope) Summary() string {
	s = s.Normalized()
	parts := make([]string, 0, 4)
	if s.ReadOnly {
		parts = append(parts, "read-only")
	}
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
	ExpectedWriteScope WriteScope `json:"expected_write_scope,omitempty"`
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
	Description        string     `json:"description"`
	AgentType          string     `json:"agent_type"`
	PlanTaskRef        string     `json:"plan_task_ref,omitempty"`
	SemanticTaskKey    string     `json:"semantic_task_key,omitempty"`
	ExpectedWriteScope WriteScope `json:"expected_write_scope,omitempty"`
}

func (DelegateTool) Name() string { return "Delegate" }

func (DelegateTool) Description() string {
	return "Delegate a task to a SubAgent for parallel execution. " +
		"The SubAgent runs independently and reports back when done. " +
		"Prefer using Read, Grep, and Bash directly when one or a few tool calls suffice; " +
		"use Delegate only for substantial sub-work that benefits from a dedicated agent (e.g. multi-file edits or independent plan items). " +
		"Use Notify(existing) for the same task's follow-up, clarification, rework, added tests, added verification, or final acceptance work; use Delegate(new) for a genuinely new, independently trackable task. " +
		"Only parallelize tasks when their write scopes are clearly independent; do not create concurrent workers that may edit the same file or tightly coupled targets. " +
		"If unsure whether work belongs to the same deliverable, prefer continuing the existing task when continuity is strong, and prefer a new delegate when independence is strong. " +
		"Each SubAgent has its own context and tool access. " +
		"IMPORTANT: The result is delivered asynchronously — do NOT poll or retrieve SubAgent results with Spawn/SpawnStop. " +
		"The returned task_id is the stable durable handle for that delegate; reuse it with Notify or Cancel instead of spawning a duplicate delegate for follow-up."
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
		meta := make([]string, 0, 4)
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
		if len(meta) > 0 {
			sb.WriteString(" [")
			sb.WriteString(strings.Join(meta, "; "))
			sb.WriteString("]")
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
				"description": "Optional semantic key for duplicate detection. Use a concise stable identifier for the same deliverable, not for unrelated new work.",
			},
			"expected_write_scope": map[string]any{
				"type":        "object",
				"description": "Optional write-scope declaration used for concurrency guardrails. Set read_only=true for research-only tasks.",
				"properties": map[string]any{
					"files":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"path_prefix": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"modules":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"read_only":   map[string]any{"type": "boolean"},
				},
				"additionalProperties": false,
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

	handle, err := t.creator.CreateSubAgent(ctx, a.Description, a.AgentType, a.PlanTaskRef, a.SemanticTaskKey, a.ExpectedWriteScope.Normalized())
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
