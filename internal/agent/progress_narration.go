package agent

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

const (
	progressNoticeInterval = 5 * time.Minute
	progressNoticeRounds   = 10
	progressNoticeText     = "Before continuing with more tools, briefly tell the user what is known or still unresolved and what comes next, then continue the task. Thinking output does not replace this update. If the task is complete, give the final result instead."
)

// progressNarration belongs only to a main turn. Tool settlement, streaming,
// request dispatch, and interaction waits access it from different goroutines.
type progressNarration struct {
	mu                 sync.Mutex
	since              time.Time
	pausedAt           time.Time
	paused             time.Duration
	waiters            int
	rounds             int
	silentRoundPending bool
	staged             bool
	delivered          bool
}

func (p *progressNarration) text(text string, now time.Time) {
	if p == nil || strings.TrimSpace(message.NormalizeInvisibleText(text)) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.since = now
	p.paused = 0
	if p.waiters > 0 {
		p.pausedAt = now
	}
	p.rounds = 0
	p.silentRoundPending = false
	p.staged = false
	p.delivered = false
}

func (p *progressNarration) toolRoundStarted(hasText bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.silentRoundPending = !hasText
	p.mu.Unlock()
}

func (p *progressNarration) toolRoundSettled() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.silentRoundPending {
		p.rounds++
		p.silentRoundPending = false
	}
	p.mu.Unlock()
}

func (p *progressNarration) beginWait(now time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waiters == 0 {
		p.pausedAt = now
	}
	p.waiters++
}

func (p *progressNarration) endWait(now time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waiters--
	if p.waiters == 0 {
		p.paused += now.Sub(p.pausedAt)
		p.pausedAt = time.Time{}
	}
}

func pauseProgressNarration(ctx context.Context) func() {
	turn := turnFromContext(ctx)
	if turn == nil || turn.progress == nil {
		return func() {}
	}
	p := turn.progress
	p.beginWait(time.Now())
	return func() { p.endWait(time.Now()) }
}

func (p *progressNarration) stage(now time.Time) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.staged = !p.delivered && p.waiters == 0 && !p.since.IsZero() &&
		p.rounds >= progressNoticeRounds && now.Sub(p.since)-p.paused >= progressNoticeInterval
	return p.staged
}

func (p *progressNarration) confirm() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.staged || p.delivered {
		return false
	}
	p.staged = false
	p.delivered = true
	return true
}

func (p *progressNarration) stagedForDispatch() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.staged && !p.delivered
}

func (a *MainAgent) buildProgressNoticeOverlay() (message.Message, bool) {
	turn := a.currentTurn()
	if turn == nil || turn.progress == nil || turn.Ctx.Err() != nil || a.sessionExecutionPaused() || !turn.progress.stage(time.Now()) {
		return message.Message{}, false
	}
	return message.Message{
		Role: message.RoleUser, Kind: message.KindTurnOverlay,
		Content: message.SystemReminderOverlayText(progressNoticeText),
	}, true
}

type progressNoticeDispatchEvent struct {
	turn         *Turn
	sessionEpoch uint64
}

// confirmProgressNotice runs at the existing request-dispatch confirmation
// point. A request cancelled before dispatch never consumes the reminder.
func (a *MainAgent) confirmProgressNotice(turn *Turn) {
	if turn == nil || a.ctxMgr == nil || !turn.progress.confirm() {
		return
	}
	msg := message.Message{
		Role: message.RoleUser, Kind: message.KindProgressNotice,
		Content: message.SystemReminderOverlayText(progressNoticeText),
	}
	index := a.ctxMgr.MessageCount()
	a.ctxMgr.Append(msg)
	a.persistAsyncAfter(identity.MainAgentID, msg, func(err error) {
		if err != nil {
			a.noteOverlayAppendPersistFailure(msg, identity.MainAgentID, index)
			a.notePersistenceFailure(err)
			return
		}
		a.emitToTUI(ProgressNoticeEvent{Message: progressNoticeText, MessageIndex: index})
	})
}

// ProgressNoticeEvent is emitted only after the reminder's transcript row is
// durable. Restored views rebuild the same card from KindProgressNotice.
type ProgressNoticeEvent struct {
	Message      string
	MessageIndex int
}

func (ProgressNoticeEvent) agentEvent() {}
