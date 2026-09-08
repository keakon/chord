package agent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/tools"
)

// model-driven compact_context proposal lifecycle.
//
// Every accepted compact_context request becomes a modelDrivenProposalState
// record on the event loop. The record is the single authority for the
// attempt's lifecycle — identity, status, runtime-owned reason/time of the
// last transition, and the audit copy of the accepted arguments — and it is
// exactly what the recovery snapshot persists and a restore reads back. All
// mutations funnel through armModelDrivenProposal and
// transitionModelDrivenProposal, which persist the snapshot after every
// change; no caller updates the fields directly, so the in-memory record and
// the persisted snapshot cannot drift apart field by field.
//
// Lifecycle:
//
//	""
//	 -> accepted (armModelDrivenProposal, on an accepted tool result)
//	 -> preparing (tool-batch barrier hands the attempt to the worker)
//	 -> applied  (the durable checkpoint was applied on the event loop)
//	accepted|preparing -> skipped | failed | cancelled (terminal settles)
//
// A proposal that a turn teardown drops before the barrier keeps its accepted
// record: the process may crash later, and a restore must then surface a
// "requested but not applied" notice instead of silently losing the intent.
// The next accepted request re-arms the record (accepted -> accepted with a
// new identity).

// Model-driven proposal statuses, part of the same lifecycle vocabulary as the
// terminal CompactionStatus* values in event.go.
const (
	modelDrivenProposalAccepted  = "accepted"
	modelDrivenProposalPreparing = "preparing"
	modelDrivenProposalApplied   = "applied"
)

// modelDrivenProposalState is the event-loop-owned lifecycle record of the
// most recent model-driven compact_context attempt.
type modelDrivenProposalState struct {
	// requestID is the identity of the most recent model-driven request. It is
	// set when the request is accepted and retained past the terminal settle —
	// a settle can arrive after the armed request was consumed by the barrier,
	// and the request that produced it must stay correlatable — until the next
	// accepted request replaces it.
	requestID string
	// status is the lifecycle status: "" before the first attempt, then one of
	// the modelDrivenProposal* constants or a terminal CompactionStatus*
	// value (skipped/failed/cancelled).
	status string
	// reason is the runtime-owned, trimmed reason of the last transition.
	reason string
	// updatedAt is when the last transition happened.
	updatedAt time.Time
	// argsJSON is the canonical audit copy of the accepted arguments
	// (re-marshaled parsed form). It survives only while the proposal can
	// still apply (accepted/preparing): a terminal settle clears it, and a
	// restore never re-applies it — it exists so a not-applied proposal's
	// intent is not lost.
	argsJSON string
}

func (p *modelDrivenProposalState) isEmpty() bool {
	return p == nil || (p.requestID == "" && p.status == "" && p.reason == "" && p.argsJSON == "" && p.updatedAt.IsZero())
}

// marshalCompactContextArgsForAudit renders the accepted arguments in their
// canonical re-marshaled parsed form for the proposal audit copy. The raw tool
// argument string may carry formatting or ordering the model chose; the
// canonical form is what a restore would ever compare against, so it is what
// gets persisted.
func marshalCompactContextArgsForAudit(args tools.CompactContextArgs) string {
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(data)
}

// armModelDrivenProposal records a freshly accepted compact_context request as
// the current proposal, replacing whatever record existed before (an earlier
// accepted proposal that a turn teardown dropped, or a settled one). The
// snapshot is persisted once, after all fields are set.
func (a *MainAgent) armModelDrivenProposal(callID string, args tools.CompactContextArgs, argsJSON, reason string) {
	p := &a.modelDrivenProposal
	p.requestID = callID
	p.argsJSON = argsJSON
	p.status = modelDrivenProposalAccepted
	p.reason = strings.TrimSpace(reason)
	p.updatedAt = time.Now()
	a.saveRecoverySnapshot()
}

// transitionModelDrivenProposal advances the proposal lifecycle record. It
// updates identity-independent metadata (status/reason/updatedAt) and persists
// the snapshot as one step. A terminal settle also clears the audit args copy:
// the settle is the end of the attempt, and the lifecycle event has already
// recorded the request identity.
//
// The move is checked against the documented lifecycle; an impossible move
// (for example accepted -> applied without the preparing barrier) is logged as
// a warning so a regression in the call graph surfaces in the log instead of
// silently overwriting a settled record. The transition itself still applies:
// refusing it would leave callers with a record that no longer matches what
// the rest of the state machine did.
func (a *MainAgent) transitionModelDrivenProposal(status, reason string) {
	if a == nil {
		return
	}
	p := &a.modelDrivenProposal
	if !modelDrivenProposalTransitionAllowed(p.status, status) {
		log.Warnf("illegal model-driven proposal transition from=%q to=%q reason=%q", p.status, status, reason)
		return
	}
	p.status = status
	p.reason = strings.TrimSpace(reason)
	p.updatedAt = time.Now()
	if isModelDrivenProposalTerminal(status) {
		p.argsJSON = ""
	}
	a.saveRecoverySnapshot()
}

// isModelDrivenProposalTerminal reports whether the status ends the proposal
// lifecycle (the next lifecycle event can only be a fresh accept).
func isModelDrivenProposalTerminal(status string) bool {
	switch status {
	case CompactionStatusSkipped, CompactionStatusFailed, CompactionStatusCancelled, modelDrivenProposalApplied:
		return true
	}
	return false
}

// modelDrivenProposalTransitionAllowed reports whether moving from the given
// source status to the given target status is a documented lifecycle step.
// Accepting is always allowed: the first accept starts the record and a later
// accept re-arms it after a teardown or a settle.
func modelDrivenProposalTransitionAllowed(from, to string) bool {
	switch to {
	case modelDrivenProposalAccepted:
		return true
	case modelDrivenProposalPreparing:
		return from == modelDrivenProposalAccepted
	case modelDrivenProposalApplied:
		return from == modelDrivenProposalPreparing
	default:
		// Terminal settles (skipped/failed/cancelled). An applied proposal is
		// already a successful terminal state; stale worker events must not
		// overwrite it.
		return isModelDrivenProposalTerminal(to) &&
			(from == "" || from == modelDrivenProposalAccepted || from == modelDrivenProposalPreparing ||
				(from != modelDrivenProposalApplied && isModelDrivenProposalTerminal(from)))
	}
}
