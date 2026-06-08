package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func agentEventMayChangeKeyPool(msg agentEventMsg) bool {
	if msg.closed {
		return false
	}
	switch evt := msg.event.(type) {
	case agent.IdleEvent, agent.ErrorEvent, agent.RateLimitUpdatedEvent, agent.KeyPoolChangedEvent, agent.RunningModelChangedEvent, agent.SessionRestoredEvent, agent.UsageUpdatedEvent:
		return true
	case agent.AgentActivityEvent:
		switch evt.Type {
		case agent.ActivityCooling, agent.ActivityRetryingKey:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

const (
	agentEventBatchMax          = 32
	agentEventStreamBatchWindow = 16 * time.Millisecond
)

func isStreamTextDeltaEvent(evt agent.AgentEvent) bool {
	switch evt.(type) {
	case agent.StreamTextEvent:
		return true
	default:
		return false
	}
}

func waitForAgentEvent(ch <-chan agent.AgentEvent) tea.Cmd {
	return func() tea.Msg {
		// Block until the first event arrives.
		evt, ok := <-ch
		batch := []agentEventMsg{{event: evt, closed: !ok}}
		if !ok {
			return agentEventBatchMsg(batch)
		}
		if isStreamTextDeltaEvent(evt) {
			deadline := time.NewTimer(agentEventStreamBatchWindow)
			defer deadline.Stop()
			for len(batch) < agentEventBatchMax {
				select {
				case ev, ok := <-ch:
					batch = append(batch, agentEventMsg{event: ev, closed: !ok})
					if !ok {
						return agentEventBatchMsg(batch)
					}
				case <-deadline.C:
					return agentEventBatchMsg(batch)
				}
			}
			return agentEventBatchMsg(batch)
		}
		// Drain any additional events that are already buffered.
		for len(batch) < agentEventBatchMax {
			select {
			case ev, ok := <-ch:
				batch = append(batch, agentEventMsg{event: ev, closed: !ok})
				if !ok {
					return agentEventBatchMsg(batch)
				}
			default:
				return agentEventBatchMsg(batch)
			}
		}
		return agentEventBatchMsg(batch)
	}
}

type agentEventEffects = uiEffects

func (m *Model) handleAgentEvent(msg agentEventMsg) tea.Cmd {
	if !msg.closed {
		m.markBackgroundDirty("agent-event")
	}
	if msg.closed {
		// Event channel closed (e.g. remote connection dropped). Reset streaming
		// state so the UI does not stay stuck on "streaming (Xs)".
		m.resetStreamingToIdle()
		if m.expectedAgentClose {
			m.expectedAgentClose = false
			m.completedExpectedAgentClose = true
			m.markAgentIdle(m.focusedAgentIDOrMain())
			m.inflightDraft = nil
			m.pauseQueuedDraftDrainOnce = true
			m.stopActiveAnimationIfIdle()
			m.clearPendingQuit()
			return nil
		}
		if m.reconnectFunc != nil {
			// Attempt auto-reconnect in the background.
			fn := m.reconnectFunc
			return func() tea.Msg {
				newAgent, err := fn()
				if err != nil {
					return reconnectFailedMsg{}
				}
				return reconnectedMsg{agent: newAgent}
			}
		}
		// Only show disconnect toast if we had received at least one event (avoid startup flash).
		if m.agentHadEvent {
			return m.enqueueToast("Connection lost, please reconnect", "warn")
		}
		return nil
	}

	m.agentHadEvent = true
	if _, ok := msg.event.(agent.IdleEvent); ok {
		m.pendingPoolSwitch = pendingPoolSwitchState{}
	}
	if evt, ok := msg.event.(agent.AgentActivityEvent); ok && evt.AgentID == m.focusedAgentIDOrMain() && evt.Type == agent.ActivityIdle {
		m.pendingPoolSwitch = pendingPoolSwitchState{}
	}
	effects := agentEventEffects{}

	if handled, sub := m.handleStreamingAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleTurnAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleSubAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleSessionAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleToolAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleMiscAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleHygieneAgentEvent(msg.event); handled {
		effects.merge(sub)
	}

	return m.applyUIEffects(effects)
}
