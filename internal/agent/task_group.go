package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/chord/internal/tools"
)

const (
	taskGroupSchemaVersion = 1
	maxRetainedTaskGroups  = 128
	maxTaskGroupMembers    = 64
	maxTaskGroupLabelBytes = 256
	maxTaskGroupKeyBytes   = 256
	mainGroupOwner         = "session-root"
)

type DurableTaskGroup struct {
	Version        int                     `json:"version"`
	GroupID        string                  `json:"group_id"`
	OwnerPrincipal string                  `json:"owner_principal"`
	Label          string                  `json:"label,omitempty"`
	SemanticKey    string                  `json:"semantic_key,omitempty"`
	Members        []tools.TaskGroupMember `json:"members"`
	CreatedAt      time.Time               `json:"created_at"`
}

func cloneDurableTaskGroup(in *DurableTaskGroup) *DurableTaskGroup {
	if in == nil {
		return nil
	}
	out := *in
	out.GroupID = strings.TrimSpace(out.GroupID)
	out.OwnerPrincipal = strings.TrimSpace(out.OwnerPrincipal)
	out.Label = strings.TrimSpace(out.Label)
	out.SemanticKey = strings.TrimSpace(out.SemanticKey)
	out.Members = append([]tools.TaskGroupMember(nil), out.Members...)
	return &out
}

func taskGroupsPath(sessionDir string) string {
	if strings.TrimSpace(sessionDir) == "" {
		return ""
	}
	return filepath.Join(sessionDir, "subagents", "task-groups.json")
}

func loadTaskGroups(sessionDir string) (map[string]*DurableTaskGroup, error) {
	path := taskGroupsPath(sessionDir)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var groups []*DurableTaskGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil, err
	}
	out := make(map[string]*DurableTaskGroup, len(groups))
	semanticKeys := make(map[string]string)
	for _, group := range groups {
		group = cloneDurableTaskGroup(group)
		if group == nil || group.Version != taskGroupSchemaVersion || group.GroupID == "" || group.OwnerPrincipal != mainGroupOwner || len(group.Members) == 0 || len(group.Members) > maxTaskGroupMembers || len(group.Label) > maxTaskGroupLabelBytes || len(group.SemanticKey) > maxTaskGroupKeyBytes {
			return nil, fmt.Errorf("invalid durable task group")
		}
		for i, member := range group.Members {
			if strings.TrimSpace(member.TaskID) == "" || member.Attempt == 0 || (i > 0 && group.Members[i-1].TaskID >= member.TaskID) {
				return nil, fmt.Errorf("invalid durable task group %s members", group.GroupID)
			}
		}
		if _, exists := out[group.GroupID]; exists {
			return nil, fmt.Errorf("duplicate durable task group %s", group.GroupID)
		}
		if group.SemanticKey != "" {
			key := group.OwnerPrincipal + "\x00" + group.SemanticKey
			if prior := semanticKeys[key]; prior != "" {
				return nil, fmt.Errorf("duplicate task group semantic key %q", group.SemanticKey)
			}
			semanticKeys[key] = group.GroupID
		}
		out[group.GroupID] = group
	}
	return out, nil
}

func persistTaskGroups(sessionDir string, groups map[string]*DurableTaskGroup) error {
	path := taskGroupsPath(sessionDir)
	if path == "" {
		return nil
	}
	ordered := make([]*DurableTaskGroup, 0, len(groups))
	for _, group := range groups {
		if group = cloneDurableTaskGroup(group); group != nil {
			ordered = append(ordered, group)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].GroupID < ordered[j].GroupID })
	return persistJSONAtomically(sessionDir, path, "task-groups", ordered)
}

func taskGroupMembersEqual(a, b []tools.TaskGroupMember) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func taskGroupResult(group *DurableTaskGroup) tools.TaskGroupCreateResult {
	return tools.TaskGroupCreateResult{GroupID: group.GroupID, Members: append([]tools.TaskGroupMember(nil), group.Members...), JoinPolicy: "all_settled"}
}

func (a *MainAgent) CreateTaskGroup(ctx context.Context, request tools.TaskGroupCreateRequest) (tools.TaskGroupCreateResult, error) {
	if a == nil {
		return tools.TaskGroupCreateResult{}, fmt.Errorf("task group creation is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if a.shuttingDown.Load() || a.admissionPaused.Load() {
		return tools.TaskGroupCreateResult{}, fmt.Errorf("cannot create task group during shutdown or session transition")
	}
	admissionEpoch := a.admissionEpoch.Load()
	if err := ctx.Err(); err != nil {
		return tools.TaskGroupCreateResult{}, err
	}
	taskIDs, err := tools.DedupeTaskIDs(request.TaskIDs)
	if err != nil {
		return tools.TaskGroupCreateResult{}, err
	}
	if len(taskIDs) == 0 {
		return tools.TaskGroupCreateResult{}, fmt.Errorf("task_ids is required")
	}
	if len(taskIDs) > maxTaskGroupMembers {
		return tools.TaskGroupCreateResult{}, fmt.Errorf("task_ids exceeds maximum member count %d", maxTaskGroupMembers)
	}
	label := strings.TrimSpace(request.Label)
	semanticKey := strings.TrimSpace(request.SemanticKey)
	if len(label) > maxTaskGroupLabelBytes {
		return tools.TaskGroupCreateResult{}, fmt.Errorf("label exceeds maximum size %d bytes", maxTaskGroupLabelBytes)
	}
	if len(semanticKey) > maxTaskGroupKeyBytes {
		return tools.TaskGroupCreateResult{}, fmt.Errorf("semantic_key exceeds maximum size %d bytes", maxTaskGroupKeyBytes)
	}
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	if a.shuttingDown.Load() || a.admissionPaused.Load() || a.admissionEpoch.Load() != admissionEpoch {
		return tools.TaskGroupCreateResult{}, fmt.Errorf("task group creation invalidated by session or lifecycle change")
	}
	if err := ctx.Err(); err != nil {
		return tools.TaskGroupCreateResult{}, err
	}
	a.taskGroupPersistMu.Lock()
	defer a.taskGroupPersistMu.Unlock()

	pins, _, sessionEpoch, _, err := a.pinMainOwnedTaskAttempts(taskIDs)
	if err != nil {
		return tools.TaskGroupCreateResult{}, err
	}
	members := make([]tools.TaskGroupMember, len(pins))
	for i, pin := range pins {
		members[i] = tools.TaskGroupMember{TaskID: pin.TaskID, Attempt: pin.Attempt}
	}
	a.subs.mu.RLock()
	if a.subs.sessionEpoch != sessionEpoch {
		a.subs.mu.RUnlock()
		return tools.TaskGroupCreateResult{}, fmt.Errorf("task group creation invalidated by session change")
	}
	for _, existing := range a.subs.taskGroups {
		if existing.OwnerPrincipal != mainGroupOwner || existing.SemanticKey == "" || existing.SemanticKey != semanticKey {
			continue
		}
		if !taskGroupMembersEqual(existing.Members, members) || existing.Label != label {
			a.subs.mu.RUnlock()
			return tools.TaskGroupCreateResult{}, fmt.Errorf("semantic_key %q already identifies group %s with different immutable content", semanticKey, existing.GroupID)
		}
		result := taskGroupResult(existing)
		a.subs.mu.RUnlock()
		return result, nil
	}
	if len(a.subs.taskGroups) >= maxRetainedTaskGroups {
		a.subs.mu.RUnlock()
		return tools.TaskGroupCreateResult{}, fmt.Errorf("durable task group limit reached (%d); automatic group deletion is disabled", maxRetainedTaskGroups)
	}
	groups := make(map[string]*DurableTaskGroup, len(a.subs.taskGroups)+1)
	for id, group := range a.subs.taskGroups {
		groups[id] = cloneDurableTaskGroup(group)
	}
	a.subs.mu.RUnlock()

	groupID := fmt.Sprintf("group-%d", a.taskGroupSeq.Add(1))
	group := &DurableTaskGroup{Version: taskGroupSchemaVersion, GroupID: groupID, OwnerPrincipal: mainGroupOwner, Label: label, SemanticKey: semanticKey, Members: members, CreatedAt: time.Now()}
	groups[groupID] = group
	if err := ctx.Err(); err != nil {
		return tools.TaskGroupCreateResult{}, err
	}
	if err := persistTaskGroups(a.sessionDir, groups); err != nil {
		return tools.TaskGroupCreateResult{}, fmt.Errorf("persist task group: %w", err)
	}
	a.subs.mu.Lock()
	if a.subs.sessionEpoch != sessionEpoch {
		a.subs.mu.Unlock()
		return tools.TaskGroupCreateResult{}, fmt.Errorf("task group creation invalidated by session change")
	}
	a.subs.taskGroups[groupID] = cloneDurableTaskGroup(group)
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()
	return taskGroupResult(group), nil
}

func nextTaskGroupSeq(groups map[string]*DurableTaskGroup) uint64 {
	var maximum uint64
	for groupID := range groups {
		if !strings.HasPrefix(groupID, "group-") {
			continue
		}
		if value, err := strconv.ParseUint(strings.TrimPrefix(groupID, "group-"), 10, 64); err == nil && value > maximum {
			maximum = value
		}
	}
	return maximum
}

func (a *MainAgent) resetTaskGroups(groups map[string]*DurableTaskGroup) {
	a.subs.mu.Lock()
	a.subs.taskGroups = make(map[string]*DurableTaskGroup, len(groups))
	for id, group := range groups {
		a.subs.taskGroups[id] = cloneDurableTaskGroup(group)
	}
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()
	a.taskGroupSeq.Store(nextTaskGroupSeq(groups))
}

// guardDegradedTaskGroupSeq raises the group ID sequence to a wall-clock floor
// after a soft-degraded restore dropped the task-groups file. The corrupt file
// may have named groups the restored transcript still references; restarting
// the sequence at group-1 would alias those references onto unrelated new
// groups, silently resolving collects against the wrong member set.
func (a *MainAgent) guardDegradedTaskGroupSeq(degraded bool) {
	if a == nil || !degraded {
		return
	}
	if floor := uint64(time.Now().Unix()); a.taskGroupSeq.Load() < floor {
		a.taskGroupSeq.Store(floor)
	}
}
