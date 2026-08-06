package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/tools"
)

type pinnedTaskAttempt struct {
	TaskID  string
	Attempt uint64
	Record  *DurableTaskRecord
}

func (a *MainAgent) pinMainOwnedTaskAttempts(taskIDs []string) ([]pinnedTaskAttempt, uint64, uint64, <-chan struct{}, error) {
	sorted := append([]string(nil), taskIDs...)
	sort.Strings(sorted)
	missing := make([]string, 0, len(sorted))
	for _, taskID := range sorted {
		a.subs.mu.RLock()
		rec := cloneDurableTaskRecord(a.subs.taskRecords[taskID])
		a.subs.mu.RUnlock()
		if rec == nil {
			missing = append(missing, taskID)
		}
	}
	archived, err := loadArchivedTaskRecordsByTaskIDs(a.sessionDir, missing)
	if err != nil {
		return nil, 0, 0, nil, fmt.Errorf("load archived tasks: %w", err)
	}

	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	pins := make([]pinnedTaskAttempt, 0, len(sorted))
	for _, taskID := range sorted {
		rec := cloneDurableTaskRecord(a.subs.taskRecords[taskID])
		if rec == nil {
			rec = cloneDurableTaskRecord(archived[taskID])
		}
		if rec == nil {
			return nil, 0, 0, nil, fmt.Errorf("unknown task_id %q", taskID)
		}
		if strings.TrimSpace(rec.OwnerTaskID) != "" || strings.TrimSpace(rec.OwnerAgentID) != "" {
			return nil, 0, 0, nil, fmt.Errorf("task %s is not directly owned by the MainAgent", taskID)
		}
		if rec.Attempt == 0 {
			rec.Attempt = 1
		}
		pins = append(pins, pinnedTaskAttempt{TaskID: taskID, Attempt: rec.Attempt, Record: rec})
	}
	return pins, a.subs.taskRevision, a.subs.sessionEpoch, a.subs.taskChanged, nil
}

func (a *MainAgent) collectPinnedTaskSnapshot(pins []pinnedTaskAttempt) (tools.TaskCollectResult, uint64, uint64, <-chan struct{}) {
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	result := tools.TaskCollectResult{
		AllSettled:       true,
		AllDurable:       true,
		SnapshotRevision: a.subs.taskRevision,
		Tasks:            make([]tools.TaskCollectItem, 0, len(pins)),
	}
	for _, pin := range pins {
		current := cloneDurableTaskRecord(a.subs.taskRecords[pin.TaskID])
		if current == nil {
			current = cloneDurableTaskRecord(pin.Record)
		}
		settlement := cloneTaskSettlement(a.subs.settlements[taskAttemptKey{TaskID: pin.TaskID, Attempt: pin.Attempt}])
		if settlement == nil && current != nil && current.LatestSettlement != nil && current.LatestSettlement.Attempt == pin.Attempt {
			settlement = cloneTaskSettlement(current.LatestSettlement)
		}
		item := tools.TaskCollectItem{TaskID: pin.TaskID, MemberAttempt: pin.Attempt}
		if current != nil {
			item.CurrentAttempt = current.Attempt
			item.LifecycleRevision = current.LifecycleRevision
			item.State = current.State
			item.Summary = current.LastSummary
			item.ArtifactRefs = append([]tools.ArtifactRef(nil), current.LastArtifactRefs...)
			item.UpdatedAt = current.UpdatedAt
			item.TranscriptPersistence = string(current.Persistence.State)
		}
		if settlement != nil {
			item.Settled = true
			item.Outcome = settlement.Outcome
			item.State = settlement.Outcome
			item.Summary = settlement.Summary
			if settlement.Completion != nil {
				item.ResultType = settlement.Completion.ResultType
			}
			item.ArtifactRefs = append([]tools.ArtifactRef(nil), settlement.ArtifactRefs...)
			if settlement.ResultRef != nil {
				ref := *settlement.ResultRef
				item.ResultRef = &ref
			}
			item.SettledAt = settlement.SettledAt
			if current != nil && current.Attempt == pin.Attempt && current.LatestSettlement != nil && current.LatestSettlement.Attempt == pin.Attempt {
				item.SettlementDurable = current.SettlementDurable
			} else {
				item.SettlementDurable = true
			}
		}
		if !item.Settled {
			result.AllSettled = false
		}
		if !item.Settled || !item.SettlementDurable {
			result.AllDurable = false
		}
		result.Tasks = append(result.Tasks, item)
	}
	return result, a.subs.taskRevision, a.subs.sessionEpoch, a.subs.taskChanged
}

func (a *MainAgent) pinsForTaskGroup(groupID string) ([]pinnedTaskAttempt, uint64, error) {
	groupID = strings.TrimSpace(groupID)
	a.subs.mu.RLock()
	group := cloneDurableTaskGroup(a.subs.taskGroups[groupID])
	sessionEpoch := a.subs.sessionEpoch
	a.subs.mu.RUnlock()
	if group == nil {
		return nil, 0, fmt.Errorf("unknown group_id %q", groupID)
	}
	if group.OwnerPrincipal != mainGroupOwner {
		return nil, 0, fmt.Errorf("task group %s is not owned by the MainAgent", groupID)
	}
	taskIDs := make([]string, len(group.Members))
	for i, member := range group.Members {
		taskIDs[i] = member.TaskID
	}
	archived, err := loadArchivedTaskRecordsByTaskIDs(a.sessionDir, taskIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("load archived task group members: %w", err)
	}
	pins := make([]pinnedTaskAttempt, 0, len(group.Members))
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	if a.subs.sessionEpoch != sessionEpoch {
		return nil, 0, fmt.Errorf("task group collection invalidated by session change")
	}
	for _, member := range group.Members {
		rec := cloneDurableTaskRecord(a.subs.taskRecords[member.TaskID])
		if rec == nil {
			rec = cloneDurableTaskRecord(archived[member.TaskID])
		}
		pins = append(pins, pinnedTaskAttempt{TaskID: member.TaskID, Attempt: member.Attempt, Record: rec})
	}
	return pins, sessionEpoch, nil
}

func (a *MainAgent) CollectTasks(ctx context.Context, request tools.TaskCollectRequest) (tools.TaskCollectResult, error) {
	if a == nil {
		return tools.TaskCollectResult{}, fmt.Errorf("task collection is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if (len(request.TaskIDs) == 0) == (strings.TrimSpace(request.GroupID) == "") {
		return tools.TaskCollectResult{}, fmt.Errorf("exactly one of task_ids or group_id is required")
	}
	if request.Wait && request.Timeout <= 0 {
		return tools.TaskCollectResult{}, fmt.Errorf("wait timeout must be positive")
	}
	parentCtx := a.parentCtx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	var pins []pinnedTaskAttempt
	var sessionEpoch uint64
	var err error
	if request.GroupID != "" {
		pins, sessionEpoch, err = a.pinsForTaskGroup(request.GroupID)
	} else {
		pins, _, sessionEpoch, _, err = a.pinMainOwnedTaskAttempts(request.TaskIDs)
	}
	if err != nil {
		return tools.TaskCollectResult{}, err
	}
	var timer *time.Timer
	var timeout <-chan time.Time
	if request.Wait && request.Timeout > 0 {
		timer = time.NewTimer(request.Timeout)
		defer timer.Stop()
		timeout = timer.C
	}
	for {
		result, _, currentEpoch, changed := a.collectPinnedTaskSnapshot(pins)
		result.GroupID = strings.TrimSpace(request.GroupID)
		if currentEpoch != sessionEpoch {
			return tools.TaskCollectResult{}, fmt.Errorf("task collection invalidated by session change")
		}
		if !request.Wait || result.AllSettled {
			return result, nil
		}
		select {
		case <-changed:
			continue
		case <-ctx.Done():
			return tools.TaskCollectResult{}, ctx.Err()
		case <-parentCtx.Done():
			return tools.TaskCollectResult{}, parentCtx.Err()
		case <-timeout:
			result, _, currentEpoch, _ = a.collectPinnedTaskSnapshot(pins)
			result.GroupID = strings.TrimSpace(request.GroupID)
			if currentEpoch != sessionEpoch {
				return tools.TaskCollectResult{}, fmt.Errorf("task collection invalidated by session change")
			}
			if result.AllSettled {
				return result, nil
			}
			result.TimedOut = true
			return result, nil
		}
	}
}
