package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/hook"
)

const outputDropLogMinInterval = 2 * time.Second

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
		evt, err := a.nextEvent(ctx)
		if err != nil {
			return err
		}
		a.dispatch(evt)
	}
}

func (a *MainAgent) nextEvent(ctx context.Context) (Event, error) {
	for {
		if evt, ok := a.popQueuedEvent(); ok {
			return evt, nil
		}
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-a.parentCtx.Done():
			return Event{}, a.parentCtx.Err()
		case evt := <-a.eventCh:
			a.signalEventSpace()
			return evt, nil
		case <-a.eventWakeCh:
			continue
		}
	}
}

// dispatch routes an internal event to the appropriate handler. It runs on the
// single event-loop goroutine, so handlers do not need to synchronise access to
// turn state.
func (a *MainAgent) dispatch(evt Event) {
	defer a.emitGlobalIdleIfReady()
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
	case EventModelPoolSwitch:
		a.handleModelPoolSwitchEvent(evt)
	case EventMCPControl:
		a.handleMCPControlEvent(evt)
	case EventMCPControlDone:
		a.handleMCPControlDoneEvent(evt)
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
	case EventSubAgentRequestBoundary:
		a.handleSubAgentRequestBoundary(evt)
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

// sendEvent queues events from asynchronous producers. Once the primary
// channel and bounded overflow are full, producers wait for capacity or
// shutdown. Main-loop handlers must use queueLoopEvent so they never wait on
// capacity that only the loop itself can release.
func (a *MainAgent) sendEvent(evt Event) {
	for {
		select {
		case <-a.stoppingCh:
			return
		default:
		}
		a.eventMu.Lock()
		if len(a.deferredEvents) == 0 && len(a.loopEvents) == 0 {
			if len(a.eventCh) < cap(a.eventCh) {
				evt = a.sequenceEvent(evt)
				a.eventCh <- evt
				a.eventMu.Unlock()
				return
			}
		}
		if a.coalesceQueuedEventLocked(a.deferredEvents, evt) {
			a.eventCoalesced.Add(1)
			a.eventMu.Unlock()
			return
		}
		if len(a.deferredEvents) < a.eventOverflowLimit {
			evt = a.sequenceEvent(evt)
			a.deferredEvents = append(a.deferredEvents, evt)
			a.updateEventOverflowPeakLocked()
			a.eventMu.Unlock()
			a.wakeDeferredEvents()
			return
		}
		a.eventBackpressure.Add(1)
		a.eventMu.Unlock()
		select {
		case <-a.eventSpaceCh:
		case <-a.stoppingCh:
			return
		}
	}
}

func (a *MainAgent) queueLoopEvent(evt Event) {
	if !a.started.Load() {
		a.sendEvent(evt)
		return
	}
	a.eventMu.Lock()
	if a.coalesceQueuedEventLocked(a.loopEvents, evt) {
		a.eventCoalesced.Add(1)
		a.eventMu.Unlock()
		return
	}
	if len(a.loopEvents) >= a.loopEventLimit {
		if idx := firstCoalescibleEventIndex(a.loopEvents); idx >= 0 {
			a.loopEvents = append(a.loopEvents[:idx], a.loopEvents[idx+1:]...)
			a.eventCoalesced.Add(1)
		} else {
			// Loop-owned causal follow-ups cannot block waiting for capacity that
			// only this goroutine can release, and dropping one would corrupt tool
			// or mailbox ordering. Preserve it and surface the invariant breach.
			log.Errorf("main-loop follow-up event reserve exceeded limit=%v type=%v", a.loopEventLimit, evt.Type)
		}
	}
	evt = a.sequenceEvent(evt)
	a.loopEvents = append(a.loopEvents, evt)
	a.updateEventOverflowPeakLocked()
	a.eventMu.Unlock()
	a.wakeDeferredEvents()
}

func firstCoalescibleEventIndex(queue []Event) int {
	for i := range queue {
		if coalescibleEventKey(queue[i]) != "" {
			return i
		}
	}
	return -1
}

func (a *MainAgent) sequenceEvent(evt Event) Event {
	if evt.Seq == 0 {
		evt.Seq = a.eventSeq.Add(1)
	}
	return evt
}

func (a *MainAgent) wakeDeferredEvents() {
	select {
	case a.eventWakeCh <- struct{}{}:
	default:
	}
}

func (a *MainAgent) popQueuedEvent() (Event, bool) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	select {
	case evt := <-a.eventCh:
		a.signalEventSpaceLocked()
		return evt, true
	default:
	}
	if len(a.deferredEvents) == 0 && len(a.loopEvents) == 0 {
		return Event{}, false
	}
	useLoop := len(a.deferredEvents) == 0 || len(a.loopEvents) > 0 && a.loopEvents[0].Seq < a.deferredEvents[0].Seq
	var evt Event
	if useLoop {
		evt = a.loopEvents[0]
		a.loopEvents[0] = Event{}
		a.loopEvents = a.loopEvents[1:]
	} else {
		evt = a.deferredEvents[0]
		a.deferredEvents[0] = Event{}
		a.deferredEvents = a.deferredEvents[1:]
		a.signalEventSpaceLocked()
	}
	if len(a.deferredEvents) > 0 || len(a.loopEvents) > 0 {
		a.wakeDeferredEvents()
	}
	return evt, true
}

func (a *MainAgent) hasDeferredEvents() bool {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	return len(a.deferredEvents) > 0 || len(a.loopEvents) > 0
}

func (a *MainAgent) signalEventSpace() {
	select {
	case a.eventSpaceCh <- struct{}{}:
	default:
	}
}

func (a *MainAgent) signalEventSpaceLocked() { a.signalEventSpace() }

func (a *MainAgent) updateEventOverflowPeakLocked() {
	depth := uint64(len(a.deferredEvents) + len(a.loopEvents))
	for {
		peak := a.eventOverflowPeak.Load()
		if depth <= peak || a.eventOverflowPeak.CompareAndSwap(peak, depth) {
			return
		}
	}
}

func (a *MainAgent) coalesceQueuedEventLocked(queue []Event, evt Event) bool {
	key := coalescibleEventKey(evt)
	if key == "" {
		return false
	}
	for i := len(queue) - 1; i >= 0; i-- {
		if coalescibleEventKey(queue[i]) == key {
			copy(queue[i:], queue[i+1:])
			queue[len(queue)-1] = a.sequenceEvent(evt)
			return true
		}
	}
	return false
}

func coalescibleEventKey(evt Event) string {
	switch evt.Type {
	case EventSubAgentProgressUpdated:
		return evt.Type + "\x00" + evt.SourceID
	default:
		return ""
	}
}

type EventQueueStats struct {
	OverflowCurrent uint64
	OverflowPeak    uint64
	Coalesced       uint64
	Backpressure    uint64
}

func (a *MainAgent) EventQueueStats() EventQueueStats {
	a.eventMu.Lock()
	current := len(a.deferredEvents) + len(a.loopEvents)
	a.eventMu.Unlock()
	return EventQueueStats{
		OverflowCurrent: uint64(current),
		OverflowPeak:    a.eventOverflowPeak.Load(),
		Coalesced:       a.eventCoalesced.Load(),
		Backpressure:    a.eventBackpressure.Load(),
	}
}

func (a *MainAgent) emitGlobalIdleIfReady() bool {
	a.drainRunnableMailboxWork()
	if a.currentTurn() != nil || a.loopKeepsMainBusy() || a.hasActiveSubAgentWork() || a.hasQueuedAutomaticWork() {
		a.globalIdle.Store(false)
		return false
	}
	a.parkQuiescentSubAgents()
	if !a.globalIdle.CompareAndSwap(false, true) {
		return false
	}
	a.emitInteractiveToTUI(a.parentCtx, GlobalIdleEvent{})
	a.fireHookBackground(a.parentCtx, hook.OnIdle, a.lastIdleTurnID.Load(), map[string]any{})
	return true
}

func (a *MainAgent) drainRunnableMailboxWork() {
	if a.mailboxDeliveryPaused.Load() {
		return
	}
	if a.currentTurn() == nil {
		a.drainSubAgentInbox()
	}
	ownerIDs := make([]string, 0, len(a.ownedSubAgentMailboxes))
	seenOwners := make(map[string]struct{}, len(a.ownedSubAgentMailboxes)+len(a.ownedMailboxSpool))
	for ownerID, queued := range a.ownedSubAgentMailboxes {
		for _, msg := range queued {
			if msg.Kind != SubAgentMailboxKindProgress {
				ownerIDs = append(ownerIDs, ownerID)
				seenOwners[ownerID] = struct{}{}
				break
			}
		}
	}
	for ownerID, queued := range a.ownedMailboxSpool {
		if len(queued) == 0 {
			continue
		}
		if _, seen := seenOwners[ownerID]; !seen {
			ownerIDs = append(ownerIDs, ownerID)
		}
	}
	for _, ownerID := range ownerIDs {
		a.drainOwnedSubAgentMailboxes(ownerID)
	}
}

func (a *MainAgent) hasQueuedAutomaticWork() bool {
	return len(a.eventCh) > 0 ||
		a.hasDeferredEvents() ||
		len(a.pendingUserMessages) > 0 ||
		a.hasRunnableMailboxWork() ||
		strings.TrimSpace(a.pendingRecoveryPrompt) != "" ||
		strings.TrimSpace(a.pendingAutoContinuePrompt) != "" ||
		strings.TrimSpace(a.pendingAutoContinueReplayPrompt) != "" ||
		a.pendingCompactionResume != nil ||
		a.IsCompactionRunning() ||
		a.mcpTransitionActive.Load()
}

func (a *MainAgent) hasRunnableMailboxWork() bool {
	if a.mailboxDeliveryPaused.Load() {
		return false
	}
	if len(a.subAgentInbox.urgent) > 0 || len(a.subAgentInbox.normal) > 0 ||
		len(a.pendingSubAgentMailboxes) > 0 || len(a.activeSubAgentMailboxes) > 0 ||
		a.activeSubAgentMailbox != nil {
		return true
	}
	for _, queued := range a.ownedSubAgentMailboxes {
		for _, msg := range queued {
			if msg.Kind != SubAgentMailboxKindProgress {
				return true
			}
		}
	}
	for _, queued := range a.ownedMailboxSpool {
		if len(queued) > 0 {
			return true
		}
	}
	return false
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
	case ToolCallStartEvent, ToolCallDiscardEvent, ToolCallExecutionEvent, ToolResultEvent, SessionRestoredEvent, SessionTitleChangedEvent, PendingDraftConsumedEvent, ForkSessionEvent, ErrorEvent, AgentStatusEvent, AgentStartedEvent, AgentNotifyEvent, AgentDoneEvent, GlobalIdleEvent, InfoEvent, ToastEvent, AssistantMessageEvent, LoopNoticeEvent, LoopStateChangedEvent, YoloModeChangedEvent:
		return "TUI output channel full, waiting to deliver critical event", []any{
			"event_type", fmt.Sprintf("%T", evt),
		}, true
	default:
		return "", nil, false
	}
}

func outputEventType(evt AgentEvent) string {
	if evt == nil {
		return "<nil>"
	}
	return reflect.TypeOf(evt).String()
}

func (a *MainAgent) shouldLogDroppedOutputEvent(eventType string, now time.Time) (bool, int) {
	a.outputDropLogMu.Lock()
	defer a.outputDropLogMu.Unlock()
	if a.outputDropLogLastByType == nil {
		a.outputDropLogLastByType = make(map[string]time.Time)
	}
	if a.outputDropLogSuppressedByType == nil {
		a.outputDropLogSuppressedByType = make(map[string]int)
	}
	last := a.outputDropLogLastByType[eventType]
	if last.IsZero() || now.Sub(last) >= outputDropLogMinInterval {
		suppressed := a.outputDropLogSuppressedByType[eventType]
		a.outputDropLogSuppressedByType[eventType] = 0
		a.outputDropLogLastByType[eventType] = now
		return true, suppressed
	}
	a.outputDropLogSuppressedByType[eventType]++
	return false, 0
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
		eventType := outputEventType(evt)
		if shouldLog, suppressed := a.shouldLogDroppedOutputEvent(eventType, time.Now()); shouldLog {
			if suppressed > 0 {
				log.Warnf("TUI output channel full, dropping event event_type=%v suppressed_since_last=%v", eventType, suppressed)
			} else {
				log.Warnf("TUI output channel full, dropping event event_type=%v", eventType)
			}
		}
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
