package agent

import (
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// persistencePump owns the ordered async-persistence channel and its drain
// goroutine's lifecycle. It was carved out of MainAgent, where the channel, the
// done signal, and the two sync.Once guards were four loose fields. The pump
// only knows how to enqueue and drain entries; the per-entry work (recovery
// writes, barriers) is supplied by the caller via start's handler, and
// MainAgent-specific concerns (shutdown gating, tool-trace timing) stay on
// MainAgent.
type persistencePump struct {
	ch        chan persistEntry
	done      chan struct{}
	closeOnce sync.Once
	loopOnce  sync.Once
}

func newPersistencePump(buffer int) *persistencePump {
	return &persistencePump{
		ch:   make(chan persistEntry, buffer),
		done: make(chan struct{}),
	}
}

// start launches the drain loop exactly once. handle is invoked for each entry
// in arrival order; done is closed when the channel is closed and drained.
func (p *persistencePump) start(handle func(persistEntry)) {
	p.loopOnce.Do(func() {
		go func() {
			defer close(p.done)
			for entry := range p.ch {
				handle(entry)
			}
		}()
	})
}

// close closes the channel exactly once so the drain loop can finish.
func (p *persistencePump) close() {
	p.closeOnce.Do(func() {
		if p.ch != nil {
			close(p.ch)
		}
	})
}

// enqueue sends entry, returning false if the pump is unusable (nil channel) or
// stopping fired before the send completed.
func (p *persistencePump) enqueue(entry persistEntry, stopping <-chan struct{}) bool {
	if p == nil || p.ch == nil {
		return false
	}
	select {
	case p.ch <- entry:
		return true
	case <-stopping:
		return false
	}
}

func (a *MainAgent) startPersistLoop() {
	a.persist.start(func(entry persistEntry) {
		if entry.barrier != nil {
			close(entry.barrier)
			return
		}
		if entry.recovery != nil {
			if err := entry.recovery.PersistMessage(entry.agentID, entry.msg); err != nil {
				log.Warnf("failed to persist message agent_id=%v error=%v", entry.agentID, err)
			}
		}
	})
}

func (a *MainAgent) closePersistLoop() {
	a.persist.close()
}

// persistAsync sends a persistence request to the ordered channel.
// Blocks if the channel is full (preferable to silently dropping
// persistence data). No-op once Shutdown has closed the channel.
func (a *MainAgent) persistAsync(agentID string, msg message.Message) {
	if a.shuttingDown.Load() {
		return
	}
	start := time.Now()
	if !a.persist.enqueue(persistEntry{agentID: agentID, msg: msg, recovery: a.recovery}, a.stoppingCh) {
		return
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
	log.Warnf("persistAsync enqueue blocked role=%v tool_call_id=%v blocked_ms=%v", msg.Role, callID, blocked.Milliseconds())
}

// flushPersist blocks until all persistence requests queued before this call
// have been written to disk. It is used before rewriting session files during
// context compaction or session switches.
func (a *MainAgent) flushPersist() {
	if a.shuttingDown.Load() {
		return
	}
	barrier := make(chan struct{})
	if !a.persist.enqueue(persistEntry{barrier: barrier}, a.stoppingCh) {
		return
	}
	<-barrier
}
