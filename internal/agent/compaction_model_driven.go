package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// modelDrivenCheckpointRequest is the event-loop-owned pending state armed
// when a compact_context tool result is accepted. It is consumed at the
// tool-batch barrier (all sibling calls and batch hooks done) and never
// touched from the compaction worker goroutine.
type modelDrivenCheckpointRequest struct {
	ToolCallID    string
	Args          tools.CompactContextArgs
	ClaimStatuses map[string]string
	// ArgsFingerprint is the canonical argument fingerprint used with the
	// post-apply runtime fingerprint to reject an unchanged checkpoint request.
	ArgsFingerprint string
	// Claims is the authoritative claim set when it is set: the merged carry
	// (fresh submission claims plus carried claims it did not restate) that
	// mergePriorTypedCheckpointState produces. Rendering and invalidation read
	// this instead of rebuilding from Args, so a carried claim keeps its
	// observed/assumed classification, evidence association and status. It is
	// nil for a fresh request that carried no prior typed state, in which case
	// the Args-derived claims are used.
	Claims map[string]checkpointClaim
	// CarriedOpenIssues is the authoritative historical open-issue set when it
	// is set: the open issues an earlier generation confirmed that this
	// submission did not restate. Rendering reads it instead of rebuilding
	// from Args, which carries only the current generation's confirmed issues.
	// It is nil for a request that carried no prior typed state.
	CarriedOpenIssues []string
}

// requestAcceptedToolResult is the canonical compact_context success text. It
// deliberately says "accepted", not "applied": a crash between acceptance and
// apply must not let the restored transcript read as a successful reset.
const requestAcceptedToolResult = "Context checkpoint request accepted. No reset has occurred yet; only a later model-driven context checkpoint confirms successful application."

const (
	// compactionSummaryModeModelDriven is the stable summary-mode marker for
	// model-driven checkpoints (mirrors model_summary / structured_fallback /
	// truncate_only).
	compactionSummaryModeModelDriven = message.CompactionSummaryModeModelDriven
	// modelDrivenLowGainMinTokens and modelDrivenLowGainMinRatio are the fixed
	// conservative low-gain gates: a reset must save at least this many
	// estimated tokens and this fraction of the prepared surface. Both must
	// hold; the tool description tells the model the runtime rejects
	// low-gain resets, so a skip is an expected outcome, not an error.
	modelDrivenLowGainMinTokens = 2048
	modelDrivenLowGainMinRatio  = 0.10
	// modelDrivenPostResetOverlayTokens is a conservative fixed allowance for
	// request-local overlays that a durable rewrite re-injects on the next
	// request (session reminder, MCP mount skeleton, key-file overlay, turn
	// overlays, thinking replay prefix). They cannot be serialized exactly at
	// preflight time, so the projected side is overestimated rather than
	// ignored: a reset is only allowed when the net gain survives the
	// re-injection cost.
	modelDrivenPostResetOverlayTokens = 1500
	// Keep ordinary task specifications intact. The bound prevents a pasted
	// document from dominating every subsequent checkpoint; oversized requests
	// retain an explicit recovery instruction instead of looking complete.
	modelDrivenAnchorMaxRunes = 8192
	// minModelDrivenApplyIntervalBatches is the conservative first-version
	// spacing between durable model-driven applies: a reset is only allowed
	// after at least this many main-model request batches since the last
	// successful model-driven apply (currentRequestBatch semantics, not call
	// counts). It prevents immediately re-resetting from a thin evidence
	// baseline right after an archival apply.
	minModelDrivenApplyIntervalBatches = 3
	// minModelDrivenSkipCooldownBatches is the fixed same-reason skip cooldown:
	// a retry within this many batches of the previous skip with the same
	// reason short-circuits without re-running the low-gain preflight (which
	// would otherwise re-run prepareMessagesForLLM for an outcome that cannot
	// change). Different reasons are never cooled down by each other.
	minModelDrivenSkipCooldownBatches = 2
	// modelDrivenCacheRebuildDeltaNumer/Denom carry the prompt-cache
	// write-read delta (1.15) as integer math: the rewritten checkpoint prefix
	// is cache-written once (write ≈ 1.25×) where the kept prefix would only
	// have been cache-read (≈ 0.1×); only the delta is charged, and it is
	// amortized over the minimum apply interval.
	modelDrivenCacheRebuildDeltaNumer = 115
	modelDrivenCacheRebuildDeltaDenom = 100
)

// ---------------------------------------------------------------- accept ---

// validateCompactContextResult performs the control-plane checks that must
// happen before the tool result is written: single tool call in the declaring
// response, persistence healthy, and re-validated arguments (trimmed,
// token-budgeted, lexically safe state_files). An automatic compaction
// already owning the slot does not reject the request: an explicit model
// checkpoint may override it (see maybeStartModelDrivenBarrier). On success
// it returns the canonical accepted text together with the parsed arguments,
// so the caller arms the pending request without re-parsing the raw args on
// the event loop.
func (a *MainAgent) validateCompactContextResult(callID string, rawArgs string) (string, tools.CompactContextArgs, error) {
	if a.turn == nil {
		return "", tools.CompactContextArgs{}, fmt.Errorf("compact_context requires an active turn")
	}
	// A compaction already owning the slot does not reject the request: the
	// model's explicit checkpoint may override an in-flight automatic
	// compaction (a usage-driven worker started by a threshold crossing, an
	// oversize recovery, or a ready draft waiting for the continuation
	// barrier). maybeStartModelDrivenBarrier discards the running compaction
	// when the model-driven barrier fires; if the armed request never reaches
	// that barrier (the turn ends or higher-priority work arrives first), the
	// running compaction settles on its own and the pending request is
	// dropped by the normal discard paths.
	if a.persistenceDegraded() {
		return "", tools.CompactContextArgs{}, fmt.Errorf("session persistence is degraded; compact_context cannot rewrite session history safely")
	}
	// The declaring assistant message must contain exactly this one tool call.
	if !compactContextSoleToolCall(a.ctxMgr.Snapshot(), callID) {
		return "", tools.CompactContextArgs{}, fmt.Errorf("compact_context must be the only tool call in its assistant response; sibling tool calls are not allowed")
	}
	args, err := a.parseCompactContextArgs(json.RawMessage(rawArgs))
	if err != nil {
		return "", tools.CompactContextArgs{}, err
	}
	return requestAcceptedToolResult, args, nil
}

// parseCompactContextArgs re-validates model arguments on the event loop with
// the usage-calibrated token estimator. The tool's own Execute already ran the
// same parser — cmd/chord wires the registered validator with this agent's
// estimator (common_runtime_setup.go) — so the re-run here does not change the
// budget verdict; it binds the accepted arguments to the event-loop state the
// barrier is about to snapshot instead of trusting the pipeline's result text.
// Only an estimator-less validator (unit tests) falls back to bytes/3.
func (a *MainAgent) parseCompactContextArgs(raw json.RawMessage) (tools.CompactContextArgs, error) {
	validator := tools.CompactContextValidator{
		ContinuationStateMaxTokens: CompactContinuationStateMaxTokens,
		WorkDir:                    a.workDir,
		EstimateTokens:             a.EstimateTokensForText,
	}
	return validator.ParseCompactContextArgs(raw)
}

// compactContextSoleToolCall reports whether the declaring assistant message
// carries exactly this one tool call and no sibling calls.
func compactContextSoleToolCall(messages []message.Message, callID string) bool {
	for _, msg := range slices.Backward(messages) {
		if len(msg.ToolCalls) == 0 {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID == callID {
				return len(msg.ToolCalls) == 1 && tools.NormalizeName(tc.Name) == tools.NameCompactContext
			}
		}
	}
	return false
}

// tryArmModelDrivenCheckpoint validates and arms the pending request. Called
// from handleToolResult before the tool message is appended; any error turns
// the result into an ordinary error result with no pending checkpoint.
func (a *MainAgent) tryArmModelDrivenCheckpoint(callID string, rawArgs string) (string, error) {
	result, args, err := a.validateCompactContextResult(callID, rawArgs)
	if err != nil {
		return "", err
	}
	// claim_evidence is validated before the merged top-level list: the tool
	// folds claim_evidence values into evidence_refs, so a bad reference
	// authored under a claim would otherwise be reported as an evidence_refs
	// entry and send the model to edit the field it authored correctly.
	claims := make([]string, 0, len(args.ClaimEvidence))
	for claim := range args.ClaimEvidence {
		claims = append(claims, claim)
	}
	slices.Sort(claims)
	for _, claim := range claims {
		if err := a.validateModelDrivenEvidenceRefs(fmt.Sprintf("claim_evidence[%q]", claim), args.ClaimEvidence[claim]); err != nil {
			return "", err
		}
	}
	if err := a.validateModelDrivenEvidenceRefs("evidence_refs", args.EvidenceRefs); err != nil {
		return "", err
	}
	if err := validateModelDrivenCheckpointKind(args); err != nil {
		return "", a.explainCheckpointRejection(err)
	}
	if err := a.validateObservedClaimEvidence(args); err != nil {
		return "", err
	}
	if err := a.validateCommittedEvidence(args); err != nil {
		return "", err
	}
	a.pendingModelDriven = &modelDrivenCheckpointRequest{
		ToolCallID:      callID,
		Args:            args,
		ArgsFingerprint: modelDrivenArgsFingerprint(args),
	}
	auditJSON := marshalCompactContextArgsForAudit(args)
	a.armModelDrivenProposal(callID, args, auditJSON, "accepted by runtime validation")
	diagnostic := map[string]string{"request_id": callID}
	if a.stageCompletionCandidatePending && a.stageCompletionCandidateTurnID > 0 && a.turn != nil && a.stageCompletionCandidateTurnID != a.turn.ID {
		a.clearStageCompletionCandidate()
	}
	if a.stageCompletionCandidatePending && a.stageCompletionCandidateTurnID > 0 {
		diagnostic["stage_candidate_turn_id"] = strconv.FormatUint(a.stageCompletionCandidateTurnID, 10)
		diagnostic["stage_candidate_consumed"] = "true"
		a.stageCompletionCandidatePending = false
		a.stageCompletionCandidatePromptDelivered = false
	}
	a.recordCompactionLifecycleEvent("accepted", diagnostic)
	// The model called compact_context in this window:
	// whatever the attempt settles to, the reminder nudge has been answered,
	// so the sticky reminder stops re-attaching until a fresh window resets
	// the claim (a durable apply advances the window; a skip/failure is
	// surfaced by the continuation notice).
	a.markReminderCompactContextCalled()
	return result, nil
}

func modelDrivenArgsFingerprint(args tools.CompactContextArgs) string {
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func modelDrivenCheckpointFingerprint(argsFingerprint, runtimeFingerprint string) string {
	argsFingerprint = strings.TrimSpace(argsFingerprint)
	runtimeFingerprint = strings.TrimSpace(runtimeFingerprint)
	if argsFingerprint == "" || runtimeFingerprint == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(argsFingerprint + "\x00" + runtimeFingerprint))
	return fmt.Sprintf("%x", sum[:])
}

// explainCheckpointRejection appends the runtime state a rejection needs to be
// actionable. Most violations are fixed by editing the field the message
// names, so their text is already complete; a check whose remedy depends on
// what the runtime currently resolves gets that list appended instead of
// leaving the model to guess. It cannot live in the tool description, which is
// baked at registration and participates in the tool-surface hash and the
// frozen prompt prefix, while the resolvable IDs change every generation.
func (a *MainAgent) explainCheckpointRejection(err error) error {
	if _, ok := errors.AsType[checkpointClaimNeedsEvidenceError](err); !ok {
		return err
	}
	return fmt.Errorf("%w; %s", err, a.resolvableEvidenceHint())
}
