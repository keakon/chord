package agent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// toolCallStageTrace tracks per-call timing markers from streaming args-end to
// finalized execution dispatch. Used for queue-latency diagnostics.
type toolCallStageTrace struct {
	CallID string
	Name   string
	Agent  string

	ToolUseEndAt           time.Time
	SpeculativeStartAt     time.Time
	FirstVisibleResultAt   time.Time
	CallLLMReturnedAt      time.Time
	OnAfterLLMCallDoneAt   time.Time
	LLMResponseEventSentAt time.Time
	LLMResponseHandledAt   time.Time
	ExecutionRunningAt     time.Time

	PersistBlockedTotal time.Duration
	PersistBlockedCount int
}

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

func (t *Turn) markToolCallCompleted(callID string) {
	if t == nil || callID == "" {
		return
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	if t.completedToolCallIDs == nil {
		t.completedToolCallIDs = make(map[string]struct{})
	}
	t.completedToolCallIDs[callID] = struct{}{}
	delete(t.PendingToolMeta, callID)
}

func (t *Turn) filterCompletedToolCalls(calls []PendingToolCall) []PendingToolCall {
	if t == nil || len(calls) == 0 {
		return calls
	}
	t.pendingToolMu.Lock()
	defer t.pendingToolMu.Unlock()
	if len(t.completedToolCallIDs) == 0 {
		return calls
	}
	out := make([]PendingToolCall, 0, len(calls))
	for _, call := range calls {
		if _, completed := t.completedToolCallIDs[call.CallID]; completed {
			continue
		}
		out = append(out, call)
	}
	return out
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
	// Streaming arguments accumulate fragment-by-fragment, so hygiene must
	// run here, on the finalized present, never on fragments: emoji ZWJ
	// preservation is position-sensitive across fragment boundaries, and these
	// runes could otherwise reach speculative or interrupted-turn execution.

	if original := call.ArgsJSON; original != "" {
		cleaned := tools.StripZeroWidthFormat(original)
		if cleaned != original {
			call.ArgsJSON = cleaned
			byField := map[string]map[rune]int{
				"tool_call_args": tools.CountStrippedInvisible(original, cleaned),
			}
			log.Warnf("sanitized zero-width format characters from streamed tool call args call_id=%v name=%v counts=%v", call.CallID, call.Name, formatInvisibleCounts(byField))
		}
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if t.streamingToolCalls == nil {
		t.streamingToolCalls = make(map[string]PendingToolCall)
	}
	t.recordStreamingToolCallLocked(call.CallID)
	t.streamingToolCalls[call.CallID] = call
}

func (t *Turn) recordStreamingToolCallLocked(callID string) {
	if t == nil || callID == "" {
		return
	}
	if _, exists := t.streamingToolCalls[callID]; !exists {
		t.streamingToolOrder = append(t.streamingToolOrder, callID)
	}
}

// appendStreamingToolCallInput appends one streamed tool-argument fragment to
// the speculative tool metadata. Providers deliver fragments (the delta's own
// bytes), so they accumulate in a per-call strings.Builder — amortized O(1) per
// fragment, with no string aliasing — and ArgsJSON materializes from it on
// demand (see materializeStreamingToolCallArgsLocked).
func (t *Turn) appendStreamingToolCallInput(callID, name, fragment, agentID string) {
	if t == nil || callID == "" || fragment == "" {
		return
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if t.streamingToolCalls == nil {
		t.streamingToolCalls = make(map[string]PendingToolCall)
	}
	t.recordStreamingToolCallLocked(callID)
	call := t.streamingToolCalls[callID]
	call.CallID = callID
	if call.Name == "" {
		call.Name = name
	}
	if call.AgentID == "" {
		call.AgentID = agentID
	}
	if call.argsFragBuf == nil {
		call.argsFragBuf = new(strings.Builder)
	}
	call.argsFragBuf.WriteString(fragment)
	t.streamingToolCalls[callID] = call
}

// streamToolArgsFlushInterval is the coalescing window for per-fragment
// ToolCallUpdateEvents. Each update carries the accumulated args, so flushing
// per fragment is quadratic in the args' size; the text flush cadence bounds
// updates to what the UI can usefully re-render.
const streamToolArgsFlushInterval = defaultStreamTextFlushInterval

// streamingArgsFlushDue reports whether a TUI args update for callID should
// flush now, recording the flush when it is due. The first fragment of a call
// always flushes.
func (t *Turn) streamingArgsFlushDue(callID string, now time.Time) bool {
	if t == nil || callID == "" {
		return false
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if last, ok := t.streamingToolEmitAt[callID]; ok && now.Sub(last) < streamToolArgsFlushInterval {
		return false
	}
	if t.streamingToolEmitAt == nil {
		t.streamingToolEmitAt = make(map[string]time.Time)
	}
	t.streamingToolEmitAt[callID] = now
	return true
}

// canonicalApplyPatchArgsJSON builds the canonical {"patch": ...} args object
// for raw freeform patch text, so a custom-tool call is indistinguishable from
// a JSON function call to tool execution, hooks, permissions and audits.
func canonicalApplyPatchArgsJSON(text string) string {
	canonical, err := json.Marshal(map[string]string{"patch": text})
	if err != nil {
		return `{"patch":""}`
	}
	return string(canonical)
}

// materializeStreamingToolCallArgsLocked rebuilds the canonical {patch}
// envelope from InputText when a freeform fragment has invalidated it,
// materializes InputText from its fragment builder, and materializes ArgsJSON
// from the JSON fragment builder as an owned copy. Callers must hold
// streamingToolMu.
//
// The envelope rebuild is deferred (rather than run per fragment) because it
// costs O(len(patch)) each time, which is O(patch²) across a streamed
// patch — and no consumer reads the envelope until the arguments are complete.
// The clones are equally load-bearing: a builder's buffer keeps growing on the
// streaming goroutine, so handing out its alias would mutate the string under
// concurrent readers (TUI previews, speculative validation).
func materializeStreamingToolCallArgsLocked(call *PendingToolCall) {
	materializeStreamingToolCallInputTextLocked(call)
	if call.inputArgsStale {
		call.ArgsJSON = canonicalApplyPatchArgsJSON(call.InputText)
		call.inputArgsStale = false
		return
	}
	if call.argsFragBuf != nil && call.argsFragBuf.Len() != call.argsLenAtMaterialize {
		call.ArgsJSON = strings.Clone(call.argsFragBuf.String())
		call.argsLenAtMaterialize = call.argsFragBuf.Len()
	}
}

// appendStreamingToolCallInputText appends one freeform text fragment
// (ToolCallDelta.InputText, e.g. Responses custom apply_patch deltas). Fragments
// accumulate in a per-call strings.Builder — amortized O(1) per fragment instead
// of the string concatenation that rebuilt the whole stored InputText on every
// fragment — and InputText materializes on demand as an owned copy (see
// materializeStreamingToolCallInputTextLocked). The canonical {patch} args
// object is rebuilt lazily too (materializeStreamingToolCallArgsLocked) because
// re-serializing the whole patch on every fragment is quadratic in the patch
// size. Every reader of the stored call observes a materialized, valid JSON
// ArgsJSON, so speculative validation and finalize are unaffected.
func (t *Turn) appendStreamingToolCallInputText(callID, name, fragment, agentID string) {
	if t == nil || callID == "" || fragment == "" {
		return
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if t.streamingToolCalls == nil {
		t.streamingToolCalls = make(map[string]PendingToolCall)
	}
	t.recordStreamingToolCallLocked(callID)
	call := t.streamingToolCalls[callID]
	call.CallID = callID
	if call.Name == "" {
		call.Name = name
	}
	if call.AgentID == "" {
		call.AgentID = agentID
	}
	if call.inputTextBuf == nil {
		call.inputTextBuf = new(strings.Builder)
	}
	call.inputTextBuf.WriteString(fragment)
	call.inputArgsStale = true
	t.streamingToolCalls[callID] = call
}

// materializeStreamingToolCallInputTextLocked materializes InputText from the
// freeform fragment builder as an owned copy. Callers must hold streamingToolMu.
func materializeStreamingToolCallInputTextLocked(call *PendingToolCall) string {
	if call.inputTextBuf != nil && call.inputTextBuf.Len() != call.inputTextLenAtMaterialize {
		call.InputText = strings.Clone(call.inputTextBuf.String())
		call.inputTextLenAtMaterialize = call.inputTextBuf.Len()
	}
	return call.InputText
}

// streamingToolCallInputText materializes and returns the accumulated freeform
// text for callID. It deliberately skips the canonical {patch} envelope rebuild:
// the streaming preview reads the raw text, and the envelope is the O(patch)
// half that only the finalized call consumes.
func (t *Turn) streamingToolCallInputText(callID string) (string, bool) {
	if t == nil || callID == "" {
		return "", false
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	call, ok := t.streamingToolCalls[callID]
	if !ok {
		return "", false
	}
	materializeStreamingToolCallInputTextLocked(&call)
	t.streamingToolCalls[callID] = call
	return call.InputText, true
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
	for _, callID := range t.streamingToolOrder {
		if c, ok := t.streamingToolCalls[callID]; ok {
			materializeStreamingToolCallArgsLocked(&c)
			out = append(out, c)
		}
	}
	t.streamingToolCalls = nil
	t.streamingToolOrder = nil
	t.streamingToolEmitAt = nil
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
	delete(t.streamingToolEmitAt, callID)
	for i, id := range t.streamingToolOrder {
		if id == callID {
			t.streamingToolOrder = append(t.streamingToolOrder[:i], t.streamingToolOrder[i+1:]...)
			break
		}
	}
	if len(t.streamingToolCalls) == 0 {
		t.streamingToolOrder = nil
	}
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
	materializeStreamingToolCallArgsLocked(&call)
	t.streamingToolCalls[callID] = call
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
	for _, callID := range t.streamingToolOrder {
		if c, ok := t.streamingToolCalls[callID]; ok {
			materializeStreamingToolCallArgsLocked(&c)
			t.streamingToolCalls[callID] = c
			out = append(out, c)
		}
	}
	return out
}

func (t *Turn) streamingToolCallsBefore(callID string) []PendingToolCall {
	if t == nil || callID == "" {
		return nil
	}
	t.streamingToolMu.Lock()
	defer t.streamingToolMu.Unlock()
	if len(t.streamingToolCalls) == 0 || len(t.streamingToolOrder) == 0 {
		return nil
	}
	out := make([]PendingToolCall, 0, len(t.streamingToolOrder))
	for _, id := range t.streamingToolOrder {
		if id == callID {
			break
		}
		if c, ok := t.streamingToolCalls[id]; ok {
			materializeStreamingToolCallArgsLocked(&c)
			t.streamingToolCalls[id] = c
			out = append(out, c)
		}
	}
	return out
}

// streamTurnID returns t.ID for tagging streamed deltas, tolerating a nil turn
// from paths that build a stream reducer before the turn exists. Zero means
// "unknown" to consumers, which keep the legacy unattributed behavior.
func streamTurnID(t *Turn) uint64 {
	if t == nil {
		return 0
	}
	return t.ID
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

// noteProducingModelRef records the model that confirmed visible output for the
// current streaming round. It is deliberately separate from the sidebar
// identity: an interrupted partial reply must stay attributed to the model that
// actually wrote it, even after the sidebar realigns to the sticky cursor.
func (t *Turn) noteProducingModelRef(ref string) {
	if t == nil {
		return
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	t.partialTextMu.Lock()
	defer t.partialTextMu.Unlock()
	t.partialProducingRef = ref
}

// producingModelRef returns the confirmed producer of the accumulated partial
// text, or "" when no attempt has emitted visible output yet.
func (t *Turn) producingModelRef() string {
	if t == nil {
		return ""
	}
	t.partialTextMu.Lock()
	defer t.partialTextMu.Unlock()
	return strings.TrimSpace(t.partialProducingRef)
}

// peekPartialText returns the accumulated partial assistant text without
// clearing it. Callers use it to decide how to recover from a failed request
// before the recovery path drains the same text.
func (t *Turn) peekPartialText() string {
	if t == nil {
		return ""
	}
	t.partialTextMu.Lock()
	defer t.partialTextMu.Unlock()
	return t.partialText.String()
}

// drainPartialText returns and clears the accumulated partial assistant text.
// The confirmed producer is dropped with it: the text it described is gone.
func (t *Turn) drainPartialText() string {
	if t == nil {
		return ""
	}
	t.partialTextMu.Lock()
	defer t.partialTextMu.Unlock()
	s := t.partialText.String()
	t.partialText.Reset()
	t.partialProducingRef = ""
	return s
}

// appendPartialResponsesOutput adds a finalized reasoning output item to the
// turn's accumulator. It mirrors appendPartialText so an interrupted stream can
// persist the reasoning that was already finalized before the interruption.
func (t *Turn) appendPartialResponsesOutput(item message.ResponsesOutputItem) {
	if t == nil {
		return
	}
	t.partialResponsesOutputMu.Lock()
	defer t.partialResponsesOutputMu.Unlock()
	t.partialResponsesOutput = append(t.partialResponsesOutput, item)
}

// drainPartialResponsesOutput returns and clears the accumulated finalized
// reasoning items. Callers that persist an interrupted assistant message pass
// the result to interruptedAssistantResponsesOutput so the reasoning pairs
// with the message; callers on the success/rollback/discard paths ignore the
// return to drop reasoning that the completed response already carries or that
// belongs to an abandoned attempt.
func (t *Turn) drainPartialResponsesOutput() []message.ResponsesOutputItem {
	if t == nil {
		return nil
	}
	t.partialResponsesOutputMu.Lock()
	defer t.partialResponsesOutputMu.Unlock()
	out := t.partialResponsesOutput
	t.partialResponsesOutput = nil
	return out
}

// interruptedAssistantResponsesOutput builds the ResponsesOutput that must
// accompany a partial-text interrupted assistant message: the finalized
// reasoning items (in provider order) followed by a message item carrying the
// partial text. The Responses API requires a message item to be preceded by its
// reasoning item; since replay replays msg.ResponsesOutput verbatim and ignores
// msg.Content when it is non-empty, the message must appear here too, paired
// with the reasoning. Tool calls are intentionally excluded (a dangling
// function_call without its output is an API error).
func interruptedAssistantResponsesOutput(reasoning []message.ResponsesOutputItem, text string) []message.ResponsesOutputItem {
	if len(reasoning) == 0 {
		// Nothing to pair the message with, so a native payload would only cost
		// replay headroom: any ResponsesOutput forces the request-level replay
		// floor up to synthesized for every later request in the session.
		return nil
	}
	out := make([]message.ResponsesOutputItem, 0, len(reasoning)+1)
	out = append(out, reasoning...)
	out = append(out, message.ResponsesOutputItem{
		Type: "message",
		Role: "assistant",
		Content: []message.ResponsesOutputContent{
			{Type: "output_text", Text: text},
		},
	})
	return out
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

func (a *MainAgent) recordToolTraceSpeculativeStart(callID, name string, at time.Time) {
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
	if trace.SpeculativeStartAt.IsZero() {
		trace.SpeculativeStartAt = at
	}
	// If we already have tool_use_end, log speculative start latency.
	if !trace.ToolUseEndAt.IsZero() {
		toolUseToStart := max(at.Sub(trace.ToolUseEndAt), 0)
		log.Debugf("streaming tool speculative start tool=%s call_id=%s agent_id=%s tool_use_end_to_speculative_start_ms=%d", trace.Name, trace.CallID, trace.Agent, toolUseToStart.Milliseconds())
	}
	a.toolTrace[callID] = trace
}

func (a *MainAgent) recordToolTraceFirstVisibleResult(callID, name string, at time.Time) {
	if a == nil || strings.TrimSpace(callID) == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	a.toolTraceMu.Lock()
	if a.toolTrace == nil {
		a.toolTrace = make(map[string]toolCallStageTrace)
	}
	trace := a.toolTrace[callID]
	trace.CallID = callID
	if strings.TrimSpace(name) != "" {
		trace.Name = strings.TrimSpace(name)
	}
	if trace.FirstVisibleResultAt.IsZero() {
		trace.FirstVisibleResultAt = at
	}
	// Compute metrics if we have a tool_use_end marker.
	if !trace.ToolUseEndAt.IsZero() {
		firstVisible := max(at.Sub(trace.ToolUseEndAt), 0)
		specStart := time.Duration(0)
		if !trace.SpeculativeStartAt.IsZero() {
			specStart = max(trace.SpeculativeStartAt.Sub(trace.ToolUseEndAt), 0)
		}
		attrs := []any{
			"tool", trace.Name,
			"call_id", trace.CallID,
			"agent_id", trace.Agent,
			"tool_use_end_to_first_visible_result_ms", firstVisible.Milliseconds(),
		}
		if specStart > 0 {
			attrs = append(attrs, "tool_use_end_to_speculative_start_ms", specStart.Milliseconds())
		}
		log.Infof("streaming tool first visible result attrs=%v", attrs)
	}
	// Avoid unbounded growth for promoted speculative calls: if we got a first-visible
	// marker before any finalized execution running marker, drop the trace now.
	if trace.ExecutionRunningAt.IsZero() {
		delete(a.toolTrace, callID)
		if len(a.toolTrace) == 0 {
			a.toolTrace = nil
		}
		a.toolTraceMu.Unlock()
		return
	}
	a.toolTrace[callID] = trace
	a.toolTraceMu.Unlock()
}

func (a *MainAgent) recordToolTraceSpeculativeDiscard(info StreamingToolDiscardInfo) {
	if a == nil || strings.TrimSpace(info.CallID) == "" {
		return
	}
	wasted := time.Duration(0)
	if info.Started && !info.CompletedAt.IsZero() && !info.StartedAt.IsZero() {
		wasted = max(info.CompletedAt.Sub(info.StartedAt), 0)
	}
	log.Debugf("streaming tool speculative discarded tool=%s call_id=%s reason=%s started=%v completed=%v wasted_ms=%d", info.Name, info.CallID, info.Reason, info.Started, info.Completed, wasted.Milliseconds())
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
		log.Warnf("tool queue latency trace attrs=%v", attrs)
	case totalQueuedLatency >= 200*time.Millisecond:
		log.Infof("tool queue latency trace attrs=%v", attrs)
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
// cards (tool_use_start/delta/end) that did not enter finalized context and asks
// the UI to remove them. It also clears any associated tool-latency traces.
func (a *MainAgent) discardSpeculativeStreamToolsAndClearToolTrace(t *Turn, reason string) {
	if a == nil || t == nil {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "turn_cancel"
	}
	if t.streamingToolExec != nil {
		discarded := t.streamingToolExec.DiscardAll(reason)
		logStreamingToolDiscard(reason, discarded)
	}
	spec := t.drainStreamingToolCalls()
	if len(spec) > 0 {
		emitToolCallDiscards(a.emitToTUI, spec, reason)
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
