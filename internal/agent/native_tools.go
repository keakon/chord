package agent

import (
	"context"
	"slices"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func nativeToolPermitted(registry *tools.Registry, rules permission.Ruleset, hooks hook.Manager, name string) bool {
	if registry == nil {
		return false
	}
	tool, ok := registry.Get(name)
	if !ok {
		return false
	}
	hosted, ok := tool.(tools.HostedTool)
	if !ok || !hosted.NativeEligible() {
		return false
	}
	match := rules.LastMatch(name, "*")
	if !match.Found || match.Rule.Action != permission.ActionAllow {
		return false
	}
	for _, rule := range rules {
		permissionOnly := rule
		permissionOnly.Pattern = "*"
		if permissionOnly.Matches(name, "*") && rule.Pattern != "*" {
			return false
		}
	}
	return hooks == nil || (!hooks.HasSyncHooks(hook.OnToolCall) && !hooks.HasSyncHooks(hook.OnBeforeToolResultAppend))
}

func nativePolicy(journal recovery.NativeRequestJournal, permitted func(string) bool, recordUsage func(llm.NativeRequestRecord, *message.Response, error)) *llm.NativeToolPolicy {
	var current llm.NativeRequestRecord
	var continuations []string
	return &llm.NativeToolPolicy{
		Permitted: permitted,
		Preflight: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			receipt, err := journal.Check()
			if err != nil {
				return &llm.NativeToolError{Cause: err, Receipt: receipt}
			}
			return nil
		},
		Begin: func(ctx context.Context, record llm.NativeRequestRecord) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			current = record
			return journal.Begin(record, continuations)
		},
		Finish: func(id string, outcome message.NativeRequestOutcome, resp *message.Response, err error) error {
			if recordUsage != nil && outcome != message.NativeRequestNotSent {
				recordUsage(current, resp, err)
			}
			if persistErr := journal.Finish(id, outcome, resp, err); persistErr != nil {
				return persistErr
			}
			if err == nil && resp != nil {
				continuations = append(continuations, id)
			}
			return nil
		},
	}
}
func (a *MainAgent) nativeRequestPolicy(turn *Turn) *llm.NativeToolPolicy {
	if a.recoveryManager() == nil || turn == nil {
		return nil
	}
	journal := recovery.NativeRequestJournal{SessionDir: a.SessionDir(), AgentID: identity.MainAgentID, TurnID: turn.ID, Generation: turn.Epoch}
	policy := nativePolicy(journal, func(name string) bool { return nativeToolPermitted(a.tools, a.snapshotRuleset(), a.hookEngine, name) }, a.nativeUsageRecorder(identity.MainAgentID, identity.MainAgentID, a.currentAgentName(), turn.ID))
	policy.Preflight = nativeReceiptPreflight(policy.Preflight, a.nativeReceipt.Load())
	policy.Failed = func(receipt *message.NativeToolHistory) {
		a.sendEvent(Event{Type: EventNativeReceipt, Payload: nativeReceiptPayload{epoch: journal.Generation, receipt: receipt, failed: true}})
	}
	return policy
}
func (s *SubAgent) nativeRequestPolicy(turn *Turn) *llm.NativeToolPolicy {
	if s.recoveryManager() == nil || turn == nil {
		return nil
	}
	var hooks hook.Manager
	if s.parent != nil {
		hooks = s.parent.hookEngine
	}
	var recorder func(llm.NativeRequestRecord, *message.Response, error)
	if s.parent != nil {
		recorder = s.parent.nativeUsageRecorder(s.instanceID, "sub", s.agentDefName, turn.ID)
	}
	journal := s.nativeJournal()
	policy := nativePolicy(journal, func(name string) bool { return nativeToolPermitted(s.tools, s.currentRuleset(), hooks, name) }, recorder)
	policy.Preflight = nativeReceiptPreflight(policy.Preflight, s.nativeReceipt.Load())
	if s.parent != nil {
		policy.Failed = func(receipt *message.NativeToolHistory) {
			s.parent.sendEvent(Event{Type: EventNativeReceipt, Payload: nativeReceiptPayload{epoch: journal.Generation, agentID: s.instanceID, receipt: receipt, failed: true, sub: s}})
		}
	}
	return policy
}
func (s *SubAgent) nativeJournal() recovery.NativeRequestJournal {
	agentID := s.taskID
	if agentID == "" {
		agentID = s.instanceID
	}
	return recovery.NativeRequestJournal{SessionDir: s.sessionDir, AgentID: agentID, TurnID: s.currentTurnID(), Generation: s.sessionEpoch}
}
func (s *SubAgent) acknowledgeNativeMessage(journal recovery.NativeRequestJournal, msg message.Message) error {
	var err error
	if msg.NativeTools != nil && !msg.NativeTools.OutcomeUnknown {
		err = journal.Acknowledge(msg.NativeTools)
	}
	return err
}

type nativeReceiptPayload struct {
	failed  bool
	sub     *SubAgent
	epoch   uint64
	agentID string
	receipt *message.NativeToolHistory
}

func (a *MainAgent) handleNativeReceipt(evt Event) {
	payload, ok := evt.Payload.(nativeReceiptPayload)
	if !ok || payload.epoch != a.sessionEpoch {
		return
	}
	if payload.failed {
		if payload.sub != nil {
			payload.sub.persistNativeFailure(payload.receipt)
		} else {
			a.persistNativeFailure(payload.receipt)
		}
		return
	}
	for _, display := range tools.NativeToolDisplays(payload.receipt) {
		a.emitToTUI(ToolCallStartEvent{ID: display.ID, Name: display.Name, ArgsJSON: display.Args, AgentID: payload.agentID})
		a.emitToTUI(ToolResultEvent{CallID: display.ID, Name: display.Name, ArgsJSON: display.Args, Result: display.Result, Payload: display.Result, Status: ToolResultStatus(display.Status), AgentID: payload.agentID})
	}
}

func (a *MainAgent) nativeUsageRecorder(agentID, kind, name string, turnID uint64) func(llm.NativeRequestRecord, *message.Response, error) {
	return func(record llm.NativeRequestRecord, resp *message.Response, err error) {
		var usage *message.TokenUsage
		attempt := hostedWireAttempt{response: resp, err: err, reason: hostedAttemptInitial}
		if record.Continuation > 0 {
			attempt.reason = hostedAttemptContinuation
		}
		if resp != nil {
			usage = resp.Usage
			attempt.elapsed = resp.NativeRequestDuration
		}
		diagnostic := hostedAttemptDiagnostics(attempt)
		diagnostic["native_contract"] = record.Authorization.Contract
		a.recordUsage(agentID, kind, name, "chat", record.Target, record.Target, turnID, usage, record.ServiceTier, diagnostic)
	}
}

// Failure receipts are canonical history too. They never acknowledge the
// request journal: the user must reconcile possible execution before moving on.
func (a *MainAgent) persistNativeFailure(receipt *message.NativeToolHistory) {
	if nativeFailureRecorded(a.ctxMgr.Snapshot(), receipt) {
		return
	}
	msg := message.Message{Role: message.RoleAssistant, NativeTools: receipt}
	a.ctxMgr.Append(msg)
	epoch := a.sessionEpoch
	a.persistAsyncAfter(identity.MainAgentID, msg, func(writeErr error) {
		a.notePersistenceFailure(writeErr)
		if writeErr == nil {
			a.sendEvent(Event{Type: EventNativeReceipt, Payload: nativeReceiptPayload{epoch: epoch, receipt: msg.NativeTools}})
		}
	})
}
func (s *SubAgent) persistNativeFailure(receipt *message.NativeToolHistory) {
	if nativeFailureRecorded(s.ctxMgr.Snapshot(), receipt) {
		return
	}
	msg := message.Message{Role: message.RoleAssistant, NativeTools: receipt}
	s.ctxMgr.Append(msg)
	parent, epoch, agentID := s.parent, s.sessionEpoch, s.instanceID
	parent.persistAsyncForEpoch(epoch, agentID, msg, func(writeErr error) {
		s.notePersistenceFailure(writeErr)
		if writeErr == nil {
			parent.sendEvent(Event{Type: EventNativeReceipt, Payload: nativeReceiptPayload{epoch: epoch, agentID: agentID, receipt: receipt}})
		}
	})
}

func nativeFailureRecorded(history []message.Message, receipt *message.NativeToolHistory) bool {
	if receipt == nil {
		return true
	}
	for _, id := range receipt.RequestIDs {
		found := false
		for _, msg := range history {
			if msg.NativeTools != nil && msg.NativeTools.OutcomeUnknown && slices.Contains(msg.NativeTools.RequestIDs, id) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(receipt.RequestIDs) > 0
}
