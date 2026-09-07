package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

// subAgentLifecycleSweepInterval is the coarse wall-clock cadence of the
// WaitingMain expiry sweep and the Running-worker stall watchdog. Waiting waits
// are bounded below by the min-wait guard (minutes at the default), and a
// stalled worker only matters minutes after its heartbeat goes quiet, so a
// per-minute check keeps reclaim/notify latency negligible without polling the
// loop any faster. The trigger event is only queued while a waiting_main or
// live-running worker exists.
const subAgentLifecycleSweepInterval = time.Minute

// EventSubAgentLifecycleSweep is the loop event the periodic wall-clock trigger
// dispatches to re-run the WaitingMain expiry sweep without any user message.
// Kept next to the sweeper because it is lifecycle-internal; dispatch lives in
// main_loop.go.
const EventSubAgentLifecycleSweep = "subagent_lifecycle_sweep"

// waitingMainExpiryPolicy is the resolved two-clock policy for abandoning a
// worker that parked waiting for its owner's reply. See the
// DefaultWaitingMain* constants for why one clock is not enough.
type waitingMainExpiryPolicy struct {
	turns   uint64
	minWait time.Duration
	maxWait time.Duration
}

func resolveWaitingMainExpiryPolicy(cfg config.OrchestrationConfig) waitingMainExpiryPolicy {
	return waitingMainExpiryPolicy{
		turns:   cfg.EffectiveWaitingMainExpiryTurns(),
		minWait: cfg.EffectiveWaitingMainMinWait(),
		maxWait: cfg.EffectiveWaitingMainMaxWait(),
	}
}

// waitingMainExpiryPolicy returns the resolved policy. It is resolved once at
// construction; a zero turns budget means this agent was built without going
// through the constructor (tests), so fall back to resolving it on demand
// rather than treating every wait as instantly expired.
func (a *MainAgent) waitingMainExpiryPolicy() waitingMainExpiryPolicy {
	if a.waitingMainExpiry.turns > 0 {
		return a.waitingMainExpiry
	}
	return resolveWaitingMainExpiryPolicy(effectiveOrchestrationConfig(a.globalConfig, a.projectConfig))
}

// expired reports whether a wait that started at (enteredTurn, since) is over.
// A zero since means the entry time was never recorded, in which case only the
// turn budget applies so the wait can still be collected.
func (p waitingMainExpiryPolicy) expired(currentTurn, enteredTurn uint64, since, now time.Time) bool {
	turnBudgetSpent := currentTurn >= enteredTurn+p.turns
	if since.IsZero() {
		return turnBudgetSpent
	}
	waited := now.Sub(since)
	if turnBudgetSpent && waited >= p.minWait {
		return true
	}
	return waited >= p.maxWait
}

// reason describes which clock fired, so the cancellation summary the owner and
// the user see is not just "expired".
func (p waitingMainExpiryPolicy) reason(currentTurn, enteredTurn uint64, since, now time.Time) string {
	if since.IsZero() || currentTurn >= enteredTurn+p.turns {
		return fmt.Sprintf("expired waiting for main reply (no reply within %d user turns)", p.turns)
	}
	return fmt.Sprintf("expired waiting for main reply (no reply within %s)", now.Sub(since).Round(time.Minute))
}

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
	if a.recoveryManager() != nil {
		if err := a.persistSnapshotLocked(a.buildRecoverySnapshot); err != nil {
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
	policy := a.waitingMainExpiryPolicy()
	now := time.Now()
	changed := false
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub == nil {
			continue
		}
		state := sub.State()
		enteredTurn := a.subs.stateEnteredTurnFor(sub.instanceID)
		switch state {
		case SubAgentStateWaitingMain:
			if policy.expired(currentTurn, enteredTurn, sub.StateChangedAt(), now) {
				reason := policy.reason(currentTurn, enteredTurn, sub.StateChangedAt(), now)
				// An expiry is an owner-visible failure of the wait contract:
				// alert the owner through a risk_alert mailbox and settle the
				// still-pending escalation request as expired instead of
				// cancelling silently.
				a.queueWaitingMainExpiryAlert(sub, nil, reason)
				a.handleSubAgentCloseRequestedEvent(Event{
					Type:     EventSubAgentCloseRequested,
					SourceID: sub.instanceID,
					Payload: &SubAgentCloseRequestedPayload{
						Reason:       reason,
						ClosedReason: reason,
						FinalState:   SubAgentStateCancelled,
					},
				})
				a.expireAgentRequestsAfterCancellation(sub.taskID)
				changed = true
			}
		case SubAgentStateWaitingDescendant:
			// Descendant waits are durable coordination state; do not expire via
			// user-turn GC. Recovery or explicit control actions decide what to do.
		}
	}
	var expiredTaskIDs []string
	a.subs.mu.RLock()
	for taskID, rec := range a.subs.taskRecords {
		if rec == nil || !rec.RuntimeParked || SubAgentState(rec.State) != SubAgentStateWaitingMain {
			continue
		}
		if policy.expired(currentTurn, rec.LastUpdatedTurn, rec.UpdatedAt, now) {
			expiredTaskIDs = append(expiredTaskIDs, taskID)
		}
	}
	a.subs.mu.RUnlock()
	// A task collected above can be revived (rehydrated for a direct reply)
	// before its settle runs, without changing its attempt. The guard re-runs
	// the expiry predicate against the live record inside the settlement
	// journal lock so the sweep backs off instead of cancelling the revived
	// attempt.
	stillExpiredParkedWaiting := func(rec *DurableTaskRecord) bool {
		return rec != nil && rec.RuntimeParked &&
			SubAgentState(rec.State) == SubAgentStateWaitingMain &&
			policy.expired(currentTurn, rec.LastUpdatedTurn, rec.UpdatedAt, now)
	}
	for _, taskID := range expiredTaskIDs {
		reason := "expired waiting for main reply"
		if rec := a.taskRecordByTaskID(taskID); rec != nil {
			reason = policy.reason(currentTurn, rec.LastUpdatedTurn, rec.UpdatedAt, now)
		}
		outcome := a.settleDetachedTerminalTaskGuarded(taskID, SubAgentStateCancelled, reason, reason, stillExpiredParkedWaiting)
		if outcome != SubAgentStateCancelled {
			continue
		}
		// The guarded settle won: the parked wait really expired. Surface the
		// cancellation to the owner and move the pending escalation request to
		// expired instead of leaving both the mailbox and the ledger silent.
		if settled := a.taskRecordByTaskID(taskID); settled != nil {
			a.queueWaitingMainExpiryAlert(nil, settled, reason)
		}
		a.expireAgentRequestsAfterCancellation(taskID)
		changed = true
	}
	if changed {
		// Both settle paths (commitTerminalTask via the close-requested handler
		// and settleDetachedTerminalTaskGuarded) already persisted the task
		// registry per task; only the recovery snapshot still needs to observe
		// the batch.
		a.saveRecoverySnapshot()
	}
	// Running-worker stall watchdog. A Running worker whose activity heartbeat
	// has gone quiet for the coordination stall threshold is not progressing
	// and has no in-flight request to self-heal (the sub-side silence watchdog
	// bounds request windows well below this threshold). Notify the owner once
	// per stall episode through a risk_alert mailbox instead of killing the
	// worker; the flag is cleared again on the next sweep that finds the
	// worker healthy.
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub == nil || sub.State() != SubAgentStateRunning {
			continue
		}
		reason := runningSubAgentStallReason(sub, now)
		if reason == "" {
			sub.clearStallAlert()
			continue
		}
		if sub.stallAlertRaised() {
			continue
		}
		sub.raiseStallAlert()
		a.queueSubAgentStallAlert(sub, reason)
	}
}

// startSubAgentLifecycleSweep runs the periodic trigger that keeps WaitingMain
// expiry and the Running-worker stall watchdog independent of user speech.
// Without it the sweep only runs inside handleUserMessage, so a headless or
// silent session would never reclaim a parked worker or surface a stalled
// worker. The trigger deliberately avoids the idle/nudge chain so it survives
// the removal of that dead code; it wakes the loop at most once per interval
// and only while a waiting_main or live-running candidate exists.
func (a *MainAgent) startSubAgentLifecycleSweep(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(subAgentLifecycleSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-a.stoppingCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if a.admissionPaused.Load() || !a.hasSubAgentLifecycleSweepCandidates() {
					continue
				}
				a.sendEvent(Event{Type: EventSubAgentLifecycleSweep})
			}
		}
	}()
}

// hasSubAgentLifecycleSweepCandidates reports whether the periodic sweep could
// find something to act on: a waiting_main expiry candidate, or a live Running
// worker whose stall/health the sweep must keep watching. The periodic trigger
// gates on it so sessions that never delegate do not wake the event loop.
func (a *MainAgent) hasSubAgentLifecycleSweepCandidates() bool {
	if a.hasWaitingMainExpiryCandidates() {
		return true
	}
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub != nil && sub.State() == SubAgentStateRunning {
			return true
		}
	}
	return false
}

// hasWaitingMainExpiryCandidates reports whether a live worker or a parked task
// record is currently in waiting_main, i.e. a lifecycle sweep could find
// something to expire. The periodic trigger gates on it so sessions that never
// delegate do not wake the event loop.
func (a *MainAgent) hasWaitingMainExpiryCandidates() bool {
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub != nil && sub.State() == SubAgentStateWaitingMain {
			return true
		}
	}
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	for _, rec := range a.subs.taskRecords {
		if rec != nil && rec.RuntimeParked && SubAgentState(rec.State) == SubAgentStateWaitingMain {
			return true
		}
	}
	return false
}

// queueWaitingMainExpiryAlert makes an expired WaitingMain wait visible to the
// worker's owner: a risk_alert mailbox is queued through the same event path
// the failure handler uses (see handleAgentError), plus the matching
// control-plane AgentNotifyEvent. Pass the live worker or the parked record —
// whichever the sweep is settling. The mailbox is durable and routed to the
// owner's own inbox/queue even when the owner is parked or the main agent is
// idle.
func (a *MainAgent) queueWaitingMainExpiryAlert(sub *SubAgent, record *DurableTaskRecord, reason string) {
	var agentID, taskID, ownerAgentID, ownerTaskID, agentType, inReplyTo string
	if sub != nil {
		agentID = sub.instanceID
		taskID = sub.taskID
		ownerAgentID, ownerTaskID, _, _ = sub.ownerSnapshot()
		agentType = sub.agentDefName
		inReplyTo = firstReplyMessageID(sub)
	} else if record != nil {
		agentID = record.LatestInstanceID
		taskID = record.TaskID
		ownerAgentID = strings.TrimSpace(record.OwnerAgentID)
		ownerTaskID = strings.TrimSpace(record.OwnerTaskID)
		agentType = record.AgentDefName
	}
	if strings.TrimSpace(taskID) == "" {
		return
	}
	mailbox := &SubAgentMailboxMessage{
		AgentID:      agentID,
		TaskID:       taskID,
		OwnerAgentID: ownerAgentID,
		OwnerTaskID:  ownerTaskID,
		InReplyTo:    inReplyTo,
		Kind:         SubAgentMailboxKindRiskAlert,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      reason,
		Payload: fmt.Sprintf(
			"SubAgent task was abandoned because its wait for a main-agent reply expired and the work was not completed.\n- task_id: %s\n- agent_id: %s\n- required_action: re-delegate the work or explicitly resume the task; the pending agent request for this task is now expired, so a later reply to it will be rejected.",
			taskID, agentID,
		),
		RequiresAck: false,
	}
	a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: agentID, Payload: mailbox})
	a.emitToTUI(AgentNotifyEvent{
		AgentID:       agentID,
		TaskID:        taskID,
		AgentType:     agentType,
		ParentAgentID: controlPlaneAgentID(ownerAgentID),
		ParentTaskID:  ownerTaskID,
		TargetAgentID: controlPlaneAgentID(ownerAgentID),
		TargetTaskID:  ownerTaskID,
		Kind:          string(SubAgentMailboxKindRiskAlert),
		Message:       reason,
	})
}

// queueSubAgentStallAlert makes a stalled Running worker visible to its owner:
// a risk_alert mailbox is queued through the same event path the failure
// handler uses (see handleAgentError), plus the matching control-plane
// AgentNotifyEvent. Conservative by design: the worker is not killed — the
// owner checks the transcript/logs and decides whether to resume, re-delegate,
// or cancel. The sweep deduplicates per stall episode via stallAlertRaised.
func (a *MainAgent) queueSubAgentStallAlert(sub *SubAgent, reason string) {
	if a == nil || sub == nil {
		return
	}
	taskID := strings.TrimSpace(sub.taskID)
	if taskID == "" {
		return
	}
	ownerAgentID, ownerTaskID, _, _ := sub.ownerSnapshot()
	mailbox := &SubAgentMailboxMessage{
		AgentID:      sub.instanceID,
		TaskID:       taskID,
		OwnerAgentID: ownerAgentID,
		OwnerTaskID:  ownerTaskID,
		InReplyTo:    firstReplyMessageID(sub),
		Kind:         SubAgentMailboxKindRiskAlert,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      reason,
		Payload: fmt.Sprintf(
			"SubAgent is suspected of stalling: it is still running but has shown no state change or activity for an extended period.\n- task_id: %s\n- agent_id: %s\n- required_action: check the worker's transcript/logs for progress; re-delegate, resume, or cancel it explicitly if it is stuck.",
			taskID, sub.instanceID,
		),
		RequiresAck: false,
	}
	a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: sub.instanceID, Payload: mailbox})
	a.emitToTUI(AgentNotifyEvent{
		AgentID:       sub.instanceID,
		TaskID:        taskID,
		AgentType:     sub.agentDefName,
		ParentAgentID: controlPlaneAgentID(ownerAgentID),
		ParentTaskID:  ownerTaskID,
		TargetAgentID: controlPlaneAgentID(ownerAgentID),
		TargetTaskID:  ownerTaskID,
		Kind:          string(SubAgentMailboxKindRiskAlert),
		Message:       reason,
	})
}

// expireAgentRequestsAfterCancellation records a WaitingMain expiry in the
// durable request ledger. Without this write the "expired" state has no writer:
// a pending escalation request whose source task was just expiry-cancelled
// would otherwise stay pending forever until a later Notify marks it orphaned.
// The transition only applies to still-pending requests and only after the task
// record actually reads cancelled, so a guarded sweep that backed off or a
// conflicting settlement never expires a request whose worker still waits.
func (a *MainAgent) expireAgentRequestsAfterCancellation(taskID string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	if rec := a.taskRecordByTaskID(taskID); rec == nil || SubAgentState(rec.State) != SubAgentStateCancelled {
		return
	}
	a.agentRequestPersistMu.Lock()
	defer a.agentRequestPersistMu.Unlock()
	records := a.snapshotAgentRequests()
	changed := false
	for id, request := range records {
		if request == nil || request.State != "pending" || strings.TrimSpace(request.SourceTaskID) != taskID {
			continue
		}
		request.State = "expired"
		records[id] = request
		changed = true
	}
	if !changed {
		return
	}
	if err := a.persistAndPublishAgentRequests(records); err != nil {
		log.Warnf("failed to mark agent requests expired task_id=%v error=%v", taskID, err)
	}
}
