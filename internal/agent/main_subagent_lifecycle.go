package agent

import (
	"os"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/tools"
)

const waitingMainExpiryUserTurns = uint64(5)

func (a *MainAgent) noteSubAgentStateTransition(sub *SubAgent, state SubAgentState) {
	if a == nil || sub == nil {
		return
	}
	a.subs.noteStateEnteredTurn(sub.instanceID, state, a.explicitUserTurnCount.Load())
}

func (a *MainAgent) closeSubAgent(agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	sub := a.subs.remove(agentID)
	if sub == nil {
		return
	}
	if focused := a.focusedAgent.Load(); focused != nil && focused.instanceID == agentID {
		a.focusedAgent.Store(nil)
		a.setFocusedTaskID("")
	}
	if rec := a.focusedDurableTask(); rec != nil && rec.LatestInstanceID == agentID {
		a.setFocusedTaskID("")
	}
	a.releaseSubAgentSlot(sub)
	a.fileTrack.ReleaseAll(agentID)
	tools.StopAllSpawnedForAgent(agentID, "terminated on subagent close")
	sub.cancel()
	sub.closeLLMClient()
	a.removeSubAgentMailboxState(agentID)
	_ = os.Remove(subAgentMetaPath(a.sessionDir, agentID))
}

// parkSubAgent releases a quiescent worker's hot runtime while preserving its
// durable task identity, transcript, and mailbox state. A later rehydration
// receives a new runtime instance ID for the same task ID.
func (a *MainAgent) parkSubAgent(agentID string) bool {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return false
	}
	sub := a.subAgentByID(agentID)
	if sub == nil {
		return false
	}
	sub.lifecycleMu.Lock()
	defer sub.lifecycleMu.Unlock()
	if !sub.canPark() {
		return false
	}
	focused := a.focusedAgent.Load() == sub
	a.flushPersistUntil(func() {
		if hook := a.subAgentParkBarrierHook; hook != nil {
			hook(sub)
		}
	})
	if err := sub.checkpointTranscript(); err != nil {
		log.Warnf("refusing to park SubAgent because transcript checkpoint failed agent_id=%v task_id=%v error=%v", agentID, sub.taskID, err)
		return false
	}
	if !sub.transcriptPersistenceHealthy() {
		log.Warnf("refusing to park SubAgent because pending transcript persistence failed agent_id=%v task_id=%v", agentID, sub.taskID)
		return false
	}
	if sub.hasPendingUserInput() {
		switch sub.State() {
		case SubAgentStateIdle, SubAgentStateWaitingMain, SubAgentStateWaitingDescendant:
			if err := a.acquireWakeReactivationSlot(sub); err == nil {
				a.markSubAgentReactivated(sub, "Queued input arrived before parking")
				sub.armStartupWatchdog()
			}
		}
		return false
	}
	if !sub.canPark() {
		return false
	}
	parkedAt := time.Now()
	a.subs.mu.Lock()
	if current := a.subs.subAgents[agentID]; current != sub || !sub.canPark() {
		a.subs.mu.Unlock()
		return false
	}
	previousRecord := cloneDurableTaskRecord(a.subs.taskRecords[sub.taskID])
	parkedRecord := buildTaskRecordFromSub(sub, previousRecord, "", a.explicitUserTurnCount.Load(), parkedAt)
	parkedRecord.RuntimeParked = true
	a.subs.mu.Unlock()
	if err := a.persistSubAgentMeta(sub); err != nil {
		log.Warnf("refusing to park SubAgent because metadata persistence failed agent_id=%v task_id=%v error=%v", agentID, sub.taskID, err)
		return false
	}
	if err := a.persistTaskRegistryRecord(a.sessionDir, sub.taskID, parkedRecord); err != nil {
		log.Warnf("refusing to park SubAgent because task registry persistence failed agent_id=%v task_id=%v error=%v", agentID, sub.taskID, err)
		return false
	}
	a.subs.mu.Lock()
	if current := a.subs.subAgents[agentID]; current != sub || !sub.canPark() {
		a.subs.mu.Unlock()
		return false
	}
	removed := a.subs.removeLocked(agentID)
	a.subs.taskRecords[sub.taskID] = cloneDurableTaskRecord(parkedRecord)
	a.subs.mu.Unlock()
	if a.recovery != nil {
		if err := a.recovery.SaveSnapshot(a.buildRecoverySnapshot()); err != nil {
			a.subs.mu.Lock()
			if removed == sub {
				a.subs.subAgents[agentID] = sub
			}
			if previousRecord != nil {
				a.subs.taskRecords[sub.taskID] = cloneDurableTaskRecord(previousRecord)
			}
			a.subs.stateEnteredTurn[agentID] = a.explicitUserTurnCount.Load()
			a.subs.mu.Unlock()
			_ = a.persistTaskRegistryRecord(a.sessionDir, sub.taskID, previousRecord)
			log.Warnf("refusing to park SubAgent because recovery snapshot persistence failed agent_id=%v task_id=%v error=%v", agentID, sub.taskID, err)
			return false
		}
	}
	if removed != sub {
		if removed != nil {
			a.subs.add(removed)
		}
		return false
	}
	a.releaseSubAgentSlot(sub)
	a.fileTrack.ReleaseAll(agentID)
	tools.StopAllSpawnedForAgent(agentID, "terminated on subagent park")
	sub.cancel()
	sub.closeLLMClient()
	if focused {
		a.focusedAgent.CompareAndSwap(sub, nil)
		a.setFocusedTaskID(sub.taskID)
	}

	a.orchestrationMetrics.recordPark(sub.taskID, parkedAt)
	log.Debugf("parked quiescent subagent agent_id=%v task_id=%v state=%v", agentID, sub.taskID, sub.State())
	return true
}

func (a *MainAgent) parkQuiescentSubAgents() int {
	parked := 0
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub != nil && a.parkSubAgent(sub.instanceID) {
			parked++
		}
	}
	return parked
}

func (a *MainAgent) removeSubAgentMailboxState(agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	delete(a.subAgentInbox.progress, agentID)
	filter := func(in []SubAgentMailboxMessage) []SubAgentMailboxMessage {
		if len(in) == 0 {
			return nil
		}
		out := in[:0]
		for _, msg := range in {
			if strings.TrimSpace(msg.AgentID) == agentID {
				a.releaseMailboxMemory(msg)
				continue
			}
			out = append(out, msg)
		}
		return out
	}
	a.subAgentInbox.urgent = filter(a.subAgentInbox.urgent)
	a.subAgentInbox.normal = filter(a.subAgentInbox.normal)
	if len(a.pendingSubAgentMailboxes) > 0 {
		filtered := a.pendingSubAgentMailboxes[:0]
		for _, msg := range a.pendingSubAgentMailboxes {
			if msg == nil || strings.TrimSpace(msg.AgentID) == agentID {
				continue
			}
			filtered = append(filtered, msg)
		}
		a.pendingSubAgentMailboxes = filtered
	}
	if len(a.activeSubAgentMailboxes) > 0 {
		filtered := a.activeSubAgentMailboxes[:0]
		for _, msg := range a.activeSubAgentMailboxes {
			if msg == nil || strings.TrimSpace(msg.AgentID) == agentID {
				continue
			}
			filtered = append(filtered, msg)
		}
		a.activeSubAgentMailboxes = filtered
	}
	if len(a.ownedSubAgentMailboxes) > 0 {
		for _, msg := range a.ownedSubAgentMailboxes[agentID] {
			a.releaseMailboxMemory(msg)
		}
		delete(a.ownedSubAgentMailboxes, agentID)
		for ownerID, queued := range a.ownedSubAgentMailboxes {
			filtered := queued[:0]
			for _, msg := range queued {
				if strings.TrimSpace(msg.AgentID) == agentID || strings.TrimSpace(msg.OwnerAgentID) == agentID {
					a.releaseMailboxMemory(msg)
					continue
				}
				filtered = append(filtered, msg)
			}
			if len(filtered) == 0 {
				delete(a.ownedSubAgentMailboxes, ownerID)
				continue
			}
			a.ownedSubAgentMailboxes[ownerID] = filtered
		}
	}
	delete(a.ownedMailboxSpool, agentID)
	if a.activeSubAgentMailbox != nil && strings.TrimSpace(a.activeSubAgentMailbox.AgentID) == agentID {
		a.activeSubAgentMailbox = nil
	}
	if len(a.activeSubAgentMailboxes) > 0 {
		a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
	} else if a.activeSubAgentMailbox == nil {
		a.activeSubAgentMailboxAck = false
	}
	a.refreshSubAgentInboxSummary()
}

func (a *MainAgent) sweepSubAgentLifecycle() {
	currentTurn := a.explicitUserTurnCount.Load()
	changed := false
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub == nil {
			continue
		}
		state := sub.State()
		enteredTurn := a.subs.stateEnteredTurnFor(sub.instanceID)
		switch state {
		case SubAgentStateWaitingMain:
			if currentTurn >= enteredTurn+waitingMainExpiryUserTurns {
				a.handleSubAgentCloseRequestedEvent(Event{
					Type:     EventSubAgentCloseRequested,
					SourceID: sub.instanceID,
					Payload: &SubAgentCloseRequestedPayload{
						Reason:       "expired waiting for main reply",
						ClosedReason: "expired waiting for main reply",
						FinalState:   SubAgentStateCancelled,
					},
				})
				changed = true
			}
		case SubAgentStateWaitingDescendant:
			// Descendant waits are durable coordination state; do not expire via
			// user-turn GC. Recovery or explicit control actions decide what to do.
		}
	}
	a.subs.mu.Lock()
	for _, rec := range a.subs.taskRecords {
		if rec == nil || !rec.RuntimeParked || SubAgentState(rec.State) != SubAgentStateWaitingMain {
			continue
		}
		if currentTurn >= rec.LastUpdatedTurn+waitingMainExpiryUserTurns {
			rec.State = string(SubAgentStateCancelled)
			rec.ResumePolicy = taskResumePolicyExplicitOnly
			rec.LastSummary = "expired waiting for main reply"
			rec.ClosedReason = rec.LastSummary
			rec.UpdatedAt = time.Now()
			changed = true
		}
	}
	a.subs.mu.Unlock()
	if changed {
		a.persistTaskRegistry()
		a.saveRecoverySnapshot()
	}
}
