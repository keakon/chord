package agent

import (
	"log/slog"
	"strings"
	"time"
)

func (t *Turn) recordPendingToolCall(call PendingToolCall) {
	if t == nil || call.CallID == "" {
		return
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	if t.PendingToolMeta == nil {
		t.PendingToolMeta = make(map[string]PendingToolCall)
	}
	t.PendingToolMeta[call.CallID] = call
}

func (t *Turn) resolvePendingToolCall(callID string) {
	if t == nil || callID == "" {
		return
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	delete(t.PendingToolMeta, callID)
}

func (t *Turn) updatePendingToolCall(call PendingToolCall) {
	if t == nil || call.CallID == "" {
		return
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	if t.PendingToolMeta == nil {
		return
	}
	existing, ok := t.PendingToolMeta[call.CallID]
	if !ok {
		return
	}
	if strings.TrimSpace(call.Name) != "" {
		existing.Name = call.Name
	}
	if strings.TrimSpace(call.ArgsJSON) != "" {
		existing.ArgsJSON = call.ArgsJSON
	}
	if strings.TrimSpace(call.AgentID) != "" {
		existing.AgentID = call.AgentID
	}
	if call.Audit != nil {
		existing.Audit = call.Audit.Clone()
	}
	t.PendingToolMeta[call.CallID] = existing
}

func (t *Turn) cancelPendingToolCalls() []PendingToolCall {
	if t == nil {
		return nil
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	if len(t.PendingToolMeta) == 0 {
		return nil
	}
	calls := make([]PendingToolCall, 0, len(t.PendingToolMeta))
	for _, call := range t.PendingToolMeta {
		calls = append(calls, call)
	}
	t.PendingToolMeta = nil
	return calls
}

func (t *Turn) recordStreamingToolCall(call PendingToolCall) {
	if t == nil || call.CallID == "" {
		return
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if t.streamingToolCalls == nil {
		t.streamingToolCalls = make(map[string]PendingToolCall)
	}
	t.streamingToolCalls[call.CallID] = call
}

// appendStreamingToolCallInput appends one streamed tool-argument fragment to the
// speculative tool metadata and returns the accumulated JSON string.
func (t *Turn) appendStreamingToolCallInput(callID, name, fragment, agentID string) string {
	if t == nil || callID == "" || fragment == "" {
		return ""
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if t.streamingToolCalls == nil {
		t.streamingToolCalls = make(map[string]PendingToolCall)
	}
	call := t.streamingToolCalls[callID]
	call.CallID = callID
	if call.Name == "" {
		call.Name = name
	}
	if call.AgentID == "" {
		call.AgentID = agentID
	}
	switch {
	case call.ArgsJSON == "":
		call.ArgsJSON = fragment
	case strings.HasPrefix(fragment, call.ArgsJSON):
		call.ArgsJSON = fragment
	case strings.HasPrefix(call.ArgsJSON, fragment):
	default:
		call.ArgsJSON += fragment
	}
	t.streamingToolCalls[callID] = call
	return call.ArgsJSON
}

// drainStreamingToolCalls removes and returns all speculative streaming tool
// metadata. Safe to call when the LLM round is finalized or abandoned.
func (t *Turn) drainStreamingToolCalls() []PendingToolCall {
	if t == nil {
		return nil
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if len(t.streamingToolCalls) == 0 {
		return nil
	}
	out := make([]PendingToolCall, 0, len(t.streamingToolCalls))
	for _, c := range t.streamingToolCalls {
		out = append(out, c)
	}
	t.streamingToolCalls = nil
	return out
}

func (t *Turn) removeStreamingToolCall(callID string) {
	if t == nil || callID == "" {
		return
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if len(t.streamingToolCalls) == 0 {
		return
	}
	delete(t.streamingToolCalls, callID)
}

func (t *Turn) getStreamingToolCall(callID string) (PendingToolCall, bool) {
	if t == nil || callID == "" {
		return PendingToolCall{}, false
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if len(t.streamingToolCalls) == 0 {
		return PendingToolCall{}, false
	}
	call, ok := t.streamingToolCalls[callID]
	if !ok {
		return PendingToolCall{}, false
	}
	return call, true
}

func (t *Turn) snapshotStreamingToolCalls() []PendingToolCall {
	if t == nil {
		return nil
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if len(t.streamingToolCalls) == 0 {
		return nil
	}
	out := make([]PendingToolCall, 0, len(t.streamingToolCalls))
	for _, c := range t.streamingToolCalls {
		out = append(out, c)
	}
	return out
}

// appendPartialText adds streamed assistant text to the turn's accumulator.
func (t *Turn) appendPartialText(s string) {
	if t == nil || s == "" {
		return
	}
	t.partialTextMu.Lock()
	defer t.partialTextMu.Unlock()
	t.partialText.WriteString(s)
}

// drainPartialText returns and clears the accumulated partial assistant text.
func (t *Turn) drainPartialText() string {
	if t == nil {
		return ""
	}
	t.partialTextMu.Lock()
	defer t.partialTextMu.Unlock()
	s := t.partialText.String()
	t.partialText.Reset()
	return s
}

func (a *MainAgent) recordToolTraceToolUseEnd(callID, name, agentID string, at time.Time) {
	if a == nil || strings.TrimSpace(callID) == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	a.toolTraceMu.Lock()
	defer a.toolTraceMu.Unlock()
	if a.toolTrace == nil {
		a.toolTrace = make(map[string]toolCallStageTrace)
	}
	trace := a.toolTrace[callID]
	trace.CallID = callID
	if strings.TrimSpace(name) != "" {
		trace.Name = strings.TrimSpace(name)
	}
	if strings.TrimSpace(agentID) != "" {
		trace.Agent = strings.TrimSpace(agentID)
	}
	if trace.ToolUseEndAt.IsZero() {
		trace.ToolUseEndAt = at
	}
	a.toolTrace[callID] = trace
}

func (a *MainAgent) recordTurnStreamingToolUseEnd(turn *Turn, at time.Time) {
	if a == nil || turn == nil {
		return
	}
	calls := turn.snapshotStreamingToolCalls()
	if len(calls) == 0 {
		return
	}
	for _, c := range calls {
		a.recordToolTraceToolUseEnd(c.CallID, c.Name, c.AgentID, at)
	}
}

func (a *MainAgent) recordToolTraceCallLLMReturned(turn *Turn, at time.Time) {
	if a == nil || turn == nil {
		return
	}
	calls := turn.snapshotStreamingToolCalls()
	if len(calls) == 0 {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	a.toolTraceMu.Lock()
	defer a.toolTraceMu.Unlock()
	if len(a.toolTrace) == 0 {
		return
	}
	for _, c := range calls {
		id := strings.TrimSpace(c.CallID)
		if id == "" {
			continue
		}
		trace, ok := a.toolTrace[id]
		if !ok {
			continue
		}
		if trace.CallLLMReturnedAt.IsZero() {
			trace.CallLLMReturnedAt = at
			a.toolTrace[id] = trace
		}
	}
}

func (a *MainAgent) recordToolTraceLLMResponseEventSent(payload *LLMResponsePayload, at time.Time) {
	if a == nil || payload == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	if len(payload.ToolCalls) == 0 {
		return
	}
	a.toolTraceMu.Lock()
	defer a.toolTraceMu.Unlock()
	if len(a.toolTrace) == 0 {
		return
	}
	for _, tc := range payload.ToolCalls {
		callID := strings.TrimSpace(tc.ID)
		if callID == "" {
			continue
		}
		trace, ok := a.toolTrace[callID]
		if !ok {
			continue
		}
		if strings.TrimSpace(tc.Name) != "" {
			trace.Name = strings.TrimSpace(tc.Name)
		}
		if trace.LLMResponseEventSentAt.IsZero() {
			trace.LLMResponseEventSentAt = at
		}
		a.toolTrace[callID] = trace
	}
}

func (a *MainAgent) recordToolTraceLLMResponseHandled(payload *LLMResponsePayload, at time.Time) {
	if a == nil || payload == nil {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	if len(payload.ToolCalls) == 0 {
		return
	}
	a.toolTraceMu.Lock()
	defer a.toolTraceMu.Unlock()
	if len(a.toolTrace) == 0 {
		return
	}
	for _, tc := range payload.ToolCalls {
		callID := strings.TrimSpace(tc.ID)
		if callID == "" {
			continue
		}
		trace, ok := a.toolTrace[callID]
		if !ok {
			continue
		}
		if strings.TrimSpace(tc.Name) != "" {
			trace.Name = strings.TrimSpace(tc.Name)
		}
		if trace.LLMResponseHandledAt.IsZero() {
			trace.LLMResponseHandledAt = at
		}
		a.toolTrace[callID] = trace
	}
}

func (a *MainAgent) recordToolTracePersistBlock(callID string, d time.Duration) {
	if a == nil || strings.TrimSpace(callID) == "" {
		return
	}
	if d <= 0 {
		return
	}
	a.toolTraceMu.Lock()
	defer a.toolTraceMu.Unlock()
	if len(a.toolTrace) == 0 {
		return
	}
	trace, ok := a.toolTrace[callID]
	if !ok {
		return
	}
	trace.PersistBlockedTotal += d
	trace.PersistBlockedCount++
	a.toolTrace[callID] = trace
}

func (a *MainAgent) recordToolTraceOnAfterLLMCallDone(callID string, at time.Time) {
	if a == nil || strings.TrimSpace(callID) == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	a.toolTraceMu.Lock()
	defer a.toolTraceMu.Unlock()
	if len(a.toolTrace) == 0 {
		return
	}
	trace, ok := a.toolTrace[callID]
	if !ok {
		return
	}
	trace.OnAfterLLMCallDoneAt = at
	a.toolTrace[callID] = trace
}

func (a *MainAgent) logToolTraceExecutionRunning(call PendingToolCall, at time.Time) {
	if a == nil || strings.TrimSpace(call.CallID) == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	cleanup := func() {
		a.toolTraceMu.Lock()
		if a.toolTrace != nil {
			delete(a.toolTrace, call.CallID)
			if len(a.toolTrace) == 0 {
				a.toolTrace = nil
			}
		}
		a.toolTraceMu.Unlock()
	}

	a.toolTraceMu.Lock()
	if a.toolTrace == nil {
		a.toolTrace = make(map[string]toolCallStageTrace)
	}
	trace := a.toolTrace[call.CallID]
	trace.CallID = call.CallID
	if strings.TrimSpace(call.Name) != "" {
		trace.Name = strings.TrimSpace(call.Name)
	}
	if strings.TrimSpace(call.AgentID) != "" {
		trace.Agent = strings.TrimSpace(call.AgentID)
	}
	if trace.ExecutionRunningAt.IsZero() {
		trace.ExecutionRunningAt = at
	}
	a.toolTrace[call.CallID] = trace
	a.toolTraceMu.Unlock()

	// No args-end marker → no useful queue-latency signal. Drop the trace to
	// avoid unbounded growth.
	if trace.ToolUseEndAt.IsZero() {
		cleanup()
		return
	}

	safeMillis := func(d time.Duration) int64 {
		if d < 0 {
			return 0
		}
		return d.Milliseconds()
	}
	var (
		llmFinalizeDelay   time.Duration
		eventQueueDelay    time.Duration
		handleToRunning    time.Duration
		totalQueuedLatency = at.Sub(trace.ToolUseEndAt)
	)
	if !trace.CallLLMReturnedAt.IsZero() {
		llmFinalizeDelay = trace.CallLLMReturnedAt.Sub(trace.ToolUseEndAt)
	}
	var onAfterLLMCallDelay time.Duration
	if !trace.CallLLMReturnedAt.IsZero() && !trace.OnAfterLLMCallDoneAt.IsZero() {
		onAfterLLMCallDelay = trace.OnAfterLLMCallDoneAt.Sub(trace.CallLLMReturnedAt)
	}
	if !trace.LLMResponseEventSentAt.IsZero() && !trace.LLMResponseHandledAt.IsZero() {
		eventQueueDelay = trace.LLMResponseHandledAt.Sub(trace.LLMResponseEventSentAt)
	}
	if !trace.LLMResponseHandledAt.IsZero() {
		handleToRunning = at.Sub(trace.LLMResponseHandledAt)
	}
	attrs := []any{
		"tool", trace.Name,
		"call_id", trace.CallID,
		"agent_id", trace.Agent,
		"tool_use_end_to_running_ms", safeMillis(totalQueuedLatency),
		"tool_use_end_to_callllm_return_ms", safeMillis(llmFinalizeDelay),
		"callllm_return_to_on_after_llm_call_done_ms", safeMillis(onAfterLLMCallDelay),
		"llm_response_event_queue_wait_ms", safeMillis(eventQueueDelay),
		"llm_response_handle_to_running_ms", safeMillis(handleToRunning),
		"persist_block_ms", safeMillis(trace.PersistBlockedTotal),
		"persist_block_count", trace.PersistBlockedCount,
	}
	switch {
	case totalQueuedLatency >= time.Second:
		slog.Warn("tool queue latency trace", attrs...)
	case totalQueuedLatency >= 200*time.Millisecond:
		slog.Info("tool queue latency trace", attrs...)
	}
	cleanup()
}

func (a *MainAgent) clearToolTraceForCalls(calls []PendingToolCall) {
	if a == nil || len(calls) == 0 {
		return
	}
	a.toolTraceMu.Lock()
	defer a.toolTraceMu.Unlock()
	if len(a.toolTrace) == 0 {
		return
	}
	for _, c := range calls {
		id := strings.TrimSpace(c.CallID)
		if id == "" {
			continue
		}
		delete(a.toolTrace, id)
	}
	if len(a.toolTrace) == 0 {
		a.toolTrace = nil
	}
}

// discardSpeculativeStreamToolsAndClearToolTrace drains speculative streaming tool
// cards (tool_use_start/delta/end) and emits cancelled results so the UI doesn't
// keep "pending" cards forever. It also clears any associated tool-latency traces.
func (a *MainAgent) discardSpeculativeStreamToolsAndClearToolTrace(t *Turn) {
	if a == nil || t == nil {
		return
	}
	spec := t.drainStreamingToolCalls()
	if len(spec) > 0 {
		emitCancelledToolResults(a.emitToTUI, spec)
		a.clearToolTraceForCalls(spec)
	}
}

// mergePendingToolCalls merges two slices, deduplicating by CallID (a wins over b).
func mergePendingToolCalls(a, b []PendingToolCall) []PendingToolCall {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := make(map[string]PendingToolCall, len(a)+len(b))
	for _, c := range a {
		if c.CallID != "" {
			seen[c.CallID] = c
		}
	}
	for _, c := range b {
		if c.CallID == "" {
			continue
		}
		if _, ok := seen[c.CallID]; !ok {
			seen[c.CallID] = c
		}
	}
	out := make([]PendingToolCall, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	return out
}
