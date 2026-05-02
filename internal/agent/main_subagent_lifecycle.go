package agent

import (
	"github.com/keakon/golog/log"
	"os"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

const (
	waitingPrimaryExpiryUserTurns   = uint64(5)
	failedCancelledGCGraceUserTurns = uint64(1)
)

func (a *MainAgent) noteSubAgentStateTransition(sub *SubAgent, state SubAgentState) {
	if a == nil || sub == nil {
		return
	}
	if a.subAgentStateEnteredTurn == nil {
		a.subAgentStateEnteredTurn = make(map[string]uint64)
	}
	switch state {
	case SubAgentStateRunning:
		delete(a.subAgentStateEnteredTurn, sub.instanceID)
	case SubAgentStateWaitingPrimary, SubAgentStateWaitingDescendant, SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled:
		a.subAgentStateEnteredTurn[sub.instanceID] = a.explicitUserTurnCount
	}
}

func (a *MainAgent) closeSubAgent(agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	a.mu.Lock()
	sub := a.subAgents[agentID]
	delete(a.subAgents, agentID)
	delete(a.nudgeCounts, agentID)
	delete(a.subAgentStateEnteredTurn, agentID)
	a.mu.Unlock()
	if sub == nil {
		return
	}
	if focused := a.focusedAgent.Load(); focused != nil && focused.instanceID == agentID {
		a.focusedAgent.Store(nil)
	}
	if sub.semHeld {
		a.releaseSubAgentSlot(sub)
	}
	a.fileTrack.ReleaseAll(agentID)
	tools.StopAllSpawnedForAgent(agentID, "terminated on subagent close")
	sub.cancel()
	a.removeSubAgentMailboxState(agentID)
	_ = os.Remove(subAgentMetaPath(a.sessionDir, agentID))
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
		delete(a.ownedSubAgentMailboxes, agentID)
		for ownerID, queued := range a.ownedSubAgentMailboxes {
			filtered := queued[:0]
			for _, msg := range queued {
				if strings.TrimSpace(msg.AgentID) == agentID || strings.TrimSpace(msg.OwnerAgentID) == agentID {
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
	if len(a.subAgents) == 0 {
		return
	}
	var toClose []string
	changed := false
	for _, sub := range a.subAgents {
		if sub == nil {
			continue
		}
		state := sub.State()
		enteredTurn := a.subAgentStateEnteredTurn[sub.instanceID]
		switch state {
		case SubAgentStateWaitingPrimary:
			if a.explicitUserTurnCount >= enteredTurn+waitingPrimaryExpiryUserTurns {
				a.handleSubAgentCloseRequestedEvent(Event{
					Type:     EventSubAgentCloseRequested,
					SourceID: sub.instanceID,
					Payload: &SubAgentCloseRequestedPayload{
						Reason:       "expired waiting for primary reply",
						ClosedReason: "expired waiting for primary reply",
						FinalState:   SubAgentStateCancelled,
					},
				})
				changed = true
			}
		case SubAgentStateWaitingDescendant:
			// Descendant waits are durable coordination state; do not expire via
			// user-turn GC. Recovery or explicit control actions decide what to do.
		case SubAgentStateFailed, SubAgentStateCancelled:
			if a.explicitUserTurnCount >= enteredTurn+failedCancelledGCGraceUserTurns {
				toClose = append(toClose, sub.instanceID)
			}
		}
	}
	if len(toClose) == 0 {
		if changed {
			a.saveRecoverySnapshot()
		}
		return
	}
	for _, id := range toClose {
		log.Debugf("closing subagent via lifecycle GC agent_id=%v user_turn=%v", id, a.explicitUserTurnCount)
		finalState := SubAgentStateCancelled
		if sub := a.subAgentByID(id); sub != nil {
			finalState = sub.State()
		}
		a.handleSubAgentCloseRequestedEvent(Event{
			Type:     EventSubAgentCloseRequested,
			SourceID: id,
			Payload:  &SubAgentCloseRequestedPayload{Reason: "closed by lifecycle GC", FinalState: finalState},
		})
	}
	a.saveRecoverySnapshot()
}
