package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	maxTaskCollectMembers = 64
	maxTaskCollectTimeout = 10 * time.Minute
	defaultCollectTimeout = 5 * time.Minute
)

// DedupeTaskIDs trims, drops empty values, and removes duplicates while
// preserving first-seen order. It returns the empty error string for a zero
// input, so callers can layer their own "required" / "max members" checks.
func DedupeTaskIDs(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("task_ids must not contain empty values")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

type TaskCollectRequest struct {
	TaskIDs []string
	GroupID string
	Wait    bool
	Timeout time.Duration
}

type TaskCollectItem struct {
	TaskID                string        `json:"task_id"`
	MemberAttempt         uint64        `json:"member_attempt"`
	CurrentAttempt        uint64        `json:"current_attempt"`
	LifecycleRevision     uint64        `json:"lifecycle_revision"`
	State                 string        `json:"state"`
	Settled               bool          `json:"settled"`
	Outcome               string        `json:"outcome,omitempty"`
	Summary               string        `json:"summary,omitempty"`
	ResultType            string        `json:"result_type,omitempty"`
	ArtifactRefs          []ArtifactRef `json:"artifact_refs,omitempty"`
	ResultRef             *ResultRef    `json:"result_ref,omitempty"`
	UpdatedAt             time.Time     `json:"updated_at"`
	SettledAt             time.Time     `json:"settled_at"`
	SettlementDurable     bool          `json:"settlement_durable"`
	TranscriptPersistence string        `json:"transcript_persistence,omitempty"`
}

type TaskCollectResult struct {
	GroupID          string            `json:"group_id,omitempty"`
	AllSettled       bool              `json:"all_settled"`
	AllDurable       bool              `json:"all_durable"`
	TimedOut         bool              `json:"timed_out"`
	SnapshotRevision uint64            `json:"snapshot_revision"`
	Tasks            []TaskCollectItem `json:"tasks"`
}

type TaskCollector interface {
	CollectTasks(context.Context, TaskCollectRequest) (TaskCollectResult, error)
}

type TaskCollectTool struct {
	collector TaskCollector
}

func NewTaskCollectTool(collector TaskCollector) *TaskCollectTool {
	return &TaskCollectTool{collector: collector}
}

func (TaskCollectTool) Name() string { return NameTaskCollect }

func (TaskCollectTool) Description() string {
	return "Read or wait for either a direct-owned task set or an existing durable task group. Supply exactly one of task_ids or group_id. Direct task IDs are pinned to their current attempts when the call begins; a group uses its immutable persisted attempts."
}

func (TaskCollectTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_ids": map[string]any{
				"type":        "array",
				"description": "Stable task IDs to collect. All tasks must be directly owned by the MainAgent.",
				"items":       map[string]any{"type": "string"},
				"minItems":    1,
				"maxItems":    maxTaskCollectMembers,
			},
			"group_id": map[string]any{"type": "string", "description": "Durable task group ID to collect instead of task_ids."},
			"wait": map[string]any{
				"type":        "boolean",
				"description": "Wait until all pinned attempts settle. Defaults to false.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Wait timeout in milliseconds. With wait=true, zero or omitted uses five minutes; maximum is ten minutes.",
				"minimum":     0,
				"maximum":     maxTaskCollectTimeout.Milliseconds(),
			},
		},
		"additionalProperties": false,
	}
}

func (TaskCollectTool) IsReadOnly() bool { return true }

func (TaskCollectTool) ConcurrencyPolicy(json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{Resource: "coordination:tasks", Mode: ConcurrencyModeRead}
}

func (TaskCollectTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (t *TaskCollectTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		TaskIDs   []string `json:"task_ids"`
		GroupID   string   `json:"group_id"`
		Wait      bool     `json:"wait"`
		TimeoutMS int64    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if t.collector == nil {
		return "", fmt.Errorf("task collection is unavailable")
	}
	taskIDs, err := DedupeTaskIDs(args.TaskIDs)
	if err != nil {
		return "", err
	}
	groupID := strings.TrimSpace(args.GroupID)
	if (len(taskIDs) == 0) == (groupID == "") {
		return "", fmt.Errorf("exactly one of task_ids or group_id is required")
	}
	if len(taskIDs) > maxTaskCollectMembers {
		return "", fmt.Errorf("task_ids exceeds maximum member count %d", maxTaskCollectMembers)
	}
	if args.TimeoutMS < 0 || args.TimeoutMS > maxTaskCollectTimeout.Milliseconds() {
		return "", fmt.Errorf("timeout_ms must be between 0 and %d", maxTaskCollectTimeout.Milliseconds())
	}
	timeout := time.Duration(args.TimeoutMS) * time.Millisecond
	if args.Wait && timeout == 0 {
		timeout = defaultCollectTimeout
	}
	result, err := t.collector.CollectTasks(ctx, TaskCollectRequest{TaskIDs: taskIDs, GroupID: groupID, Wait: args.Wait, Timeout: timeout})
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal task collection: %w", err)
	}
	return string(out), nil
}
