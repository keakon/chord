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
// loop any faster. The trigger event is only queued while a waiting_main,
// live-running, or pending-mailbox-work candidate exists.
const subAgentLifecycleSweepInterval = time.Minute

// EventSubAgentLifecycleSweep is the loop event the periodic wall-clock trigger
// dispatches to re-run the WaitingMain expiry sweep without any user message.
// Kept next to the sweeper because it is lifecycle-internal; dispatch lives in
// main_loop.go.
const EventSubAgentLifecycleSweep = "subagent_lifecycle_sweep"

// waitingMainExpiryClosedReasonPrefix is the text prefix every WaitingMain
// expiry records as the task's terminal ClosedReason (see policy.reason and
// the parked-record default below). The terminal state itself is Cancelled —
// the same state a user stop or a cascade cancellation writes — so restore
// uses this prefix to tell an expiry apart from those other cancellations,
// which never queue a risk_alert mailbox before their terminal commit and must
// not gain a notification from the restore-side re-synthesis.
const waitingMainExpiryClosedReasonPrefix = "expired waiting for main reply"

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

// waitingMainExpiryPolicy returns the policy the sweep runs under. It is
// resolved once from the effective orchestration config when the agent is
// built, which is the only way a MainAgent that can run a sweep comes into
// existence; a test that drives the sweep on a hand-built agent sets the same
// field to the budget it wants to exercise.
func (a *MainAgent) waitingMainExpiryPolicy() waitingMainExpiryPolicy {
	return a.waitingMainExpiry
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
		return fmt.Sprintf("%s (no reply within %d user turns)", waitingMainExpiryClosedReasonPrefix, p.turns)
	}
	return fmt.Sprintf("%s (no reply within %s)", waitingMainExpiryClosedReasonPrefix, now.Sub(since).Round(time.Minute))
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
	// Messages that landed in the queue before the task settled would otherwise
	// make canPark refuse forever; see dropSettledTaskQueuedInput.
	if state := sub.State(); isTerminalSubAgentState(state) && sub.hasPendingUserInput() {
		a.dropSettledTaskQueuedInput(sub, state)
	}
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
		switch state := sub.State(); {
		case !isTerminalSubAgentState(state):
			// A worker that can still be woken consumes the queue itself.
			if err := a.acquireWakeReactivationSlot(sub); err == nil {
				a.markSubAgentReactivated(sub, "Queued input arrived before parking")
				sub.armStartupWatchdog()
			}
			return false
		default:
			// A settled task cannot: nothing moves it back to Running, so the
			// queue would keep it unparkable forever — its run loop and LLM
			// client leaking, and global idle pinned to false. Drop the
			// messages, tell the owner they were never read, and park.
			a.dropSettledTaskQueuedInput(sub, state)
		}
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
	filterStaged := func(in []*SubAgentMailboxMessage) []*SubAgentMailboxMessage {
		if len(in) == 0 {
			return nil
		}
		out := in[:0]
		for _, msg := range in {
			if msg == nil || strings.TrimSpace(msg.AgentID) == agentID {
				continue
			}
			out = append(out, msg)
		}
		return out
	}
	// The mailbox queues and the staged batch are shared with the delivery
	// paths on other goroutines (the event-loop drains and the TUI-facing
	// manual-delivery claims), so the whole state removal — including the
	// active-batch head repair — runs under subAgentMailboxIDsMu.
	a.subAgentMailboxIDsMu.Lock()
	progress, hasProgress := a.subAgentInbox.progress[agentID]
	if hasProgress {
		delete(a.subAgentInbox.progress, agentID)
	}
	progressInQueue := false
	for _, msg := range a.subAgentInbox.progressQueue {
		if msg.MessageID == progress.MessageID {
			progressInQueue = true
			break
		}
	}
	progressPending := false
	for _, messageID := range a.subAgentInbox.progressPending {
		if messageID == progress.MessageID {
			progressPending = true
			break
		}
	}
	if hasProgress && !progressInQueue && !progressPending {
		a.releaseMailboxMemory(progress)
	}
	filterPending := a.subAgentInbox.progressPending[:0]
	for _, messageID := range a.subAgentInbox.progressPending {
		if a.subAgentInbox.progressPendingAgent[messageID] == agentID {
			delete(a.subAgentInbox.progressPendingAgent, messageID)
			delete(a.subAgentInbox.progressPendingAttempts, messageID)
			continue
		}
		filterPending = append(filterPending, messageID)
	}
	a.subAgentInbox.progressPending = filterPending
	filterProgress := func(in []SubAgentMailboxMessage) []SubAgentMailboxMessage {
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
	a.subAgentInbox.progressQueue = filterProgress(a.subAgentInbox.progressQueue)
	a.subAgentInbox.urgent = filter(a.subAgentInbox.urgent)
	a.subAgentInbox.normal = filter(a.subAgentInbox.normal)
	a.pendingSubAgentMailboxes = filterStaged(a.pendingSubAgentMailboxes)
	a.activeSubAgentMailboxes = filterStaged(a.activeSubAgentMailboxes)
	if a.activeSubAgentMailbox != nil && strings.TrimSpace(a.activeSubAgentMailbox.AgentID) == agentID {
		a.activeSubAgentMailbox = nil
	}
	if len(a.activeSubAgentMailboxes) > 0 {
		a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
	} else if a.activeSubAgentMailbox == nil {
		a.activeSubAgentMailboxAck = false
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
	a.subAgentMailboxIDsMu.Unlock()
	a.refreshSubAgentInboxSummary()
}

func (a *MainAgent) sweepSubAgentLifecycle() {
	currentTurn := a.explicitUserTurnCount.Load()
	policy := a.waitingMainExpiryPolicy()
	now := time.Now()
	changed := false
	// One snapshot for both passes below: the expiry pass can only settle
	// workers it already sees, and a worker it settles leaves Running, so the
	// stall pass skips it on the state check anyway.
	subs := a.subs.snapshotSubAgents()
	for _, sub := range subs {
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
				// cancelling silently. Terminal-commit ordering mirrors the
				// completion path (see handleAgentDone): the alert is
				// persisted before the guarded live settle commits the
				// terminal Cancelled, so a crash after the commit can no
				// longer lose the notification. The alert is delivered only
				// after the guarded settle wins — a concurrent reactivation
				// (a manual reply waking the worker) or a conflicting
				// settlement (the worker really completed or was stopped
				// while the sweep ran) must not leave a phantom expiry
				// behind (see withdrawPreparedWaitingMainExpiryAlert).
				alert, alertDurable := a.prepareWaitingMainExpiryAlert(sub, nil, reason)
				if a.settleLiveWaitingMainExpiry(sub, reason) {
					// The finalization below mirrors the terminal tail of
					// handleSubAgentCloseRequestedEvent, which the live expiry
					// branch cannot reuse because its commit must first win the
					// reactivation race under the lifecycle lock.
					a.reconcileTerminalTaskChildren(sub.taskID, SubAgentStateCancelled, reason)
					a.emitToTUI(AgentStatusEvent{AgentID: sub.instanceID, Status: "cancelled", Message: reason})
					a.releaseSubAgentSlot(sub)
					a.fileTrack.ReleaseAll(sub.instanceID)
					tools.StopAllSpawnedForAgent(sub.instanceID, "terminated on waiting-main expiry")
					a.parkSubAgent(sub.instanceID)
					if settled := a.taskRecordByTaskID(sub.taskID); settled != nil {
						a.deliverSettledWaitingMainExpiryAlert(alert, alertDurable, settled)
					}
					a.expireAgentRequestsAfterCancellation(sub.taskID)
					changed = true
				} else {
					a.withdrawPreparedWaitingMainExpiryAlert(alert)
				}
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
		expired := a.taskRecordByTaskID(taskID)
		reason := waitingMainExpiryClosedReasonPrefix
		if expired != nil {
			reason = policy.reason(currentTurn, expired.LastUpdatedTurn, expired.UpdatedAt, now)
		}
		// Terminal-commit ordering (mirror of the completion path in
		// handleAgentDone): the expiry alert is persisted BEFORE the guarded
		// settle below commits the terminal cancellation, so a crash after
		// the commit can no longer lose the owner notification. Apply and
		// delivery happen only after the guarded settle wins; when the settle
		// backs off the prepared alert is withdrawn (see
		// settleExpiredParkedWaitingMainTask).
		if a.settleExpiredParkedWaitingMainTask(taskID, expired, reason, stillExpiredParkedWaiting) {
			a.expireAgentRequestsAfterCancellation(taskID)
			changed = true
		}
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
	for _, sub := range subs {
		if sub == nil || sub.State() != SubAgentStateRunning {
			continue
		}
		// A live Running runtime whose durable record already settled
		// (completed/failed/cancelled) is a stale conflict survivor: raising a
		// stall alert would tell the owner a task needs attention after it
		// already finished. Skip it — the settle path owns terminal truth, and
		// the late-mailbox drop in the inbox layer covers anything that was
		// queued before the settlement landed.
		if rec := a.taskRecordByTaskID(strings.TrimSpace(sub.taskID)); rec != nil && !isNonTerminalTaskState(rec.State) {
			continue
		}
		reason := runningSubAgentStallReason(sub, now)
		if reason == "" {
			a.closeStallEpisodeIfAlerted(sub)
			continue
		}
		// While the main agent waits on a user-facing dialog (confirm,
		// question, or handoff) it dispatches no new input, so a healthy
		// Running worker legitimately produces no activity for as long as the
		// user takes to answer. Treat the wait as worker heartbeat: refresh
		// the activity clock and close any earlier stall episode, so the
		// quiet window never raises AGENT BLOCKED, and a worker that stays
		// silent after the dialog resolves alerts again as a fresh episode.
		if a.interaction.hasPendingUserInteraction() {
			sub.markActivity()
			a.closeStallEpisodeIfAlerted(sub)
			continue
		}
		if sub.stallAlertRaised() {
			continue
		}
		sub.raiseStallAlert()
		a.queueSubAgentStallAlert(sub, reason)
	}
}

// closeStallEpisodeIfAlerted closes a stall episode that was previously
// alerted: it clears the deduplication flag and tells the owner the worker
// recovered. The sweep reaches it on the first pass that finds the worker
// healthy again (fresh activity, or an open user dialog explaining the
// silence), so an AGENT BLOCKED card is not left standing after the worker
// resumed — the resolution closes the episode explicitly, and the worker
// alerts again only if a genuinely new quiet window exceeds the threshold.
func (a *MainAgent) closeStallEpisodeIfAlerted(sub *SubAgent) {
	if sub == nil || !sub.stallAlertRaised() {
		return
	}
	sub.clearStallAlert()
	a.queueSubAgentStallResolved(sub)
}

// queueSubAgentStallResolved notifies the owner that a previously alerted
// stall episode has ended: the worker is again showing state/progress
// updates, so the earlier risk_alert no longer applies and no action is
// needed. The alert card itself stays in history; the resolution is a
// separate message so the owner can tell a recovered worker from one that is
// still silently stalled.
func (a *MainAgent) queueSubAgentStallResolved(sub *SubAgent) {
	if a == nil || sub == nil {
		return
	}
	mailbox := newSubAgentRiskAlertMailbox(sub, nil, SubAgentStallResolvedSubtype, "stall resolved: worker resumed activity", fmt.Sprintf(
		"SubAgent stall episode resolved: the worker is again showing state or progress updates.\n- task_id: %s\n- agent_id: %s\n- required_action: none. The earlier AGENT BLOCKED alert for this task no longer applies; it will alert again only if the worker stalls anew.",
		strings.TrimSpace(sub.taskID), sub.instanceID,
	))
	a.dispatchSubAgentRiskAlert(mailbox, sub, nil)
}

// startSubAgentLifecycleSweep runs the periodic trigger that keeps WaitingMain
// expiry and the Running-worker stall watchdog independent of user speech.
// Without it the sweep only runs inside handleUserMessage, so a headless or
// silent session would never reclaim a parked worker or surface a stalled
// worker. The trigger deliberately avoids the idle/nudge chain so it survives
// the removal of that dead code; it wakes the loop at most once per interval
// and only while a waiting_main, live-running, or not-globally-idle candidate
// exists (the last term keeps draining stranded owned mailboxes after every
// worker is gone).
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
// find something to act on: a waiting_main expiry candidate, a live Running
// worker whose stall/health the sweep must keep watching, or a loop that is
// not globally idle. The last term keeps the cadence alive after every worker
// has gone terminal: a stranded owned mailbox (for example a child completion
// queued under an owner that already finished) is queued mailbox work, so it
// suppresses global idle, and no worker remains to ever fire another event —
// the periodic sweep is the only retry that can drain it into a main turn. The
// gate reads the global-idle flag rather than the mailbox queues because it
// runs on the trigger goroutine while the queues belong to the event loop. The
// trigger stays silent for sessions that never delegate and have no pending
// mailbox work, so a truly idle main is not woken.
func (a *MainAgent) hasSubAgentLifecycleSweepCandidates() bool {
	subs := a.subs.snapshotSubAgents()
	for _, sub := range subs {
		if sub != nil && sub.State() == SubAgentStateRunning {
			return true
		}
	}
	if a.waitingMainExpiryCandidatesAmong(subs) {
		return true
	}
	return !a.globalIdle.Load()
}

// hasWaitingMainExpiryCandidates reports whether a live worker or a parked task
// record is currently in waiting_main, i.e. a lifecycle sweep could find
// something to expire. The periodic trigger gates on it so sessions that never
// delegate do not wake the event loop.
func (a *MainAgent) hasWaitingMainExpiryCandidates() bool {
	return a.waitingMainExpiryCandidatesAmong(a.subs.snapshotSubAgents())
}

func (a *MainAgent) waitingMainExpiryCandidatesAmong(subs []*SubAgent) bool {
	for _, sub := range subs {
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

// newSubAgentRiskAlertMailbox builds the risk_alert mailbox every owner-visible
// worker alert shares. The task is identified by its live runtime when there is
// one and by its durable record otherwise; subtype, summary and payload
// describe the specific alert.
func newSubAgentRiskAlertMailbox(sub *SubAgent, record *DurableTaskRecord, subtype, summary, payload string) *SubAgentMailboxMessage {
	agentID, taskID, ownerAgentID, ownerTaskID, inReplyTo := "", "", "", "", ""
	if sub != nil {
		agentID = sub.instanceID
		taskID = sub.taskID
		ownerAgentID, ownerTaskID, _, _ = sub.ownerSnapshot()
		inReplyTo = firstReplyMessageID(sub)
	} else if record != nil {
		agentID = strings.TrimSpace(record.LatestInstanceID)
		taskID = strings.TrimSpace(record.TaskID)
		ownerAgentID = strings.TrimSpace(record.OwnerAgentID)
		ownerTaskID = strings.TrimSpace(record.OwnerTaskID)
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	return &SubAgentMailboxMessage{
		AgentID:      strings.TrimSpace(agentID),
		TaskID:       taskID,
		OwnerAgentID: ownerAgentID,
		OwnerTaskID:  ownerTaskID,
		InReplyTo:    inReplyTo,
		Kind:         SubAgentMailboxKindRiskAlert,
		Subtype:      subtype,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      strings.TrimSpace(summary),
		Payload:      payload,
		RequiresAck:  false,
	}
}

// dispatchSubAgentRiskAlert hands a built risk_alert mailbox to the delivery
// queue and emits the matching control-plane AgentNotifyEvent. Every risk-alert
// producer ends this way; how durable the message already is differs per
// producer and is settled by the caller before it dispatches.
func (a *MainAgent) dispatchSubAgentRiskAlert(mailbox *SubAgentMailboxMessage, sub *SubAgent, record *DurableTaskRecord) {
	if mailbox == nil {
		return
	}
	agentType := ""
	if sub != nil {
		agentType = sub.agentDefName
	} else if record != nil {
		agentType = record.AgentDefName
	}
	ownerAgentID := strings.TrimSpace(mailbox.OwnerAgentID)
	ownerTaskID := strings.TrimSpace(mailbox.OwnerTaskID)
	if strings.TrimSpace(mailbox.MessageID) == "" {
		mailbox.MessageID = a.nextSubAgentMailboxMessageID(mailbox.AgentID)
	}
	a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: strings.TrimSpace(mailbox.AgentID), Payload: mailbox})
	a.emitToTUI(AgentNotifyEvent{
		AgentID:       strings.TrimSpace(mailbox.AgentID),
		TaskID:        strings.TrimSpace(mailbox.TaskID),
		AgentType:     agentType,
		ParentAgentID: controlPlaneAgentID(ownerAgentID),
		ParentTaskID:  ownerTaskID,
		TargetAgentID: controlPlaneAgentID(ownerAgentID),
		TargetTaskID:  ownerTaskID,
		Kind:          string(SubAgentMailboxKindRiskAlert),
		Subtype:       strings.TrimSpace(mailbox.Subtype),
		Message:       strings.TrimSpace(mailbox.Summary),
		MessageID:     mailbox.MessageID,
	})
}

// dropSettledTaskQueuedInput discards the messages a settled task can no longer
// read and tells its owner they were dropped. Keeping them queued instead is
// not an option: no path returns a terminal runtime to Running, so the queue
// would block parking forever and with it the worker's cancel, its LLM client
// release and the global idle transition. Durability is the same best-effort
// contract the stall alert uses — the queued mailbox event persists the message
// on its own path — because a notification must never block terminal cleanup.
func (a *MainAgent) dropSettledTaskQueuedInput(sub *SubAgent, state SubAgentState) {
	dropped := sub.discardPendingUserInput()
	if dropped == 0 {
		return
	}
	taskID := strings.TrimSpace(sub.taskID)
	log.Warnf("dropping messages queued for a settled SubAgent task agent_id=%v task_id=%v state=%v dropped=%v", sub.instanceID, taskID, state, dropped)
	summary := fmt.Sprintf("%d message(s) were dropped because task %s had already finished as %s", dropped, taskID, state)
	mailbox := newSubAgentRiskAlertMailbox(sub, nil, agentMessageSubtypeUndeliveredInput, summary,
		fmt.Sprintf(
			"Messages arrived for a SubAgent task that had already reached its final state, so they were never read.\n- task_id: %s\n- agent_id: %s\n- final_state: %s\n- dropped_messages: %d\n- required_action: the instruction was not applied; delegate the remaining work again if it still matters.",
			taskID, sub.instanceID, state, dropped,
		))
	a.dispatchSubAgentRiskAlert(mailbox, sub, nil)
}

// settleLiveWaitingMainExpiry runs the guarded expiry settlement for a still
// live WaitingMain worker. The expiry may only win while the worker is still
// waiting: a manual reply or resume that reactivated it first means the wait
// ended normally, so the commit is refused and the prepared alert is withdrawn
// by the caller. The guard is twofold — the lifecycle lock serializes against
// the manual-delivery and parking paths, and commitTerminalTaskFrom's
// compare-and-transition serializes against every other reactivation that only
// takes the runtime's state lock — so a freshly resumed attempt is never
// cancelled as expired. Reports whether the expiry committed. A returned error
// alone does not mean the commit was refused: commitTerminalTaskFrom reports a
// durability degradation (the registry write failed after the terminal state
// was committed) as an error too, so the caller checks the committed record to
// tell the two apart and still runs the expiry closeout for a task that is
// already Cancelled.
func (a *MainAgent) settleLiveWaitingMainExpiry(sub *SubAgent, reason string) bool {
	if a == nil || sub == nil {
		return false
	}
	sub.lifecycleMu.Lock()
	defer sub.lifecycleMu.Unlock()
	if sub.State() != SubAgentStateWaitingMain {
		return false
	}
	if _, _, err := a.commitTerminalTaskFrom(sub, SubAgentStateWaitingMain, SubAgentStateCancelled, reason, reason, nil); err != nil {
		if !a.taskCommittedToState(sub.taskID, SubAgentStateCancelled) {
			return false
		}
	}
	return true
}

// settleExpiredParkedWaitingMainTask runs the guarded expiry settlement for
// one parked WaitingMain record the sweep's collection pass found expired:
// the expiry alert is persisted ahead of the guarded terminal commit
// (crash-window ordering) and applied/delivered only when the guarded settle
// wins Cancelled. When the settle backs off — the task was revived or
// otherwise changed in the window — the wait did not actually expire, so the
// prepared alert is withdrawn instead: a persisted-but-undeliverable alert
// would otherwise be replayed by a restore after a crash (an unconsumed
// message whose task later settles as a matching expiry would surface an
// expiry that never happened). Reports whether the expiry committed.
func (a *MainAgent) settleExpiredParkedWaitingMainTask(taskID string, expired *DurableTaskRecord, reason string, guard func(*DurableTaskRecord) bool) bool {
	alert, alertDurable := a.prepareWaitingMainExpiryAlert(nil, expired, reason)
	outcome := a.settleDetachedTerminalTaskGuarded(taskID, SubAgentStateCancelled, reason, reason, guard)
	if outcome != SubAgentStateCancelled {
		a.withdrawPreparedWaitingMainExpiryAlert(alert)
		return false
	}
	if settled := a.taskRecordByTaskID(taskID); settled != nil {
		a.deliverSettledWaitingMainExpiryAlert(alert, alertDurable, settled)
	}
	return true
}

// withdrawPreparedWaitingMainExpiryAlert removes a WaitingMain expiry alert
// that prepareWaitingMainExpiryAlert persisted but whose guarded terminal
// settle did not win. The alert was persisted ahead of the settle for
// crash-window ordering; once the settle loses it must never be delivered in
// this process nor replayed by a restore, so its mailbox.log line is rolled
// back (see rollbackSubAgentMailboxMessage). A nil alert or a persist that
// never appended a line has nothing to roll back.
func (a *MainAgent) withdrawPreparedWaitingMainExpiryAlert(alert *SubAgentMailboxMessage) {
	if alert == nil {
		return
	}
	log.Infof("withdrawing expiry risk_alert whose guarded settle did not win task_id=%v agent_id=%v message_id=%v", alert.TaskID, alert.AgentID, alert.MessageID)
	a.rollbackSubAgentMailboxMessage(alert, alert.rollbackAppendOffset)
}

// buildWaitingMainExpiryAlertMailbox constructs the risk_alert mailbox that
// surfaces an expired WaitingMain wait to its owner. The expiry sweep (both
// branches) and the restore-side re-synthesis of an alert lost to the
// terminal-commit crash window build through this helper so the delivered
// notification stays identical across paths.
func (a *MainAgent) buildWaitingMainExpiryAlertMailbox(sub *SubAgent, record *DurableTaskRecord, reason string) *SubAgentMailboxMessage {
	summary := strings.TrimSpace(reason)
	if summary == "" {
		summary = waitingMainExpiryClosedReasonPrefix
	}
	mailbox := newSubAgentRiskAlertMailbox(sub, record, agentMessageSubtypeWaitingExpiry, summary, "")
	if mailbox == nil {
		return nil
	}
	mailbox.Payload = fmt.Sprintf(
		"SubAgent task was abandoned because its wait for a main-agent reply expired and the work was not completed.\n- task_id: %s\n- agent_id: %s\n- required_action: re-delegate the work; the pending agent request for this task is now expired, so a later reply to it will be rejected.",
		mailbox.TaskID, mailbox.AgentID,
	)
	// No mailbox.jsonl line has been appended for this alert yet. The zero
	// value would otherwise let a settle backoff roll the whole log back to
	// byte zero (see rollbackSubAgentMailboxMessage, which only guards offset
	// < 0), wiping earlier rows; keep -1 until persistSubAgentMailboxMessage
	// returns a trusted start offset.
	mailbox.rollbackAppendOffset = -1
	return mailbox
}

// prepareWaitingMainExpiryAlert persists the expiry risk_alert mailbox for an
// expired WaitingMain wait ahead of its guarded terminal settle, without
// applying it. Both sweep branches deliberately defer the apply until the
// guarded settle wins: applying would refresh the parked record's update
// clock through syncTaskRecordFromMailbox and make the settle's re-run of the
// expiry predicate back off. A persistence failure stays best-effort: the
// message is not marked seen, so the queued delivery event retries the write
// through its own persist path. The message carries the offset of its
// mailbox.jsonl line so a settle that backs off can roll the line back (see
// withdrawPreparedWaitingMainExpiryAlert).
func (a *MainAgent) prepareWaitingMainExpiryAlert(sub *SubAgent, record *DurableTaskRecord, reason string) (*SubAgentMailboxMessage, bool) {
	mailbox := a.buildWaitingMainExpiryAlertMailbox(sub, record, reason)
	if mailbox == nil {
		return nil, false
	}
	a.normalizeSubAgentMailboxMessage(mailbox)
	appendOffset, err := a.persistSubAgentMailboxMessageWithOffset(*mailbox)
	if err != nil {
		log.Warnf("expiry risk_alert mailbox durability degraded task_id=%v agent_id=%v error=%v (will retry through the mailbox queue)", mailbox.TaskID, mailbox.AgentID, err)
		return mailbox, false
	}
	mailbox.rollbackAppendOffset = appendOffset
	a.markSubAgentMailboxSeen(mailbox.MessageID)
	return mailbox, true
}

// deliverSettledWaitingMainExpiryAlert applies and delivers the expiry
// risk_alert mailbox that prepareWaitingMainExpiryAlert persisted before the
// parked task's guarded settle won. Applying only now is safe: the record is
// already terminal Cancelled, so refreshing its LastMailboxID and summary
// cannot make an expiry guard back off. When the pre-settle persist failed the
// message was not marked seen, so the queued delivery event performs the
// apply, persistence retry, and delivery by itself.
func (a *MainAgent) deliverSettledWaitingMainExpiryAlert(mailbox *SubAgentMailboxMessage, durable bool, settled *DurableTaskRecord) {
	if mailbox == nil {
		return
	}
	if durable {
		a.applyPersistedSubAgentMailboxMessage(mailbox)
	}
	a.dispatchSubAgentRiskAlert(mailbox, nil, settled)
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
	mailbox := newSubAgentRiskAlertMailbox(sub, nil, "", reason, fmt.Sprintf(
		"SubAgent is suspected of stalling: it is still Running but has shown no state or progress update for an extended period — it is not executing a tool and not waiting inside a user-facing dialog.\n- task_id: %s\n- agent_id: %s\n- required_action: check the worker's transcript/logs for progress. A wait on the owner or the user must be surfaced explicitly through question, escalate, or notify instead of staying silent; if the worker is genuinely stuck, resume, re-delegate, or cancel it explicitly.",
		strings.TrimSpace(sub.taskID), sub.instanceID,
	))
	a.dispatchSubAgentRiskAlert(mailbox, sub, nil)
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
	for _, request := range records {
		if request == nil || request.State != "pending" || strings.TrimSpace(request.SourceTaskID) != taskID {
			continue
		}
		request.State = "expired"
		changed = true
	}
	if !changed {
		return
	}
	if err := a.persistAndPublishAgentRequests(records); err != nil {
		log.Warnf("failed to mark agent requests expired task_id=%v error=%v", taskID, err)
	}
}
