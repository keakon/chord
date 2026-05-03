package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/hook"
)

// Run starts the blocking event loop. It returns when ctx is cancelled,
// the agent's parent context is cancelled, or an unrecoverable error occurs.
// The caller should run this in a dedicated goroutine:
//
//	go agent.Run(ctx)
func (a *MainAgent) Run(ctx context.Context) error {
	a.started.Store(true)
	log.Debugf("agent event loop started instance=%v model=%v", a.instanceID, a.modelName)
	if _, err := a.fireHook(ctx, hook.OnSessionStart, 0, map[string]any{}); err != nil {
		log.Warnf("on_session_start hook error error=%v", err)
	}
	if a.consumeStartupResumePending() {
		sessionID := a.startupResumeSessionIDValue()
		if loadedAt := a.startupResumeLoadedAtValue(); !loadedAt.IsZero() {
			log.Debugf("startup resume ready timing session=%v loaded_to_ready_ms=%v", sessionID, time.Since(loadedAt).Milliseconds())
		}
		a.emitToTUI(SessionSwitchStartedEvent{Kind: "resume", SessionID: sessionID})
		a.emitInteractiveToTUI(a.parentCtx, SessionRestoredEvent{})
	}

	// Start the async persistence loop.
	a.startPersistLoop()

	defer func() {
		log.Debugf("agent event loop stopped instance=%v", a.instanceID)
		// 1. Signal interactive senders to stop.
		close(a.stoppingCh)
		// 2. Wait for ConfirmFunc/QuestionFunc goroutines to exit.
		a.toolWg.Wait()
		// 3. Wait for async TUI producers (for example, main LLM goroutines) to
		// finish any cancellation/flush work before closing outputCh.
		a.outputWg.Wait()
		// 4. Now safe to close outputCh (all producers stopped).
		a.outputMu.Lock()
		a.outputClosed.Store(true)
		close(a.outputCh)
		a.outputMu.Unlock()
		// 5. Signal Shutdown() that Run has fully exited.
		close(a.done)
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.parentCtx.Done():
			return a.parentCtx.Err()
		case evt := <-a.eventCh:
			a.dispatch(evt)
		}
	}
}

// dispatch routes an internal event to the appropriate handler. It runs on the
// single event-loop goroutine, so handlers do not need to synchronise access to
// turn state.
func (a *MainAgent) dispatch(evt Event) {
	switch evt.Type {
	case EventUserMessage:
		a.handleUserMessage(evt)
	case EventPendingDraftUpsert:
		a.handlePendingDraftUpsert(evt)
	case EventPendingDraftRemove:
		a.handlePendingDraftRemove(evt)
	case EventAppendContext:
		a.handleAppendContext(evt)
	case EventLLMResponse:
		a.handleLLMResponse(evt)
	case EventToolResult:
		a.handleToolResult(evt)
	case EventTurnCancelled:
		a.handleTurnCancelled(evt)
	case EventAgentError:
		a.handleAgentError(evt)
	case EventExecutePlan:
		a.handleExecutePlanEvent(evt)
	case EventSessionControl:
		a.handleSessionControlEvent(evt)
	case EventAgentDone:
		a.handleAgentDone(evt)
	case EventAgentIdle:
		a.handleAgentIdle(evt)
	case EventAgentNotify:
		a.handleAgentNotify(evt)
	case EventEscalate:
		a.handleEscalate(evt)
	case EventSubAgentMailbox:
		a.handleSubAgentMailboxEvent(evt)
	case EventSubAgentStateChanged:
		a.handleSubAgentStateChangedEvent(evt)
	case EventSubAgentCloseRequested:
		a.handleSubAgentCloseRequestedEvent(evt)
	case EventSubAgentProgressUpdated:
		a.handleSubAgentProgressUpdatedEvent(evt)
	case EventSubAgentSendMessage:
		a.handleSubAgentSendMessageEvent(evt)
	case EventSubAgentStop:
		a.handleSubAgentStopEvent(evt)
	case EventAgentLog:
		a.handleAgentLog(evt)
	case EventResetNudge:
		a.handleResetNudge(evt)
	case EventSpawnFinished:
		a.handleSpawnFinished(evt)
	case EventContinue:
		a.handleContinueFromContext(evt)
	case EventLoopAssessment:
		a.handleLoopAssessment(evt)
	case EventCompactionReady:
		a.handleCompactionReady(evt)
	case EventCompactionFailed:
		a.handleCompactionFailed(evt)
	case EventCompactionOversizeSuspend:
		a.handleCompactionOversizeSuspend(evt)
	default:
		log.Warnf("unknown event type in agent dispatch type=%v seq=%v", evt.Type, evt.Seq)
	}
}

// sendEvent assigns a monotonic sequence number and writes the event to the
// internal event bus. This may block if the bus is full (cap 256).
func (a *MainAgent) sendEvent(evt Event) {
	evt.Seq = a.eventSeq.Add(1)
	a.eventCh <- evt
}

func reliableOutputEventLog(evt AgentEvent) (string, []any, bool) {
	switch e := evt.(type) {
	case AgentActivityEvent:
		if e.Type == ActivityIdle {
			return "", nil, false
		}
		return "TUI output channel full, waiting to deliver agent activity event", []any{
			"activity", e.Type,
			"agent_id", e.AgentID,
		}, true
	case ToolCallUpdateEvent:
		if !e.ArgsStreamingDone {
			return "", nil, false
		}
		return "TUI output channel full, waiting to deliver tool-arg completion event", []any{
			"event_type", fmt.Sprintf("%T", evt),
			"tool_id", e.ID,
		}, true
	case ToolCallStartEvent, ToolCallExecutionEvent, ToolResultEvent, SessionRestoredEvent, PendingDraftConsumedEvent, ForkSessionEvent, ErrorEvent, AgentStatusEvent, InfoEvent, ToastEvent, AssistantMessageEvent, LoopNoticeEvent, LoopStateChangedEvent:
		return "TUI output channel full, waiting to deliver critical event", []any{
			"event_type", fmt.Sprintf("%T", evt),
		}, true
	default:
		return "", nil, false
	}
}

func (a *MainAgent) emitReliableToTUI(evt AgentEvent, warnMsg string, warnAttrs ...any) {
	a.outputMu.RLock()
	if a.outputClosed.Load() {
		a.outputMu.RUnlock()
		return
	}
	select {
	case a.outputCh <- evt:
		a.outputMu.RUnlock()
		return
	default:
		a.outputMu.RUnlock()
		log.Warnf("%s attrs=%v", warnMsg, warnAttrs)
	}
	select {
	case <-a.stoppingCh:
		return
	default:
	}
	a.outputMu.RLock()
	if a.outputClosed.Load() {
		a.outputMu.RUnlock()
		return
	}
	select {
	case a.outputCh <- evt:
		a.outputMu.RUnlock()
	case <-a.stoppingCh:
		a.outputMu.RUnlock()
	}
}

// emitToTUI sends an AgentEvent to the output channel. Most events are
// best-effort and may be dropped when the channel is full so streaming and
// tool execution goroutines never block on UI throughput. A small set of
// low-frequency correctness/control events (tool lifecycle milestones,
// non-idle activity, session restore, draft consumption, fork reload, errors)
// are delivered reliably with blocking semantics guarded by stoppingCh. This
// is safe to call from any goroutine.
func (a *MainAgent) emitToTUI(evt AgentEvent) {
	if a.shuttingDown.Load() {
		return
	}
	switch e := evt.(type) {
	case StreamTextEvent:
		if strings.TrimSpace(e.AgentID) != "" && !a.shouldEmitSubAgentStreaming(e.AgentID) {
			return
		}
	case StreamThinkingDeltaEvent:
		if strings.TrimSpace(e.AgentID) != "" && !a.shouldEmitSubAgentStreaming(e.AgentID) {
			return
		}
	case StreamThinkingEvent:
		if strings.TrimSpace(e.AgentID) != "" && !a.shouldEmitSubAgentStreaming(e.AgentID) {
			return
		}
	}
	if warnMsg, warnAttrs, ok := reliableOutputEventLog(evt); ok {
		a.emitReliableToTUI(evt, warnMsg, warnAttrs...)
		return
	}
	select {
	case <-a.stoppingCh:
		return
	default:
	}
	a.outputMu.RLock()
	defer a.outputMu.RUnlock()
	if a.outputClosed.Load() {
		return
	}
	select {
	case a.outputCh <- evt:
	default:
		log.Warnf("TUI output channel full, dropping event event_type=%v", fmt.Sprintf("%T", evt))
	}
}

func (a *MainAgent) shouldEmitSubAgentStreaming(agentID string) bool {
	if strings.TrimSpace(agentID) == "" {
		return true
	}
	if focused := a.focusedAgent.Load(); focused != nil {
		return focused.instanceID == agentID
	}
	return false
}

// emitInteractiveToTUI sends an interactive event (ConfirmRequest / QuestionRequest)
// to the output channel with blocking semantics. Unlike emitToTUI, it will wait
// for space in the channel, but respects ctx cancellation and stoppingCh to avoid
// blocking forever during shutdown.
func (a *MainAgent) emitInteractiveToTUI(ctx context.Context, evt AgentEvent) error {
	select {
	case <-a.stoppingCh:
		return ErrAgentShutdown
	default:
	}
	a.outputMu.RLock()
	defer a.outputMu.RUnlock()
	if a.outputClosed.Load() {
		return ErrAgentShutdown
	}
	select {
	case a.outputCh <- evt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stoppingCh:
		return ErrAgentShutdown
	}
}
