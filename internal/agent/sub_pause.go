package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"sort"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// SubSessionPauseCapture is produced by the owning loop, never by its parent.
type SubSessionPauseCapture struct {
	TaskID         string
	PendingInputs  []pendingUserMessage
	ContextAppends []message.Message
	LLMInFlight    bool
	ToolInFlight   int
}

type subPauseCaptureRequest struct {
	result chan SubSessionPauseCapture
	drain  bool
}

func (s *SubAgent) sessionExecutionGate() *executiongate.Gate {
	if s == nil || s.parent == nil {
		return nil
	}
	return s.parent.sessionExecutionGate()
}
func (s *SubAgent) sessionExecutionPaused() bool { return s.sessionExecutionGate().Snapshot().Paused }

func clonePauseInput(input pendingUserMessage) pendingUserMessage {
	input.Parts = cloneContentParts(input.Parts)
	if input.Mailbox != nil {
		meta := *input.Mailbox
		input.Mailbox = &meta
	}
	return input
}

func (s *SubAgent) assignPauseInputIdentity(input *pendingUserMessage) {
	if input.InputID != "" {
		return
	}
	if s.inputRuntimeID == "" {
		s.inputRuntimeID = rand.Text()
	}
	s.inputSequence++
	input.AcceptedOrder = s.inputSequence
	input.InputID = fmt.Sprintf("%s:%s:%d", s.inputRuntimeID, s.taskID, s.inputSequence)
	input.Revision = 1
	input.Stage = recovery.PendingInputStageQueued
}

func (s *SubAgent) pauseInputAlreadyAppended(id string) bool {
	if id == "" {
		return false
	}
	for _, msg := range s.ctxMgr.Snapshot() {
		if msg.InputID == id {
			return true
		}
	}
	return false
}

func (s *SubAgent) CaptureSessionPause(ctx context.Context) (SubSessionPauseCapture, error) {
	if !s.sessionExecutionPaused() {
		return SubSessionPauseCapture{}, fmt.Errorf("session execution is not paused")
	}
	req := &subPauseCaptureRequest{result: make(chan SubSessionPauseCapture, 1)}
	select {
	case s.continueCh <- continueMsg{pauseCapture: req}:
	case <-ctx.Done():
		return SubSessionPauseCapture{}, ctx.Err()
	case <-s.done:
		return SubSessionPauseCapture{}, fmt.Errorf("sub-agent loop is stopped")
	}
	select {
	case result := <-req.result:
		return result, nil
	case <-ctx.Done():
		return SubSessionPauseCapture{}, ctx.Err()
	case <-s.done:
		return SubSessionPauseCapture{}, fmt.Errorf("sub-agent loop is stopped")
	}
}

// StopAfterSessionPause retires an already drained owner without cancellation.
func (s *SubAgent) StopAfterSessionPause(ctx context.Context) error {
	result := make(chan error, 1)
	select {
	case s.continueCh <- continueMsg{stopPaused: result}:
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-result:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *SubAgent) handleSessionPauseControl(msg continueMsg) bool {
	if msg.pauseSpecDone {
		s.pauseSpecDrained = true
		s.trySessionPauseCaptures()
		return true
	}
	if msg.pauseCapture != nil {
		if msg.pauseCapture.drain && !s.pauseSpecStarted {
			s.pauseSpecStarted = true
			var executor *StreamingToolExecutor
			if s.turn != nil {
				executor = s.turn.streamingToolExec
			}
			go func() {
				_ = executor.WaitForSessionPause(s.parentCtx)
				select {
				case s.continueCh <- continueMsg{pauseSpecDone: true}:
				case <-s.parentCtx.Done():
				}
			}()
		}
		s.pauseCaptures = append(s.pauseCaptures, msg.pauseCapture)
		s.trySessionPauseCaptures()
		return true
	}
	if msg.stopPaused != nil {
		if !s.sessionExecutionPaused() || s.llmRequestInFlight.Load() || (s.turn != nil && s.turn.PendingToolCalls.Load() != 0) || len(s.promotedToolQueue) != 0 {
			msg.stopPaused <- fmt.Errorf("sub-agent has not drained")
			return true
		}
		s.pauseStopped = true
		msg.stopPaused <- nil
		return true
	}
	if s.sessionExecutionPaused() && !msg.cancelCurrentTurn {
		return true
	}
	return false
}

func (s *SubAgent) DrainSessionPause(ctx context.Context) (SubSessionPauseCapture, error) {
	if !s.sessionExecutionPaused() {
		return SubSessionPauseCapture{}, fmt.Errorf("session execution is not paused")
	}
	req := &subPauseCaptureRequest{result: make(chan SubSessionPauseCapture, 1), drain: true}
	select {
	case s.continueCh <- continueMsg{pauseCapture: req}:
	case <-ctx.Done():
		return SubSessionPauseCapture{}, ctx.Err()
	case <-s.done:
		return SubSessionPauseCapture{}, fmt.Errorf("sub-agent loop is stopped")
	}
	select {
	case capture := <-req.result:
		return capture, nil
	case <-ctx.Done():
		return SubSessionPauseCapture{}, ctx.Err()
	case <-s.done:
		return SubSessionPauseCapture{}, fmt.Errorf("sub-agent loop is stopped")
	}
}

func (s *SubAgent) trySessionPauseCaptures() {
	if len(s.pauseCaptures) == 0 {
		return
	}
	if s.llmRequestInFlight.Load() && !s.pauseRequestStarted.Load() {
		return
	}
	s.inputQueueMu.Lock()
	if s.inputQueueReservedMessages != 0 {
		s.inputQueueMu.Unlock()
		return
	}
	s.pauseInputs = append(s.pauseInputs, s.takePendingUserMessagesLocked()...)
	s.inputQueueMu.Unlock()
	s.ctxAppendQueueMu.Lock()
	for {
		select {
		case msg := <-s.ctxAppendCh:
			s.pauseContextAppends = append(s.pauseContextAppends, msg)
		default:
			goto drained
		}
	}
drained:
	s.pauseContextAppends = append(s.pauseContextAppends, s.ctxAppendOverflow...)
	s.ctxAppendOverflow = nil
	s.ctxAppendBytes = 0
	s.ctxAppendQueueMu.Unlock()
	capture := SubSessionPauseCapture{TaskID: s.taskID, LLMInFlight: s.llmRequestInFlight.Load()}
	if s.turn != nil {
		capture.ToolInFlight = int(s.turn.PendingToolCalls.Load())
	}
	inputs := append([]pendingUserMessage(nil), s.pauseInputs...)
	s.inputQueueMu.Lock()
	inputs = append(inputs, s.restoredPauseInputs...)
	s.inputQueueMu.Unlock()
	if !s.pauseRequestStarted.Load() {
		inputs = append(inputs, s.stagedPauseInputs...)
	}
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if input.InputID != "" && seen[input.InputID] {
			continue
		}
		seen[input.InputID] = true
		capture.PendingInputs = append(capture.PendingInputs, clonePauseInput(input))
	}
	sort.SliceStable(capture.PendingInputs, func(i, j int) bool {
		return capture.PendingInputs[i].AcceptedOrder < capture.PendingInputs[j].AcceptedOrder
	})
	for index, msg := range s.pauseContextAppends {
		s.inputQueueMu.Lock()
		input := pendingUserMessage{InputID: msg.InputID, Content: msg.Content, Parts: msg.Parts, Kind: msg.Kind, Mailbox: msg.Mailbox, MailboxAckID: msg.MailboxAckID, Stage: recovery.PendingInputStageQueued}
		if s.pauseContextSequences == nil {
			s.pauseContextSequences = make(map[string]int64)
		}
		if input.InputID == "" {
			s.assignPauseInputIdentity(&input)
			msg.InputID = input.InputID
			s.pauseContextAppends[index] = msg
			s.pauseContextSequences[input.InputID] = input.AcceptedOrder
		}
		if s.pauseContextSequences[input.InputID] == 0 {
			s.inputSequence++
			s.pauseContextSequences[input.InputID] = s.inputSequence
		}
		input.AcceptedOrder = s.pauseContextSequences[input.InputID]
		s.inputQueueMu.Unlock()
		capture.PendingInputs = append(capture.PendingInputs, clonePauseInput(input))
	}
	sort.SliceStable(capture.PendingInputs, func(i, j int) bool {
		return capture.PendingInputs[i].AcceptedOrder < capture.PendingInputs[j].AcceptedOrder
	})

	remaining := s.pauseCaptures[:0]
	for _, req := range s.pauseCaptures {
		if req.drain && (!s.pauseSpecDrained || capture.LLMInFlight || capture.ToolInFlight != 0 || len(s.promotedToolQueue) != 0) {
			remaining = append(remaining, req)
			continue
		}
		req.result <- capture
	}
	s.pauseCaptures = remaining
}

// RestorePauseInputs installs durable pending input without starting execution.
func (s *SubAgent) RestorePauseInputs(inputs []pendingUserMessage) {
	s.inputQueueMu.Lock()
	defer s.inputQueueMu.Unlock()
	for _, input := range inputs {
		input = clonePauseInput(input)
		duplicate := false
		for i, existing := range s.restoredPauseInputs {
			if input.InputID != "" && existing.InputID == input.InputID {
				if input.Revision > existing.Revision {
					s.restoredPauseInputs[i] = input
				}
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if input.AcceptedOrder > s.inputSequence {
			s.inputSequence = input.AcceptedOrder
		}
		s.restoredPauseInputs = append(s.restoredPauseInputs, input)
	}
}

func (s *SubAgent) startPausedToolCalls(turn *Turn, calls []message.ToolCall) {
	turn.PendingToolCalls.Store(int32(len(calls)))
	for _, call := range calls {
		call := call
		turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args), AgentID: s.instanceID})
		go func() {
			result := &toolResult{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args), TurnID: turn.ID, Error: &executiongate.ErrSessionPaused{}, RecoveryState: message.ToolRecoveryStateNotStarted}
			if turn.streamingToolExec != nil {
				if payload, ok, _ := turn.streamingToolExec.Promote(call); ok {
					result = pausedSubToolResult(payload)
					if err := s.commitPromotedToolSideEffects(call, result); err != nil {
						result.Error = fmt.Errorf("commit paused speculative result: %w", err)
					}
				}
			}
			select {
			case s.toolCh <- result:
			case <-s.parentCtx.Done():
			}
		}()
	}
}

func pausedSubToolResult(p *ToolResultPayload) *toolResult {
	return &toolResult{CallID: p.CallID, Name: p.Name, ArgsJSON: p.ArgsJSON, Audit: p.Audit, Result: p.Result, Payload: p.Payload, Notes: p.Notes, Images: p.Images, DeliveredFiles: message.CloneDeliveredFiles(p.DeliveredFiles), Error: p.Error, TurnID: p.TurnID, Duration: p.Duration, Diff: p.Diff, DiffAdded: p.DiffAdded, DiffRemoved: p.DiffRemoved, FileCreated: p.FileCreated, LSPReviews: p.LSPReviews, FileState: p.FileState, walltimeTarget: p.walltimeTarget, speculativeHooks: p.speculativeHooks, RecoveryState: p.RecoveryState}
}

// handlePausedLLMResponse records the completed request before settling every
// unstarted tool. Recovery heuristics cannot discard a response during transfer.
func (s *SubAgent) handlePausedLLMResponse(resp *message.Response) {
	if resp == nil {
		return
	}
	calls := sanitizeToolCallArgs(resp.ToolCalls)
	if s.turn.streamingToolExec != nil {
		valid := make(map[string]struct{}, len(calls))
		for _, call := range calls {
			valid[call.ID] = struct{}{}
		}
		s.turn.streamingToolExec.DiscardExceptInfo(valid, "filtered")
	}
	msg := message.Message{Role: message.RoleAssistant, Content: resp.Content, ToolCalls: calls, ThinkingBlocks: resp.ThinkingBlocks, ResponsesOutput: resp.ResponsesOutput, GeminiParts: resp.GeminiParts, ReasoningContent: resp.ReasoningContent, NativeTools: resp.NativeTools, StopReason: resp.StopReason, Usage: resp.Usage, Provenance: subAssistantProvenance(s)}
	s.ctxMgr.Append(msg)
	s.parent.emitToTUI(AssistantMessageEvent{FileLinkBaseDir: s.effectiveToolBaseDir(), AgentID: s.instanceID, TaskID: s.taskID, AgentType: s.agentDefName, ParentAgentID: controlPlaneAgentID(s.OwnerAgentID()), Text: resp.Content, ToolCalls: len(calls)})
	barrier, pending := s.persistMessageBarrier(msg, "assistant message")
	if resp.Usage != nil {
		s.ctxMgr.UpdateFromUsage(*resp.Usage)
	} else {
		s.ctxMgr.NoteMissingUsage()
	}
	if err := s.waitPersistBarrier(barrier, pending); err != nil {
		s.failIntentBarrier(calls, err)
		return
	}
	s.startPausedToolCalls(s.turn, calls)
}

func (s *SubAgent) consumeRestoredPauseInputs() {
	s.inputQueueMu.Lock()
	pending := s.restoredPauseInputs
	s.restoredPauseInputs = nil
	s.inputQueueMu.Unlock()
	s.appendPendingUserMessages(pending)
}
