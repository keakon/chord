package agent

import (
	"log/slog"
	"time"

	"github.com/keakon/chord/internal/message"
)

func (a *MainAgent) startPersistLoop() {
	a.persistLoopOnce.Do(func() {
		go a.runPersistLoop()
	})
}

func (a *MainAgent) closePersistLoop() {
	a.persistCloseOnce.Do(func() {
		if a.persistCh != nil {
			close(a.persistCh)
		}
	})
}

// runPersistLoop reads persistence requests from persistCh and writes them
// in order to the JSONL file. It runs in its own goroutine started by Run.
// When persistCh is closed, it drains remaining entries and exits.
func (a *MainAgent) runPersistLoop() {
	defer close(a.persistDone)
	for entry := range a.persistCh {
		if entry.barrier != nil {
			close(entry.barrier)
			continue
		}
		if entry.recovery != nil {
			if err := entry.recovery.PersistMessage(entry.agentID, entry.msg); err != nil {
				slog.Warn("failed to persist message", "agent_id", entry.agentID, "error", err)
			}
		}
	}
}

// persistAsync sends a persistence request to the ordered channel.
// Blocks if the channel is full (preferable to silently dropping
// persistence data). No-op once Shutdown has closed the channel.
func (a *MainAgent) persistAsync(agentID string, msg message.Message) {
	if a.shuttingDown.Load() {
		return
	}
	if a.persistCh == nil {
		return
	}
	start := time.Now()
	select {
	case a.persistCh <- persistEntry{agentID: agentID, msg: msg, recovery: a.recovery}:
	case <-a.stoppingCh:
		// Agent is shutting down; don't block indefinitely.
	}
	blocked := time.Since(start)
	if blocked <= 50*time.Millisecond {
		return
	}
	callID := ""
	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			if tc.ID == "" {
				continue
			}
			if callID == "" {
				callID = tc.ID
			}
			a.recordToolTracePersistBlock(tc.ID, blocked)
		}
	} else if msg.Role == "tool" && msg.ToolCallID != "" {
		callID = msg.ToolCallID
		a.recordToolTracePersistBlock(callID, blocked)
	}
	slog.Warn("persistAsync enqueue blocked",
		"role", msg.Role,
		"tool_call_id", callID,
		"blocked_ms", blocked.Milliseconds(),
	)
}

// flushPersist blocks until all persistence requests queued before this call
// have been written to disk. It is used before rewriting session files during
// context compaction or session switches.
func (a *MainAgent) flushPersist() {
	if a.persistCh == nil || a.shuttingDown.Load() {
		return
	}
	barrier := make(chan struct{})
	select {
	case a.persistCh <- persistEntry{barrier: barrier}:
	case <-a.stoppingCh:
		return
	}
	<-barrier
}
