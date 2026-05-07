package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent/agentdiff"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

const streamingToolSpeculativeLimit = 4

type streamingToolState string

const (
	streamingToolQueued    streamingToolState = "queued"
	streamingToolExecuting streamingToolState = "executing"
	streamingToolCompleted streamingToolState = "completed"
	streamingToolYielded   streamingToolState = "yielded"
	streamingToolDiscarded streamingToolState = "discarded"
)

type streamingToolEntry struct {
	call        message.ToolCall
	argsHash    string
	state       streamingToolState
	startedAt   time.Time
	completedAt time.Time
	result      ToolExecutionResult
	err         error
	done        chan struct{}
	discarded   bool
	discardWhy  string
}

type StreamingToolDiscardInfo struct {
	CallID      string
	Name        string
	ArgsJSON    string
	Reason      string
	Started     bool
	Completed   bool
	StartedAt   time.Time
	CompletedAt time.Time
}

type StreamingToolExecutor struct {
	turnID  uint64
	ctx     context.Context
	execute func(context.Context, message.ToolCall) (ToolExecutionResult, error)
	emit    func(AgentEvent)

	onSpeculativeStart     func(callID, toolName string, at time.Time)
	onFirstVisibleResult   func(callID, toolName string, at time.Time)
	onSpeculativeDiscarded func(info StreamingToolDiscardInfo)

	mu       sync.Mutex
	limit    int
	running  int
	entries  map[string]*streamingToolEntry
	deferred []*streamingToolEntry
}

func NewStreamingToolExecutor(turnID uint64, ctx context.Context, emit func(AgentEvent), execute func(context.Context, message.ToolCall) (ToolExecutionResult, error)) *StreamingToolExecutor {
	return &StreamingToolExecutor{turnID: turnID, ctx: ctx, emit: emit, execute: execute, limit: streamingToolSpeculativeLimit, entries: make(map[string]*streamingToolEntry)}
}

func (e *StreamingToolExecutor) SetTraceCallbacks(onStart func(callID, toolName string, at time.Time), onFirstVisible func(callID, toolName string, at time.Time), onDiscard func(info StreamingToolDiscardInfo)) {
	if e == nil {
		return
	}
	e.onSpeculativeStart = onStart
	e.onFirstVisibleResult = onFirstVisible
	e.onSpeculativeDiscarded = onDiscard
}

func (e *StreamingToolExecutor) Start(call message.ToolCall) bool {
	if e == nil || call.ID == "" || e.ctx == nil || e.execute == nil {
		return false
	}
	entry := &streamingToolEntry{call: call, argsHash: canonicalArgsHash(call.Args), state: streamingToolQueued, done: make(chan struct{})}
	e.mu.Lock()
	if _, exists := e.entries[call.ID]; exists {
		e.mu.Unlock()
		return false
	}
	e.entries[call.ID] = entry
	if e.running >= e.limit {
		e.deferred = append(e.deferred, entry)
		e.mu.Unlock()
		return true
	}
	e.running++
	e.mu.Unlock()
	e.run(entry)
	return true
}

func (e *StreamingToolExecutor) run(entry *streamingToolEntry) {
	go e.runEntry(entry)
}

func (e *StreamingToolExecutor) runEntry(entry *streamingToolEntry) {
	e.mu.Lock()
	if entry.discarded || e.ctx.Err() != nil {
		e.running--
		e.startDeferredLocked()
		e.mu.Unlock()
		close(entry.done)
		return
	}
	entry.state = streamingToolExecuting
	entry.startedAt = time.Now()
	call := entry.call
	startedAt := entry.startedAt
	e.mu.Unlock()

	if e.onSpeculativeStart != nil {
		e.onSpeculativeStart(call.ID, call.Name, startedAt)
	}

	if e.emit != nil {
		e.emit(ToolCallExecutionEvent{ID: call.ID, Name: call.Name, ArgsJSON: string(call.Args), State: ToolCallExecutionStateRunning})
	}
	result, err := e.execute(e.ctx, call)
	completedAt := time.Now()

	e.mu.Lock()
	entry.result = result
	entry.err = err
	entry.completedAt = completedAt
	if entry.discarded || e.ctx.Err() != nil {
		entry.state = streamingToolDiscarded
	} else {
		entry.state = streamingToolCompleted
	}
	e.running--
	e.startDeferredLocked()
	discarded := entry.discarded || e.ctx.Err() != nil
	e.mu.Unlock()
	close(entry.done)

	if discarded || e.emit == nil {
		return
	}
	if e.onFirstVisibleResult != nil {
		e.onFirstVisibleResult(call.ID, call.Name, time.Now())
	}
	status := toolResultStatusFromError(err != nil)
	e.emit(ToolResultEvent{CallID: call.ID, Name: call.Name, ArgsJSON: result.EffectiveArgsJSON, Audit: result.Audit.Clone(), Result: result.Result, Status: status})
}

func (e *StreamingToolExecutor) startDeferredLocked() {
	for e.running < e.limit && len(e.deferred) > 0 {
		entry := e.deferred[0]
		e.deferred = e.deferred[1:]
		if entry.discarded {
			close(entry.done)
			continue
		}
		e.running++
		go e.runEntry(entry)
	}
}

func (e *StreamingToolExecutor) Promote(call message.ToolCall) (*ToolResultPayload, bool, bool) {
	if e == nil || call.ID == "" {
		return nil, false, false
	}
	e.mu.Lock()
	entry := e.entries[call.ID]
	if entry == nil || entry.discarded {
		e.mu.Unlock()
		return nil, false, false
	}
	if entry.argsHash != canonicalArgsHash(call.Args) {
		entry.discarded = true
		entry.discardWhy = "args_drift"
		entry.state = streamingToolDiscarded
		delete(e.entries, call.ID)
		e.mu.Unlock()
		return nil, false, true
	}
	done := entry.done
	e.mu.Unlock()

	select {
	case <-done:
	case <-e.ctx.Done():
		return nil, false, false
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if entry.state != streamingToolCompleted && entry.state != streamingToolYielded {
		return nil, false, false
	}
	entry.state = streamingToolYielded
	delete(e.entries, call.ID)
	startedAt := entry.startedAt
	if startedAt.IsZero() {
		startedAt = entry.completedAt
	}
	var diff agentdiff.Summary
	if entry.err == nil {
		effective := call
		effective.Args = json.RawMessage(entry.result.EffectiveArgsJSON)
		diff = agentdiff.GenerateToolDiff(effective, entry.result.PreContent, entry.result.PreFilePath)
	}
	return &ToolResultPayload{CallID: call.ID, Name: call.Name, ArgsJSON: entry.result.EffectiveArgsJSON, Audit: entry.result.Audit, Result: entry.result.Result, Error: entry.err, TurnID: e.turnID, Duration: entry.completedAt.Sub(startedAt), Diff: diff.Text, DiffAdded: diff.Added, DiffRemoved: diff.Removed, FileCreated: call.Name == tools.NameWrite && !entry.result.PreExisted, LSPReviews: append([]message.LSPReview(nil), entry.result.LSPReviews...)}, true, false
}

func (e *StreamingToolExecutor) DiscardCall(callID, reason string) (StreamingToolDiscardInfo, bool) {
	if e == nil || strings.TrimSpace(callID) == "" {
		return StreamingToolDiscardInfo{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entry := e.entries[callID]
	if entry == nil {
		return StreamingToolDiscardInfo{}, false
	}
	started := !entry.startedAt.IsZero()
	completed := entry.state == streamingToolCompleted || entry.state == streamingToolYielded
	entry.discarded = true
	entry.discardWhy = reason
	entry.state = streamingToolDiscarded
	delete(e.entries, callID)
	info := StreamingToolDiscardInfo{
		CallID:      entry.call.ID,
		Name:        entry.call.Name,
		ArgsJSON:    string(entry.call.Args),
		Reason:      reason,
		Started:     started,
		Completed:   completed,
		StartedAt:   entry.startedAt,
		CompletedAt: entry.completedAt,
	}
	if e.onSpeculativeDiscarded != nil {
		e.onSpeculativeDiscarded(info)
	}
	return info, true
}
func (e *StreamingToolExecutor) DiscardExcept(valid map[string]struct{}, reason string) []PendingToolCall {
	info := e.DiscardExceptInfo(valid, reason)
	if len(info) == 0 {
		return nil
	}
	out := make([]PendingToolCall, 0, len(info))
	for _, it := range info {
		out = append(out, PendingToolCall{CallID: it.CallID, Name: it.Name, ArgsJSON: it.ArgsJSON})
	}
	return out
}

func (e *StreamingToolExecutor) DiscardExceptInfo(valid map[string]struct{}, reason string) []StreamingToolDiscardInfo {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var discarded []StreamingToolDiscardInfo
	for id, entry := range e.entries {
		if _, ok := valid[id]; ok {
			continue
		}
		started := !entry.startedAt.IsZero()
		completed := entry.state == streamingToolCompleted || entry.state == streamingToolYielded
		entry.discarded = true
		entry.discardWhy = reason
		entry.state = streamingToolDiscarded
		delete(e.entries, id)
		info := StreamingToolDiscardInfo{
			CallID:      entry.call.ID,
			Name:        entry.call.Name,
			ArgsJSON:    string(entry.call.Args),
			Reason:      reason,
			Started:     started,
			Completed:   completed,
			StartedAt:   entry.startedAt,
			CompletedAt: entry.completedAt,
		}
		discarded = append(discarded, info)
		if e.onSpeculativeDiscarded != nil {
			e.onSpeculativeDiscarded(info)
		}
	}
	return discarded
}

func (e *StreamingToolExecutor) DiscardAll(reason string) []PendingToolCall {
	return e.DiscardExcept(map[string]struct{}{}, reason)
}

func canonicalArgsHash(args json.RawMessage) string {
	var v any
	if len(args) > 0 && json.Unmarshal(args, &v) == nil {
		if canonical, err := json.Marshal(v); err == nil {
			sum := sha256.Sum256(canonical)
			return hex.EncodeToString(sum[:])
		}
	}
	sum := sha256.Sum256(args)
	return hex.EncodeToString(sum[:])
}

func logStreamingToolDiscard(reason string, calls []PendingToolCall) {
	if len(calls) == 0 {
		return
	}
	log.Debugf("discarded speculative streaming tools reason=%s count=%d", reason, len(calls))
}

func logStreamingToolDiscardInfo(reason string, calls []StreamingToolDiscardInfo) {
	if len(calls) == 0 {
		return
	}
	started := 0
	for _, c := range calls {
		if c.Started {
			started++
		}
	}
	log.Debugf("discarded speculative streaming tools reason=%s count=%d started=%d", reason, len(calls), started)
}
