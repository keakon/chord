package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type TaskGroupMember struct {
	TaskID  string `json:"task_id"`
	Attempt uint64 `json:"attempt"`
}

type TaskGroupCreateRequest struct {
	TaskIDs     []string
	Label       string
	SemanticKey string
}

type TaskGroupCreateResult struct {
	GroupID    string            `json:"group_id"`
	Members    []TaskGroupMember `json:"members"`
	JoinPolicy string            `json:"join_policy"`
}

type TaskGroupCreator interface {
	CreateTaskGroup(context.Context, TaskGroupCreateRequest) (TaskGroupCreateResult, error)
}

type TaskGroupCreateTool struct {
	creator TaskGroupCreator
}

func NewTaskGroupCreateTool(creator TaskGroupCreator) *TaskGroupCreateTool {
	return &TaskGroupCreateTool{creator: creator}
}

func (TaskGroupCreateTool) Name() string { return NameTaskGroupCreate }

func (TaskGroupCreateTool) Description() string {
	return "Create a durable immutable group from the current attempts of direct-owned tasks. Use semantic_key for idempotent retries across compaction or restart. Members cannot be added, removed, or advanced to later attempts."
}

func (TaskGroupCreateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_ids": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"minItems": 1, "maxItems": maxTaskCollectMembers,
				"description": "Stable direct-owned task IDs whose current attempts become immutable group members.",
			},
			"label":        map[string]any{"type": "string", "description": "Optional human-readable group label."},
			"semantic_key": map[string]any{"type": "string", "description": "Optional owner-scoped idempotency key."},
		},
		"required": []string{"task_ids"}, "additionalProperties": false,
	}
}

func (TaskGroupCreateTool) IsReadOnly() bool { return false }

func (TaskGroupCreateTool) ConcurrencyPolicy(json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{Resource: "coordination:task-groups", Mode: ConcurrencyModeWrite}
}

func (t *TaskGroupCreateTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		TaskIDs     []string `json:"task_ids"`
		Label       string   `json:"label"`
		SemanticKey string   `json:"semantic_key"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if t.creator == nil {
		return "", fmt.Errorf("task group creation is unavailable")
	}
	taskIDs, err := DedupeTaskIDs(args.TaskIDs)
	if err != nil {
		return "", err
	}
	if len(taskIDs) == 0 {
		return "", fmt.Errorf("task_ids is required")
	}
	if len(taskIDs) > maxTaskCollectMembers {
		return "", fmt.Errorf("task_ids exceeds maximum member count %d", maxTaskCollectMembers)
	}
	result, err := t.creator.CreateTaskGroup(ctx, TaskGroupCreateRequest{
		TaskIDs: taskIDs, Label: strings.TrimSpace(args.Label), SemanticKey: strings.TrimSpace(args.SemanticKey),
	})
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal task group: %w", err)
	}
	return string(out), nil
}
