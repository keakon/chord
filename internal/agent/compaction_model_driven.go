package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
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
	// modelDrivenAnchorMaxRunes caps the latest-request anchor in the
	// deterministic checkpoint. It matches the structured-fallback summary's
	// anchor cap so both compaction paths preserve the same amount of the
	// latest user request.
	modelDrivenAnchorMaxRunes = 260
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

// modelDrivenBarrierSnapshot is the immutable event-loop capture handed to the
// model-driven compaction worker. The worker never reads live MainAgent state:
// evidence (event-loop-only by contract), todos, SubAgents and background
// objects are all captured at the tool-batch barrier so a checkpoint can never
// mix barrier-time facts with post-barrier updates.
type modelDrivenBarrierSnapshot struct {
	snapshot          []message.Message
	evidenceItems     []evidenceItem
	todos             []tools.TodoItem
	subAgents         []SubAgentInfo
	backgroundObjects []recovery.BackgroundObjectState
	maxTokens         int
	sessionDir        string
	originalRequest   string
	responsesState    *llm.ResponsesTurnState
	// prepareReducedRequest is the request-reduction closure captured at the
	// barrier from the (isolated) reduction scratch agent. Capturing only the
	// function — not the *MainAgent — keeps the worker from ever calling other
	// agent methods: it can only reduce a message slice. The closure still runs
	// on the scratch agent, so it never reads live MainAgent state.
	prepareReducedRequest func([]message.Message) []message.Message
	// fixedRequestTokens is the estimated per-request fixed surface (system
	// prompt + tool definitions) that is paid on both sides of a reset. It is
	// included in the low-gain ratio base so the 10% gate is measured against
	// the full prepared request, not just the message payload.
	fixedRequestTokens int
	// queuedUserMessages are the user messages waiting in the queue that the
	// next request would merge into the conversation (slash commands that
	// stay deferred are excluded). They are part of the real prepared surface
	// on both sides of a reset, so the low-gain gate counts them in its
	// denominator instead of understating the current side.
	queuedUserMessages []message.Message
	// postResetFixedRequestTokens is the fixed surface the next request pays
	// after the reset applies: applyCompactionDraft runs
	// forceFullMCPToolInjection, which drops cache-friendly mounts and
	// re-injects the full MCP tool surface at top level. A cache-friendly
	// barrier captures only the mounted subset for fixedRequestTokens, so the
	// projected side must use the larger full-injection surface instead of
	// being silently understated.
	postResetFixedRequestTokens int
	// currentRequestBatch is the main-model request batch at the barrier
	// (currentRequestBatch semantics: the request-batch counter falling back
	// to maxRequestBatch(messages) after a process restart). The interval and
	// cooldown verdicts and their settlement recording all use this one
	// number, captured once on the event loop.
	currentRequestBatch     uint64
	runtimeStateFingerprint string
	// lastModelDrivenApplyBatch / lastModelDrivenSkipBatch /
	// lastModelDrivenSkipReason are the event-loop-owned apply/skip records at
	// the barrier. The worker decides the interval and cooldown verdicts from
	// these snapshots; the settlement writes the verdict batch back on the
	// event loop.
	lastModelDrivenApplyBatch uint64
	lastModelDrivenSkipBatch  uint64
	lastModelDrivenSkipReason string
	// promptCacheCapable reports whether the running model supports prompt
	// caching, so the low-gain gate can charge the projected side the cache
	// rewrite cost of replacing the archived prefix.
	promptCacheCapable bool
	// calibratedRatio is the usage-calibrated tokens/bytes ratio snapshot at
	// the barrier. Preflight and the post-export re-check both estimate
	// through this snapshot (bundle.estimateTokens) so the two sides cannot
	// drift apart if the live ctxMgr calibration changes between them.
	calibratedRatio float64
	// retainRecentTokens is the estimated-token budget for the real recent
	// user messages kept verbatim inside the checkpoint (zero falls back to
	// the built-in default). Read from the merged config on the event loop
	// like every other config read; the worker never touches live config.
	retainRecentTokens int
	// archiveMeta is the session/model identity the history export stamps,
	// captured on the event loop at the barrier (exportCompactionHistory runs
	// in the worker and reads only this bundle).
	archiveMeta compactionArchiveMeta
	// lastPreparedTurnID / lastPreparedSource / lastPreparedPrefix are the
	// most recent request surface this turn actually sent to the provider
	// (rememberPreparedLLMRequest): the reduced prefix and the original source
	// copy it was reduced from. When the barrier snapshot still begins with
	// that source unchanged, the preflight reuses the sent prefix as the
	// current-side baseline — the provider already saw it, so the estimate is
	// exact for the head — instead of re-running a full scratch reduction.
	// Captured under loopReductionMu on the event loop.
	lastPreparedTurnID uint64
	lastPreparedSource []message.Message
	lastPreparedPrefix []message.Message
}

// estimateTokens estimates the input tokens of a message slice on the barrier
// calibration snapshot. It is the only estimator the model-driven worker uses,
// so the preflight and its post-export re-check share one baseline.
func (b modelDrivenBarrierSnapshot) estimateTokens(messages []message.Message) int {
	return ctxmgr.EstimateMessagesTokensWithRatio(messages, b.calibratedRatio)
}

// modelDrivenPreflightStats carries the low-gain preflight estimates from the
// worker to the event loop so skip and applied lifecycle events can record
// them. Zero for drafts that never ran a preflight (e.g. "not enough history").
type modelDrivenPreflightStats struct {
	CurrentTokens   int
	ProjectedTokens int
	SavedTokens     int
	SavedRatioPct   int
	// CurrentBytes / ProjectedBytes are the raw byte sizes of the reduced
	// request surface on each side, using the same accounting as the calibrated
	// token estimator's denominator. They let telemetry report the request
	// surface size independently of the token estimate.
	CurrentBytes       int
	ProjectedBytes     int
	CheckpointBytes    int
	AnchorBytes        int
	HistoryMapBytes    int
	ContinuationTokens int
	// CurrentSource records which current-side baseline produced the preflight
	// estimate: "last_prepared" (the most recent request surface this turn
	// actually sent to the provider, reused when the head is unchanged) or
	// "scratch" (a full reduction re-scan). Telemetry compares the estimate
	// against the provider's own input usage to gauge both baselines.
	CurrentSource string
	// CacheRebuildCost is the prompt-cache rewrite cost of the projected
	// prefix for prompt-cache-capable sessions (rewritten-prefix tokens ×
	// amortized write−read multiplier). Telemetry only: the low-gain gate
	// compares raw surface savings and never subtracts this per-provider
	// billing weight.
	CacheRebuildCost int
}

// ---------------------------------------------------------------- accept ---

// validateCompactContextResult performs the control-plane checks that must
// happen before the tool result is written: single tool call in the declaring
// response, no active/ready compaction, persistence healthy, and re-validated
// arguments (trimmed, token-budgeted, lexically safe state_files). On success
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
		ProjectRoot:                a.ProjectRoot,
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
	if err := a.validateModelDrivenEvidenceRefs(args.EvidenceRefs); err != nil {
		return "", err
	}
	for claim, refs := range args.ClaimEvidence {
		if err := a.validateModelDrivenEvidenceRefs(refs); err != nil {
			return "", fmt.Errorf("claim_evidence %q: %w", claim, err)
		}
	}
	if err := validateModelDrivenCheckpointKind(args); err != nil {
		return "", err
	}
	if err := a.validateObservedClaimEvidence(args); err != nil {
		return "", err
	}
	if err := a.validateCommittedEvidence(args); err != nil {
		return "", err
	}
	a.pendingModelDriven = &modelDrivenCheckpointRequest{
		ToolCallID: callID,
		Args:       args,
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

func (a *MainAgent) validateModelDrivenEvidenceRefs(refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	known := make(map[string]evidenceItem)
	for _, item := range a.evidence.snapshot() {
		known[evidenceItemID(item)] = item
	}
	for _, ref := range refs {
		item, ok := known[ref]
		if !ok {
			return fmt.Errorf("compact_context evidence_refs contains unknown evidence ID %q", ref)
		}
		if item.Validity == evidenceValidityInvalidated || item.Validity == evidenceValidityUnavailable {
			return fmt.Errorf("compact_context evidence_refs contains %s evidence %q", item.Validity, ref)
		}
	}
	return nil
}

func (a *MainAgent) validateObservedClaimEvidence(args tools.CompactContextArgs) error {
	byID := make(map[string]evidenceItem)
	for _, item := range a.evidence.snapshot() {
		byID[evidenceItemID(item)] = item
	}
	for claim, kind := range args.ClaimKinds {
		if kind != "observed" {
			continue
		}
		for _, ref := range args.ClaimEvidence[claim] {
			item, ok := byID[ref]
			if !ok {
				return fmt.Errorf("observed claim %q references unknown evidence %q", claim, ref)
			}
			if item.Kind == evidenceToolError || item.Kind == evidenceDoneRejected {
				return fmt.Errorf("observed claim %q cannot use %s evidence %q", claim, item.Kind, ref)
			}
			if item.Validity == evidenceValidityInvalidated || item.Validity == evidenceValidityUnavailable {
				return fmt.Errorf("observed claim %q cannot use %s evidence %q", claim, item.Validity, ref)
			}
		}
	}
	return nil
}

func (a *MainAgent) validateCommittedEvidence(args tools.CompactContextArgs) error {
	if args.CheckpointKind != "committed" && args.StageStatus != "completed" {
		return nil
	}
	byID := make(map[string]evidenceItem)
	for _, item := range a.evidence.snapshot() {
		byID[evidenceItemID(item)] = item
	}
	for _, ref := range args.EvidenceRefs {
		item, ok := byID[ref]
		if !ok {
			continue
		}
		if item.Kind == evidenceToolError || item.Kind == evidenceDoneRejected {
			return fmt.Errorf("committed checkpoint cannot use %s evidence %q", item.Kind, ref)
		}
	}
	return nil
}

func validateModelDrivenCheckpointKind(args tools.CompactContextArgs) error {
	if args.CheckpointKind == "committed" && args.StageStatus != "completed" {
		return fmt.Errorf("committed compact_context requires stage_status=completed")
	}
	if args.CheckpointKind == "committed" && len(args.EvidenceRefs) == 0 {
		return fmt.Errorf("committed compact_context requires at least one evidence_refs entry")
	}
	if args.StageStatus == "completed" && len(args.EvidenceRefs) == 0 {
		return fmt.Errorf("completed compact_context stage requires evidence_refs")
	}
	for claim, kind := range args.ClaimKinds {
		if kind == "observed" && len(args.ClaimEvidence[claim]) == 0 {
			return fmt.Errorf("claim_kinds %q is observed but has no claim_evidence", claim)
		}
		if kind == "observed" {
			refs := make(map[string]struct{}, len(args.EvidenceRefs))
			for _, ref := range args.EvidenceRefs {
				refs[ref] = struct{}{}
			}
			for _, ref := range args.ClaimEvidence[claim] {
				if _, ok := refs[ref]; !ok {
					return fmt.Errorf("observed claim %q references evidence %q that is not listed in evidence_refs", claim, ref)
				}
			}
		}
	}
	return nil
}

// ------------------------------------------------------------------ barrier ---

// maybeStartModelDrivenBarrier is called at the tool-batch end, after batch
// hooks and before Handoff/Done control flow. When a model-driven checkpoint
// is pending it captures the immutable snapshot bundle, resolves the fixed
// archival profile, and starts the worker with a model-driven continuation
// that resumes the same turn. It returns true when a barrier was started; the
// caller must then return without calling beginMainLLMAfterPreparation.
func (a *MainAgent) maybeStartModelDrivenBarrier() bool {
	if a.pendingModelDriven == nil {
		return false
	}
	req := a.pendingModelDriven
	a.pendingModelDriven = nil
	a.transitionModelDrivenProposal(modelDrivenProposalPreparing, "preparing durable checkpoint")
	if a.turn == nil {
		log.Warn("model-driven checkpoint pending but turn is gone; dropping request")
		return false
	}
	snapshot := a.ctxMgr.Snapshot()
	bundle := a.captureModelDrivenBarrierSnapshot(snapshot)
	// Policy pre-verdict on the event loop: the interval
	// and same-reason cooldown verdicts are deterministic from the bundle
	// alone, so a request they reject must settle before the compaction slot
	// is touched. A running or ready automatic compaction keeps its paid-for
	// draft (a usage-driven worker or ready draft is only discarded when the
	// model-driven request actually proceeds), and the worker never starts —
	// the settle is synchronous with started + terminal events on the same
	// plan id, and the caller continues into beginMainLLMAfterPreparation
	// without a model-driven continuation. The worker re-checks the same
	// verdict defensively for paths that could reach it without this gate.
	if reason, skipReason, skip := a.modelDrivenIntervalCooldownVerdict(bundle); skip {
		planID, target := a.nextCompactionPlan()
		a.recordCompactionLifecycleEvent("started", map[string]string{
			"trigger":        compactionTriggerModelDriven.analyticsName(),
			"plan_id":        strconv.FormatUint(planID, 10),
			"turn_id":        strconv.FormatUint(target.turnID, 10),
			"message_count":  strconv.Itoa(len(bundle.snapshot)),
			"state_file_cnt": strconv.Itoa(len(req.Args.StateFiles)),
			"max_tokens":     strconv.Itoa(bundle.maxTokens),
		})
		a.emitToTUI(CompactionStatusEvent{Status: CompactionStatusStarted, Trigger: string(compactionTriggerModelDriven), PlanID: strconv.FormatUint(planID, 10), Synthetic: true})
		draft := modelDrivenSkipDraft(planID, target, reason, skipReason, modelDrivenPolicySkipRecordBatch(skipReason, bundle.currentRequestBatch), nil)
		a.settleModelDrivenSkip(draft)
		a.appendModelDrivenContinuationNotice()
		return false
	}
	planID, target := a.nextCompactionPlan()
	continuation := continuationPlan{
		kind:      compactionResumeModelDriven,
		turnID:    a.turn.ID,
		turnEpoch: a.turn.Epoch,
	}
	// The model's explicit checkpoint overrides any automatic compaction
	// currently owning the slot (usage-driven async started by a threshold
	// crossing, an oversize recovery, or a ready draft waiting for the
	// continuation barrier): the model chose this boundary on purpose, so its
	// checkpoint wins over the runtime's background summary. The single slot
	// cannot host two workers, so the running compaction is discarded first;
	// its late terminal event is dropped by the plan-id stale guard in
	// handleCompactionReady / handleCompactionFailed.
	a.discardCompactionForModelOverride()
	a.startModelDrivenCompactionAsync(bundle, planID, target, continuation, req)
	return true
}

// discardCompactionForModelOverride cancels the automatic compaction currently
// owning the slot (usage-driven async started by a threshold crossing, an
// oversize recovery, or a ready draft waiting for the continuation barrier) so
// a freshly accepted model-driven checkpoint can take over. A ready draft is
// discarded together with its orphan history files, the worker context is
// cancelled, and the state is reset. No terminal TUI event is emitted here:
// the model-driven worker that follows immediately emits its own Started
// event, so a cancelled flash would be misleading. A model-driven worker
// already owning the slot is never overridden — a second model-driven barrier
// cannot legitimately occur while the first checkpoint is pending, because the
// model-driven continuation freezes the main request until it settles.
func (a *MainAgent) discardCompactionForModelOverride() {
	if !a.compactionState.isRunning() {
		return
	}
	if a.compactionState.continuation.kind == compactionResumeModelDriven {
		return
	}
	if readyDraft := a.compactionState.readyDraft; readyDraft != nil {
		a.compactionState.readyDraft = nil
		cleanupOrphanCompactionFiles(readyDraft.AbsHistoryPath)
	}
	a.recordCompactionPolicyAnalyticsEvent("auto_compact_overridden_by_model_checkpoint")
	if a.compactionState.cancel != nil {
		a.compactionState.cancel()
	}
	a.resetCompactionState()
}

// captureModelDrivenBarrierSnapshot snapshots every piece of event-loop-owned
// state the model-driven worker may need, on the event loop. The returned
// bundle is immutable; the worker only ever reads it.
func (a *MainAgent) captureModelDrivenBarrierSnapshot(snapshot []message.Message) modelDrivenBarrierSnapshot {
	lastPreparedTurnID, lastPreparedSource, lastPreparedPrefix := a.captureLastPreparedSurfaceForPreflight()
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		evidenceItems:               a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens()),
		todos:                       a.GetTodos(),
		subAgents:                   a.taskInfosForCompaction(),
		backgroundObjects:           spawnStatesForSnapshot(),
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		originalRequest:             a.captureOriginalFirstUserHint(),
		responsesState:              a.currentTurnResponsesState(),
		prepareReducedRequest:       a.compactionReductionScratch().prepareMessagesForLLM,
		fixedRequestTokens:          a.estimateFixedRequestTokens(),
		queuedUserMessages:          a.pendingUserMessagesForPreflight(),
		postResetFixedRequestTokens: a.estimatePostResetFixedRequestTokens(),
		currentRequestBatch:         a.currentRequestBatch(snapshot),
		lastModelDrivenApplyBatch:   a.lastModelDrivenApplyBatch,
		lastModelDrivenSkipBatch:    a.lastModelDrivenSkipBatch,
		lastModelDrivenSkipReason:   a.lastModelDrivenSkipReason,
		promptCacheCapable:          a.currentModelPromptCacheCapable(),
		calibratedRatio:             a.ctxMgr.CalibratedRatio(),
		retainRecentTokens:          a.effectiveCompactionRetainRecentTokens(),
		archiveMeta:                 a.captureCompactionArchiveMeta(),
		lastPreparedTurnID:          lastPreparedTurnID,
		lastPreparedSource:          lastPreparedSource,
		lastPreparedPrefix:          lastPreparedPrefix,
	}
	bundle.runtimeStateFingerprint = modelDrivenRuntimeStateFingerprint(bundle)
	return bundle
}

func modelDrivenRuntimeStateFingerprint(bundle modelDrivenBarrierSnapshot) string {
	payload, _ := json.Marshal(struct {
		Todos      []tools.TodoItem
		SubAgents  []SubAgentInfo
		Background []recovery.BackgroundObjectState
		Queued     []message.Message
		Evidence   []evidenceItem
	}{bundle.todos, bundle.subAgents, bundle.backgroundObjects, bundle.queuedUserMessages, bundle.evidenceItems})
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum[:])
}

// captureLastPreparedSurfaceForPreflight snapshots the most recent sent
// request surface under loopReductionMu — the remember path writes these
// fields from the LLM goroutine under the same lock, so reading them on the
// event loop must hold it too.
func (a *MainAgent) captureLastPreparedSurfaceForPreflight() (turnID uint64, source, prefix []message.Message) {
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	return a.lastPreparedLLMTurnID,
		append([]message.Message(nil), a.lastPreparedLLMShapeSource...),
		cloneMessageSliceForRequestShape(a.lastPreparedLLMRequestPrefix)
}

// currentModelPromptCacheCapable reports whether the running model supports
// prompt caching, so the low-gain gate can charge the projected side the
// cache-rewrite cost of replacing the archived prefix. Read on the event loop
// at the barrier; the verdict rides inside the snapshot.
func (a *MainAgent) currentModelPromptCacheCapable() bool {
	if a == nil {
		return false
	}
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	if client == nil {
		return false
	}
	return client.SupportsAnthropicPromptCache(client.NextRequestModelRef())
}

// pendingUserMessagesForPreflight converts the queued user messages that the
// next request would merge into plain user messages for the low-gain gate's
// denominator. The deferred slash commands (idle-only /loop*, /resume*, /new,
// /mcp*) are excluded, mirroring consumePendingUserMessagesForRequest.
func (a *MainAgent) pendingUserMessagesForPreflight() []message.Message {
	if len(a.pendingUserMessages) == 0 {
		return nil
	}
	var out []message.Message
	for _, p := range a.pendingUserMessages {
		c := strings.TrimSpace(pendingUserMessageText(p))
		if c == "/resume" || strings.HasPrefix(c, "/resume ") || c == "/new" || c == "/mcp" || strings.HasPrefix(c, "/mcp ") || isLoopSlashCommand(c) {
			continue
		}
		if m, ok := a.pendingUserMessageToConversationMessage(p); ok {
			out = append(out, m)
		}
	}
	return out
}

// estimateFixedRequestTokens estimates the per-request fixed surface paid on
// both sides of a reset: the installed system prompt plus the tool
// definitions. Read on the event loop; the value rides inside the barrier
// snapshot so the worker never touches the live prompt.
func (a *MainAgent) estimateFixedRequestTokens() int {
	a.llmMu.RLock()
	sysPrompt := a.installedSysPrompt
	a.llmMu.RUnlock()
	return llm.EstimateRequestInputTokens(sysPrompt, nil, a.mainLLMToolDefinitions())
}

// estimatePostResetFixedRequestTokens estimates the fixed request surface the
// next request pays after a model-driven reset applies. applyCompactionDraft
// calls forceFullMCPToolInjection, which clears cache-friendly mounts and the
// frozen tool surface so mainLLMToolDefinitions would return the full visible
// tool set. The full-injection surface is computed directly here (it does not
// depend on mount state) so the projected side of the low-gain gate reflects
// the larger post-reset tool surface rather than the cache-friendly subset
// captured for the current side.
func (a *MainAgent) estimatePostResetFixedRequestTokens() int {
	a.llmMu.RLock()
	sysPrompt := a.installedSysPrompt
	a.llmMu.RUnlock()
	return llm.EstimateRequestInputTokens(sysPrompt, nil, llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools()))
}

// startModelDrivenCompactionAsync mirrors startCompactionAsyncWithContinuation
// but pins the archival profile, passes the model-authored args and the
// immutable barrier snapshot, and skips the summarization model call entirely.
func (a *MainAgent) startModelDrivenCompactionAsync(bundle modelDrivenBarrierSnapshot, planID uint64, target compactionTarget, continuation continuationPlan, req *modelDrivenCheckpointRequest) {
	a.recordCompactionLifecycleEvent("started", map[string]string{
		"trigger":        compactionTriggerModelDriven.analyticsName(),
		"request_id":     a.modelDrivenProposal.requestID,
		"plan_id":        strconv.FormatUint(planID, 10),
		"turn_id":        strconv.FormatUint(target.turnID, 10),
		"message_count":  strconv.Itoa(len(bundle.snapshot)),
		"state_file_cnt": strconv.Itoa(len(req.Args.StateFiles)),
		"max_tokens":     strconv.Itoa(bundle.maxTokens),
	})
	// The before-compress hook fires from the barrier like the usage-driven
	// path, so integration hooks observe model-driven checkpoints too.
	a.fireBeforeCompressHook(bundle.snapshot, false)
	profile := compactionProfileArchival
	headSplit := compactionHeadSplitForProfile(a.ctxMgr, profile, bundle.snapshot, bundle.maxTokens)

	ctx, cancel := context.WithTimeout(a.parentCtx, compactionDraftTimeout)
	ctx = llm.WithResponsesTurnState(ctx, bundle.responsesState)
	a.beginCompactionState(planID, target, compactionTriggerModelDriven, continuation, headSplit, cancel)
	if a.walltime != nil {
		a.walltime.startCompactionAt(planID, a.currentAgentName(), target.turnID)
	}

	// The barrier freezes this turn's next request until the checkpoint
	// settles, so there is no live foreground work left to protect: the tool
	// batch that just ended left the shared main activity slot on
	// "executing N tools" (mainSlotForeground held) and nobody else releases
	// it. Hand the slot to compaction — a plain emitCompactionSlotActivity
	// would be swallowed by the foreground guard and the status bar would show
	// the stale executing state for the whole worker run (same fix as the
	// model-downshift deferral path).
	a.handoffMainActivityToCompaction()
	a.emitToTUI(CompactionStatusEvent{Status: CompactionStatusStarted, Trigger: string(compactionTriggerModelDriven), PlanID: strconv.FormatUint(planID, 10)})
	a.compactionWg.Add(1)
	go func(ctx context.Context, bundle modelDrivenBarrierSnapshot, planID uint64, target compactionTarget, headSplit int, req *modelDrivenCheckpointRequest) {
		defer a.compactionWg.Done()
		defer cancel()

		draftDone := make(chan struct{})
		defer close(draftDone)
		go func() {
			timer := time.NewTimer(compactionDraftTimeout + compactionWatchdogGrace)
			defer timer.Stop()
			select {
			case <-draftDone:
			case <-timer.C:
				log.Errorf("model-driven compaction draft watchdog fired plan_id=%v head_split=%v", planID, headSplit)
				a.sendEvent(Event{Type: EventCompactionFailed, Payload: &compactionFailure{planID: planID, target: target, err: errCompactionWatchdog}})
			}
		}()

		draft, err := a.produceModelDrivenDraftAsync(ctx, bundle, planID, target, headSplit, req)
		if err != nil {
			a.sendEvent(Event{Type: EventCompactionFailed, Payload: &compactionFailure{planID: planID, target: target, err: err, absHistoryPath: getAbsHistoryPathFromDraft(draft)}})
			return
		}
		if draft != nil {
			draft.PlanID = planID
			draft.Target = target
			draft.HeadSplit = headSplit
			draft.RuntimeGeneration = bundle.currentRequestBatch
			draft.RuntimeStateFingerprint = bundle.runtimeStateFingerprint
			draft.ModelDrivenRequestID = a.modelDrivenProposal.requestID
		}
		a.sendEvent(Event{Type: EventCompactionReady, Payload: draft})
	}(ctx, bundle, planID, target, headSplit, req)
}

// ------------------------------------------------------------------ draft ---

// produceModelDrivenDraftAsync builds the checkpoint draft without any
// summarization model call. Steps mirror produceCompactionDraftAsync: size
// gates, evidence/profile application, anchors, history export, deterministic
// checkpoint construction. The low-gain preflight runs before history export
// so a skip never leaves orphan history/metadata files. All inputs come from
// the immutable barrier snapshot; no live MainAgent state is read here.
func (a *MainAgent) produceModelDrivenDraftAsync(ctx context.Context, bundle modelDrivenBarrierSnapshot, planID uint64, target compactionTarget, headSplit int, req *modelDrivenCheckpointRequest) (*compactionDraft, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	snapshot := bundle.snapshot
	if len(snapshot) < 4 {
		return &compactionDraft{
			Skip:        true,
			InfoMessage: "Not enough history to compact.",
			Manual:      false,
			PlanID:      planID,
			Target:      target,
		}, nil
	}
	if headSplit <= 0 {
		return &compactionDraft{
			Skip:         true,
			SmallContext: true,
			InfoMessage:  "Cannot find a safe compaction boundary; nothing to compact.",
			PlanID:       planID,
			Target:       target,
		}, nil
	}

	headSnapshot := snapshot[:headSplit]
	// Model-driven always uses the archival profile: no recent raw tail is
	// retained, and only constraint/error evidence survives (pure function of
	// the barrier snapshot — no live manager access).
	evidenceItems := filterCompactionEvidenceForArchival(bundle.evidenceItems)
	head, evidenceMsgs := splitMessagesForCompactionWithSelections(headSnapshot, nil, evidenceItems)
	if len(head) == 0 {
		return &compactionDraft{
			Skip:         true,
			SmallContext: true,
			InfoMessage:  "Current context is already small enough; nothing to compact.",
			PlanID:       planID,
			Target:       target,
		}, nil
	}

	// Apply-interval and same-reason skip-cooldown verdicts. Both
	// are decided before the low-gain preflight: an interval skip never enters
	// preflight (savings are meaningless while the apply spacing has not
	// elapsed) and a cooldown short-circuit avoids re-running
	// prepareMessagesForLLM for a request whose outcome cannot change. The
	// verdict batch/reason ride on the draft so the event-loop settlement
	// records exactly the numbers the worker decided on.
	if reason, skipReason, ok := a.modelDrivenIntervalCooldownVerdict(bundle); ok {
		return modelDrivenSkipDraft(planID, target, reason, skipReason, modelDrivenPolicySkipRecordBatch(skipReason, bundle.currentRequestBatch), nil), nil
	}

	// Low-gain preflight BEFORE history export. A skip here must not produce
	// orphan history-*.md / metadata / backup files. The preflight stats ride
	// on the returned draft so the event-loop settlement records them once.
	checkpointBuilder := a.newModelDrivenCheckpointBuilder(bundle, snapshot, headSplit, req)
	skipReason, skip, preflight := a.modelDrivenLowGainPreflight(bundle, headSplit, snapshot, checkpointBuilder)
	if skip {
		return modelDrivenSkipDraft(planID, target, skipReason, modelDrivenSkipReasonLowGain, bundle.currentRequestBatch, &preflight), nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	index, err := a.nextCompactionIndexForAgent(bundle.archiveMeta.sessionDir)
	if err != nil {
		return nil, fmt.Errorf("determine compaction index: %w", err)
	}
	absHistoryPath, sourceRefs, sourceFingerprint, err := a.exportCompactionHistory(head, index, evidenceItemTopics(evidenceItems), bundle.archiveMeta)
	if err != nil {
		return nil, fmt.Errorf("export compacted history: %w", err)
	}
	absHistoryMetaPath := compactionHistoryMetaPath(absHistoryPath)
	transactionID := fmt.Sprintf("%d-%d", planID, index)
	if err := writeCompactionTransactionManifest(bundle.archiveMeta.sessionDir, compactionTransactionManifest{TransactionID: transactionID, ProposalID: req.ToolCallID, SourceFingerprint: sourceFingerprint, ArchivePath: absHistoryPath, ArchiveMetaPath: absHistoryMetaPath, TranscriptIndex: index, Status: compactionTransactionPrepared}); err != nil {
		return nil, fmt.Errorf("write compaction transaction: %w", err)
	}
	// Any failure after the archive is written must remove it: a cancelled or
	// errored worker otherwise leaves orphan history-*.md / .status.json files
	// that no draft path carries (getAbsHistoryPathFromDraft sees a nil draft
	// and the generic cancellation cleanup cannot find them).
	historyCommitted := false
	defer func() {
		if !historyCommitted {
			cleanupOrphanCompactionFiles(absHistoryPath)
			_ = os.Remove(compactionTransactionManifestPath(bundle.archiveMeta.sessionDir, transactionID))
		}
	}()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// The post-export build refines the checkpoint: the history map references
	// the just-written archive file, so the projected checkpoint is slightly
	// larger than the pre-export preflight estimate. Re-run the low-gain gate
	// on the refined numbers — a threshold-edge reset must not be approved on
	// the smaller estimate. A skip here cleans up the exported archive (the
	// pre-export gate exists to keep this the rare path).
	checkpointContent, contentStats := checkpointBuilder.render(absHistoryPath)
	// The build is the worker's heaviest step; honour a cancellation that
	// landed during it so the deferred cleanup above removes the archive
	// instead of producing a draft the event loop would discard.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	preflight.CheckpointBytes = contentStats.CheckpointBytes
	preflight.AnchorBytes = contentStats.AnchorBytes
	preflight.HistoryMapBytes = contentStats.HistoryMapBytes
	preflight.ContinuationTokens = contentStats.ContinuationTokens
	projected := []message.Message{{Role: message.RoleUser, Content: checkpointContent, IsCompactionSummary: true}}
	projected = append(projected, snapshot[headSplit:]...)
	projected = append(projected, bundle.queuedUserMessages...)
	projectedTokens := bundle.estimateTokens(projected) + bundle.postResetFixedRequestTokens + modelDrivenPostResetOverlayTokens
	saved := preflight.CurrentTokens - projectedTokens
	preflight.ProjectedTokens = projectedTokens
	preflight.SavedTokens = saved
	preflight.ProjectedBytes = ctxmgr.EstimateMessagesBytes(projected) + (bundle.postResetFixedRequestTokens+modelDrivenPostResetOverlayTokens)*3
	if preflight.CurrentTokens > 0 {
		preflight.SavedRatioPct = saved * 100 / preflight.CurrentTokens
	}
	if reason, skip := modelDrivenLowGainCheck(preflight.CurrentTokens, saved); skip {
		return modelDrivenSkipDraft(planID, target, reason, modelDrivenSkipReasonLowGain, bundle.currentRequestBatch, &preflight), nil
	}
	contextSummaryMsg := message.Message{
		Role:                  "user",
		Content:               checkpointContent,
		IsCompactionSummary:   true,
		CompactionSummaryMode: compactionSummaryModeModelDriven,
	}

	newMessages := []message.Message{contextSummaryMsg}
	historyCommitted = true
	return &compactionDraft{
		PlanID:                planID,
		Target:                target,
		NewMessages:           newMessages,
		HeadSplit:             headSplit,
		Index:                 index,
		AbsHistoryPath:        absHistoryPath,
		AbsHistoryMetaPath:    absHistoryMetaPath,
		SourceRefs:            sourceRefs,
		SourceFingerprint:     sourceFingerprint,
		TransactionID:         transactionID,
		TransactionSessionDir: bundle.archiveMeta.sessionDir,
		SummaryMode:           compactionSummaryModeModelDriven,
		Backend:               config.CompactionPresetGeneric,
		Profile:               string(compactionProfileArchival),
		ModelRef:              "model_declared",
		Manual:                false,
		ArchivedCount:         len(head),
		EvidenceCount:         len(evidenceItems),
		EvidenceArtifacts:     len(evidenceMsgs),
		ModelDrivenPreflight:  &preflight,
	}, nil
}

// ------------------------------------------------------------- preflight ---

// modelDrivenCurrentSurfaceBaseline returns the prepared current-side message
// surface for the low-gain preflight plus a provenance label for telemetry.
// When the snapshot is still an append-only extension of the source messages
// of this turn's most recent sent request, that request's reduced prefix is
// reused (the provider already saw it, so the estimate is exact for the head)
// and the appended tail is added unreduced; otherwise the scratch reduction
// re-scans the whole snapshot as an approximation of the next real request.
func (a *MainAgent) modelDrivenCurrentSurfaceBaseline(bundle modelDrivenBarrierSnapshot, snapshot []message.Message) ([]message.Message, string) {
	if bundle.lastPreparedTurnID != 0 && len(bundle.lastPreparedSource) > 0 && len(bundle.lastPreparedPrefix) > 0 {
		if reusableMessagePrefixLen(bundle.lastPreparedSource, snapshot) == len(bundle.lastPreparedSource) {
			out := cloneMessageSliceForRequestShape(bundle.lastPreparedPrefix)
			out = append(out, snapshot[len(bundle.lastPreparedSource):]...)
			return out, "last_prepared"
		}
	}
	return bundle.prepareReducedRequest(snapshot), "scratch"
}

// modelDrivenLowGainPreflight estimates the prepared request surface with and
// without the reset. The current side is the next real request's reduced
// message surface; the projected side is the complete checkpoint (wrapper,
// history map, anchors, evidence artifact, display marker and model-driven
// copy) plus the preserved live tail, plus a conservative fixed allowance for
// the request-local overlays a durable rewrite re-injects. Both sides carry
// the fixed system-prompt + tool-definition surface, so the ratio gate is
// measured against the full request. Uncertain post-reset costs are
// overestimated, never ignored, so a reset is only skipped for clearly
// insufficient gain.
func (a *MainAgent) modelDrivenLowGainPreflight(bundle modelDrivenBarrierSnapshot, headSplit int, snapshot []message.Message, builder *modelDrivenCheckpointBuilder) (string, bool, modelDrivenPreflightStats) {
	currentSurface, currentSource := a.modelDrivenCurrentSurfaceBaseline(bundle, snapshot)
	// Queued user messages are merged after reduction on the real request
	// path, so they append to the prepared surface on both sides.
	currentSurface = append(currentSurface, bundle.queuedUserMessages...)
	checkpointContent, preflight := builder.render("")
	preflight.CurrentSource = currentSource
	projected := []message.Message{{Role: message.RoleUser, Content: checkpointContent, IsCompactionSummary: true}}
	projected = append(projected, snapshot[headSplit:]...)
	projected = append(projected, bundle.queuedUserMessages...)

	currentTokens := bundle.estimateTokens(currentSurface) + bundle.fixedRequestTokens
	// The projected side pays the post-reset fixed surface: after apply, the
	// cache-friendly MCP mount is dropped for full top-level injection, so the
	// frozen/mounted subset used for the current side would understate it.
	projectedTokens := bundle.estimateTokens(projected) + bundle.postResetFixedRequestTokens + modelDrivenPostResetOverlayTokens
	saved := currentTokens - projectedTokens
	preflight.CurrentTokens = currentTokens
	preflight.ProjectedTokens = projectedTokens
	preflight.SavedTokens = saved
	preflight.CurrentBytes = ctxmgr.EstimateMessagesBytes(currentSurface) + bundle.fixedRequestTokens*3
	preflight.ProjectedBytes = ctxmgr.EstimateMessagesBytes(projected) + (bundle.postResetFixedRequestTokens+modelDrivenPostResetOverlayTokens)*3
	if currentTokens > 0 {
		preflight.SavedRatioPct = saved * 100 / currentTokens
	}
	// Prompt-cache rebuild cost: after apply the checkpoint prefix must be
	// cache-written once (write ≈ 1.25×) where a kept prefix would only have
	// been cache-read (≈ 0.1×). The rewritten prefix is the projected surface,
	// not the archived head, and the one-time delta is amortized over the
	// minimum apply interval. The cost is recorded on the preflight stats for
	// cost telemetry only; the low-gain gates below compare raw
	// surface savings, keeping pure surface semantics — the rebuild delta is a
	// per-provider billing weight, not a token the model attends to, and
	// whether it systematically eats the gains is answered by the telemetry
	// instead of being pre-decided inside the gate.
	var cacheRebuildCost int
	if bundle.promptCacheCapable && headSplit > 0 {
		cacheRebuildCost = modelDrivenCacheRebuildCost(projectedTokens)
	}
	preflight.CacheRebuildCost = cacheRebuildCost
	if reason, skip := modelDrivenLowGainCheck(currentTokens, saved); skip {
		return reason, true, preflight
	}
	return "", false, preflight
}

// modelDrivenLowGainCheck applies the fixed low-gain gates to a savings
// estimate and returns the skip reason when either gate fails. Both the
// pre-export preflight and the post-export re-check share it so the refined
// checkpoint cannot be approved on a stale estimate. The gates compare raw
// surface savings only: the prompt-cache rebuild cost is recorded on the
// preflight stats for telemetry but deliberately NOT subtracted here, so the
// gate keeps a pure surface semantics (the one-off cache rewrite of a kept
// prefix is a per-provider billing weight, not a token the model attends to);
// whether cache rebuilds systematically eat the gains is answered by the
// cost telemetry instead of being pre-decided inside the gate.
func modelDrivenLowGainCheck(currentTokens, saved int) (string, bool) {
	if saved < modelDrivenLowGainMinTokens || saved < int(float64(currentTokens)*modelDrivenLowGainMinRatio) {
		return fmt.Sprintf("projected savings %d tokens is below the low-gain gate (%d tokens and %d%% of the prepared surface)", saved, modelDrivenLowGainMinTokens, int(modelDrivenLowGainMinRatio*100)), true
	}
	return "", false
}

// modelDrivenCacheRebuildCost returns the amortized prompt-cache rebuild
// charge for a reset whose post-apply prefix is projectedTokens long: the
// write−read delta of cache-writing that prefix once, spread over the
// minModelDrivenApplyIntervalBatches requests that follow before another
// reset may apply.
func modelDrivenCacheRebuildCost(projectedTokens int) int {
	if projectedTokens <= 0 {
		return 0
	}
	return projectedTokens * modelDrivenCacheRebuildDeltaNumer / modelDrivenCacheRebuildDeltaDenom / minModelDrivenApplyIntervalBatches
}

const (
	// modelDrivenSkipReasonLowGain / modelDrivenSkipReasonInterval are the
	// bound skip reasons recorded on the cooldown state.
	modelDrivenSkipReasonLowGain  = "low_gain"
	modelDrivenSkipReasonInterval = "interval"
)

// modelDrivenPolicySkipRecordBatch returns the batch a pre-preflight policy
// skip records on the cooldown state. A cooldown short-circuit records nothing
// (batch 0, which settleModelDrivenSkip ignores): the cooldown window is
// anchored on the ORIGINAL low-gain skip, and re-stamping the current batch on
// every cooled-down retry would turn a fixed window into a sliding one — a
// model that retries on every batch keeps current-last at 1, the window never
// expires, and the low-gain preflight would never run again. An interval skip
// has no such feedback loop (it is re-decided from the apply anchor) and
// records normally.
func modelDrivenPolicySkipRecordBatch(skipReason string, current uint64) uint64 {
	if skipReason == modelDrivenSkipReasonLowGain {
		return 0
	}
	return current
}

// modelDrivenSkipDraft builds a policy skip draft carrying the verdict batch
// and reason so the event-loop settlement records exactly the numbers the
// worker decided on. Preflight stays nil for skips that never ran it (interval
// and cooldown short-circuits).
func modelDrivenSkipDraft(planID uint64, target compactionTarget, reason, skipReason string, batch uint64, preflight *modelDrivenPreflightStats) *compactionDraft {
	return &compactionDraft{
		Skip:                  true,
		InfoMessage:           "Context checkpoint skipped: " + reason,
		PlanID:                planID,
		Target:                target,
		ModelDrivenPreflight:  preflight,
		ModelDrivenSkipReason: skipReason,
		ModelDrivenSkipBatch:  batch,
	}
}

// modelDrivenIntervalCooldownVerdict decides the two pre-preflight policy
// gates from the barrier snapshot:
//   - the apply interval: fewer than
//     minModelDrivenApplyIntervalBatches since the last successful model-driven
//     apply skips without preflight. The current >= last guard prevents the
//     uint64 underflow of a restored session whose batch counter restarted
//     below the persisted anchor; such a stale anchor counts as satisfied
//     instead of throttling forever.
//   - the same-reason low-gain cooldown: within
//     minModelDrivenSkipCooldownBatches of a previous low-gain skip,
//     short-circuit without entering preflight (which would re-run
//     prepareMessagesForLLM for an outcome that cannot change).
//
// The interval gate runs first: it is deterministic and already skips without
// preflight, so an interval-rejected retry is re-gated by the interval itself,
// never cooled down (a parameter-corrected retry after an interval rejection
// must not be cooldown-blocked). The cooldown therefore binds only to the
// low-gain reason — the one whose repeated evaluation is expensive.
//
// It returns the skip reason, the bound skip reason, and whether to skip.
func (a *MainAgent) modelDrivenIntervalCooldownVerdict(bundle modelDrivenBarrierSnapshot) (string, string, bool) {
	current := bundle.currentRequestBatch
	// current < last means the recorded anchor comes from a counter the
	// current session no longer continues (a restored session whose batch
	// numbering restarted below the persisted anchor): the anchor is stale
	// and the interval counts as satisfied rather than blocking every
	// model-driven request until the counter catches up. current == last is
	// a genuine zero-batch gap and stays throttled.
	if bundle.lastModelDrivenApplyBatch > 0 && current >= bundle.lastModelDrivenApplyBatch &&
		current-bundle.lastModelDrivenApplyBatch < minModelDrivenApplyIntervalBatches {
		return fmt.Sprintf("the minimum %d-request-batch interval since the last applied context checkpoint has not elapsed", minModelDrivenApplyIntervalBatches), modelDrivenSkipReasonInterval, true
	}
	if bundle.lastModelDrivenSkipReason == modelDrivenSkipReasonLowGain &&
		current > bundle.lastModelDrivenSkipBatch &&
		current-bundle.lastModelDrivenSkipBatch < minModelDrivenSkipCooldownBatches {
		return "cooling down after a previous low_gain skip; wait a couple of model requests before retrying", modelDrivenSkipReasonLowGain, true
	}
	return "", "", false
}

// --------------------------------------------------------------- checkpoint ---

// modelDrivenCheckpointBuilder holds the parts of a model-driven checkpoint
// that are identical on both renders of the same draft: the deterministic
// summary body, the archival evidence selection, the retained recent messages
// and the archived-history map of the already-committed chain. The draft
// renders the checkpoint twice — once for the pre-export low-gain preflight
// and once after the archive was written — and only the history map differs
// between them (by exactly the one line of the just-exported archive). Sharing
// the builder keeps the expensive parts (anchor resolution over the whole
// snapshot, one directory listing plus one meta read per archive) to a single
// pass instead of paying them twice per checkpoint.
type modelDrivenCheckpointBuilder struct {
	bundle        modelDrivenBarrierSnapshot
	req           *modelDrivenCheckpointRequest
	summaryText   string
	evidenceItems []evidenceItem
	// retainedRecent is the rendered `## Retained Recent Messages` section.
	retainedRecent string
	// historyLines are the rendered map lines of the committed archive chain
	// (this draft's own archive is added at render time by the post-export
	// call, which is the only caller that knows its path).
	historyLines []string
}

// newModelDrivenCheckpointBuilder performs the one-time work for a draft's
// checkpoint renders. All inputs come from the immutable barrier snapshot.
func (a *MainAgent) newModelDrivenCheckpointBuilder(bundle modelDrivenBarrierSnapshot, snapshot []message.Message, headSplit int, req *modelDrivenCheckpointRequest) *modelDrivenCheckpointBuilder {
	historyChain, historyMetas, err := listCheckpointHistoryReferences(bundle.sessionDir, "")
	if err != nil {
		log.Warnf("model-driven checkpoint: list history references error=%v", err)
	}
	headSnapshot := snapshot[:headSplit]
	return &modelDrivenCheckpointBuilder{
		bundle:        bundle,
		req:           req,
		summaryText:   a.buildModelDrivenCheckpointSummary(bundle, snapshot, headSplit, req),
		evidenceItems: filterCompactionEvidenceForArchival(bundle.evidenceItems),
		// The newest real user messages of the archived head (and any dangling
		// interrupted reply) stay verbatim inside the checkpoint within the
		// retention budget, deterministic like the rest of this path — no model
		// call is involved, and the preflight counts the section because it is
		// part of checkpointContent.
		retainedRecent: renderCheckpointRetainedRecentMessages(headSnapshot, compactRetainRecentUserMessages, bundle.retainRecentTokens, func(text string) int {
			return ctxmgr.EstimateMessagesTokensWithRatio([]message.Message{{Role: message.RoleUser, Content: text}}, bundle.calibratedRatio)
		}),
		historyLines: formatHistoryMapLines(historyChain, historyMetas),
	}
}

// render renders the deterministic checkpoint the way it will appear in the
// transcript: the summary sections wrapped in the canonical checkpoint
// envelope (header, archived history map, evidence artifact, display hint)
// plus the model-driven wrapper copy. exportedArchive, when non-empty, is this
// draft's freshly written archive: it is appended to the map with its own
// topics (it is the newest index, so it sorts last). The returned stats
// describe the checkpoint for the low-gain preflight and lifecycle telemetry.
func (b *modelDrivenCheckpointBuilder) render(exportedArchive string) (string, modelDrivenPreflightStats) {
	historyRefs := b.historyLines
	if exportedArchive != "" {
		base := filepath.Base(exportedArchive)
		exportedLine := formatHistoryMapLines([]string{exportedArchive}, map[string]*compactionHistoryMeta{
			base: {Topics: evidenceItemTopics(b.evidenceItems)},
		})
		historyRefs = append(append(make([]string, 0, len(b.historyLines)+1), b.historyLines...), exportedLine...)
	}
	checkpointContent := buildCompactionCheckpointMessage(b.summaryText, historyRefs, compactionSummaryModeModelDriven, b.evidenceItems, b.retainedRecent)

	var historyMapBytes int
	for _, ref := range historyRefs {
		historyMapBytes += len(ref)
	}
	// AnchorBytes measures the latest-request anchor section (the checkpoint's
	// continuation core), not the raw summary head/tail whitespace.
	anchorBytes := 0
	if _, section, ok := strings.Cut(b.summaryText, "## Current User Request"); ok {
		if before, _, found := strings.Cut(section, "\n## "); found {
			section = before
		}
		anchorBytes = len("## Current User Request") + len(section)
	}
	req := b.req
	return checkpointContent, modelDrivenPreflightStats{
		CheckpointBytes:    len(checkpointContent),
		AnchorBytes:        anchorBytes,
		HistoryMapBytes:    historyMapBytes,
		ContinuationTokens: b.bundle.estimateTokens([]message.Message{{Role: message.RoleUser, Content: req.Args.ActiveObjective + "\n" + req.Args.NextStep + "\n" + strings.Join(req.Args.Completed, "\n") + "\n" + strings.Join(req.Args.Decisions, "\n") + "\n" + strings.Join(req.Args.OpenIssues, "\n") + "\n" + strings.Join(req.Args.StateFiles, "\n")}}),
	}
}

// buildModelDrivenCheckpointSummary renders the deterministic checkpoint
// sections. Current User Request comes from the authoritative runtime
// resolver (real user messages / Done-rejected reasons in message order, with
// inheritance from the previous checkpoint when the tail has nothing new),
// never from the model-authored args. Every model-authored field is rendered
// verbatim except that column-zero ATX heading markers are stripped, so the
// model cannot open a new section from inside one; state_files are labeled as
// model-declared references.
//
// snapshot is the whole barrier snapshot and headSplit its archive boundary:
// the anchor resolver reads the full history, the anchors/carry only the
// archived head. Both are slices of the same immutable snapshot — copying the
// history into a fresh slice just to concatenate head and tail would duplicate
// the entire conversation for no gain.
func (a *MainAgent) buildModelDrivenCheckpointSummary(bundle modelDrivenBarrierSnapshot, snapshot []message.Message, headSplit int, req *modelDrivenCheckpointRequest) string {
	// The prior checkpoint's machine-carryable state (decisions, open issues,
	// evidence references, stage) is merged into this submission and rendered
	// below; the whole prior body is never carried as natural-language
	// Markdown. stateCarryOmitted reports carried decisions dropped by the
	// merge's list bound so the renderer discloses the omission.
	var stateCarryOmitted int
	req, stateCarryOmitted = mergePriorTypedCheckpointState(req, latestPriorCheckpointBody(snapshot[:headSplit]))
	markTypedClaimsInvalidated(req, bundle.evidenceItems)
	headSnapshot := snapshot[:headSplit]
	anchor := resolveLatestUserRequestAnchor(snapshot)
	constraints := renderEvidenceKindForFallback(&compactionInput{EvidenceItems: bundle.evidenceItems}, evidenceUserCorrection, "- No preserved user constraints.")
	openIssues := renderModelStateList(req.Args.OpenIssues, "(none reported by the model)")
	decisions := renderModelStateList(req.Args.Decisions, "(none reported by the model)")
	completed := renderModelStateList(req.Args.Completed, "(none reported by the model)")
	stateFiles := renderStateFilesSection(req.Args.StateFiles)
	plannedStateFiles := renderPlannedStateFilesSection(req.Args.PlannedStateFiles)
	evidenceRefs := renderEvidenceRefsSection(req.Args.EvidenceRefs)
	claimEvidence := renderClaimEvidenceSection(req.Args.ClaimEvidence)
	claimKinds := renderClaimKindsSection(req.Args.ClaimKinds)
	stage := renderModelDrivenStageSection(req.Args.StageID, req.Args.StageStatus, req.Args.CheckpointKind)
	typedState := renderTypedCheckpointState(req)
	if stateCarryOmitted > 0 {
		// The dropped entries exist only in the archived history files. The
		// note must say so: a bounded carry that silently looked complete
		// would read as the full decision record.
		decisions += "\n" + typedStateOmittedNote
	}

	sections := []fallbackSummarySection{
		{"## Current User Request", modelDrivenCurrentUserRequestSection(anchor)},
		{"## Active Objective", renderModelState(req.Args.ActiveObjective)},
		{"## Background Goals", "- Earlier goals are background; follow only the Current User Request and Active Objective above."},
		{"## User Constraints", constraints},
		{"## Progress", completed},
		{"## Key Decisions", decisions},
		{"## Files and Evidence", "- Precise archived history is listed in the checkpoint wrapper's archived history map."},
		{"## Externalized State", stateFiles},
		{"## Planned Externalized State", plannedStateFiles},
		{"## Evidence References", evidenceRefs},
		{"## Claim Evidence", claimEvidence},
		{"## Claim Classification", claimKinds},
		{"## Checkpoint Stage", stage},
		{"## Typed Checkpoint State", typedState},
		{"## Todo State", formatTodosAsRelevanceBullets(bundle.todos, anchor)},
		{"## SubAgent State", formatSubAgentsAsBullets(bundle.subAgents)},
		{"## Open Problems", openIssues},
		{"## Next Step", renderModelState(req.Args.NextStep)},
	}
	summary := renderFallbackSummarySections(sections, bundle.backgroundObjects)
	// The model-driven checkpoint has no model classification to fill the
	// relevance skeleton above, so the runtime-owned snapshot carries the
	// complete todo state — the same guarantee the summarization runner
	// applies, and the line-escaping that keeps todo content from escaping
	// its section. The stale bucket must stay empty: restore only drops
	// runtime todos when it sees entries there.
	summary = ensureCompactionTodoSnapshot(summary, bundle.todos)
	// Skills loaded in the archived head are recorded by name, exactly as the
	// summarization runner does: the instructions live only in the transcript,
	// so this reset would otherwise leave the model following a workflow it can
	// no longer see — and unable to tell that it should re-load it.
	skillNames, skillsOmitted := collectCheckpointSkillNames(headSnapshot)
	summary = ensureCheckpointSkillsSection(summary, skillNames, skillsOmitted)
	// The previous checkpoint's machine-carryable state was merged into the
	// typed state block above; its natural-language body is deliberately NOT
	// carried forward. The model re-states its current objective, progress and
	// claims on every submission, and carried decisions/open issues/evidence
	// references/stage travel structurally through the typed block — so two
	// consecutive model-driven resets cannot erase prior verified decisions,
	// and everything else the model did not restate exists in the archived
	// history files rather than as a growing verbatim appendix.
	anchors := buildCompactionAnchors(latestCompactionAnchors(headSnapshot), bundle.originalRequest, bundle.evidenceItems)
	return withCompactionAnchors(summary, anchors)
}

func markTypedClaimsInvalidated(req *modelDrivenCheckpointRequest, evidenceItems []evidenceItem) {
	if req == nil || len(req.Args.ClaimEvidence) == 0 || len(req.Args.ClaimKinds) == 0 {
		return
	}
	validity := make(map[string]evidenceValidity, len(evidenceItems))
	for _, item := range evidenceItems {
		validity[evidenceItemID(item)] = item.Validity
	}
	for claim, refs := range req.Args.ClaimEvidence {
		for _, ref := range refs {
			if validity[ref] == evidenceValidityInvalidated || validity[ref] == evidenceValidityUnavailable {
				if req.ClaimStatuses == nil {
					req.ClaimStatuses = map[string]string{}
				}
				req.ClaimStatuses[claim] = "invalidated"
				break
			}
		}
	}
}

// mergePriorTypedCheckpointState merges the typed state carried by the most
// recent prior checkpoint (inside the archived head) into the fresh model
// submission, then writes the merged state back into the request arguments so
// the rendered sections and the typed state block both reflect the carry. The
// fresh submission wins (its items come first and its stage metadata
// overrides); carried items fill the remaining list capacity. omittedDecisions
// reports how many carried decisions were dropped to bound the list, so the
// renderer can disclose it. A nil request, an empty prior body, or a prior
// checkpoint without a typed state block (a usage-driven summary, an old
// checkpoint) leaves the submission untouched.
func mergePriorTypedCheckpointState(req *modelDrivenCheckpointRequest, prior string) (mergedReq *modelDrivenCheckpointRequest, omittedDecisions int) {
	if req == nil {
		return req, 0
	}
	priorState, ok := parseCheckpointTypedState(prior)
	if !ok {
		return req, 0
	}
	merged, omitted := mergeCheckpointTypedStates(priorState, typedStateFromArgs(req.Args))
	copyReq := *req
	copyReq.Args.Decisions = merged.Decisions
	copyReq.Args.OpenIssues = merged.OpenIssues
	copyReq.Args.EvidenceRefs = merged.EvidenceRefs
	copyReq.Args.StageID = merged.StageID
	copyReq.Args.StageStatus = merged.StageStatus
	copyReq.Args.CheckpointKind = merged.Kind
	return &copyReq, omitted
}

func renderTypedCheckpointState(req *modelDrivenCheckpointRequest) string {
	if req == nil {
		return "- (none)"
	}
	state := typedStateFromArgs(req.Args)
	for claim, status := range req.ClaimStatuses {
		item := state.Claims[claim]
		item.Status = status
		state.Claims[claim] = item
	}
	return renderTypedStateJSON(state)
}

// inheritedCheckpointLabel is the label prefixed to a `## Current User Request`
// section inherited from the previous checkpoint. It lives in one place so the
// accumulation guard in modelDrivenCurrentUserRequestSection strips exactly
// the same text the resolver stamps on inherited anchors.
const inheritedCheckpointLabel = "Inherited from the previous context checkpoint"

// modelDrivenCurrentUserRequestSection renders the `## Current User Request`
// section of a deterministic checkpoint from the latest-request anchor, capping
// the anchor text like the structured-fallback summary does.
func modelDrivenCurrentUserRequestSection(anchor fallbackAnchor) string {
	if anchor.Kind != "" {
		if anchor.Kind == "inherited_checkpoint" {
			// The inherited body is the previous checkpoint's own bullet
			// ("- Latest user request: ..."); strip the bullet marker so the
			// inherited label reads naturally. The body may itself carry the
			// inherited label from an earlier checkpoint in a chain of resets
			// (a checkpoint built with no new user request inherits the prior
			// one), so strip it too: the label must never accumulate across
			// successive checkpoints.
			body := strings.TrimSpace(strings.TrimPrefix(anchor.Text, "- "))
			body = strings.TrimSpace(strings.TrimPrefix(body, inheritedCheckpointLabel+": "))
			return "- " + inheritedCheckpointLabel + ": " + body
		}
		// Cap the anchor text like the structured-fallback summary does
		// (260 chars with an explicit cut marker): an overlong user message
		// or Done-rejected reason must not crowd out the rest of the
		// deterministic checkpoint.
		return "- " + anchor.Label + ": " + compactTextSnippet(strings.ReplaceAll(anchor.Text, "\n", " "), modelDrivenAnchorMaxRunes)
	}
	return "- Unknown: no reliable latest user request was preserved; do not infer the active task from stale context."
}

// renderModelState renders a model-authored field into a checkpoint section.
// The text is trusted verbatim except for column-zero ATX heading markers,
// which are stripped: the checkpoint readers locate sections by "^## " lines,
// and a heading is the only line-level structure that would let the text
// escape its section. Quoted lines ("> ...") and every other markdown
// construct are preserved, so a blockquote the model wrote renders as one.
func renderModelState(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "- (none)"
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, stripLeadingHeadingMarkers(line))
	}
	return strings.Join(out, "\n")
}

// renderModelStateList renders model-authored list items as bullets
// ("- item" per line), stripping only column-zero heading markers the same
// way renderModelState does. The fallback text is used when the list is empty.
func renderModelStateList(items []string, empty string) string {
	if len(items) == 0 {
		return "- " + empty
	}
	var sb strings.Builder
	for i, item := range items {
		if i > 0 {
			sb.WriteByte('\n')
		}
		for line := range strings.SplitSeq(item, "\n") {
			sb.WriteString("- ")
			sb.WriteString(stripLeadingHeadingMarkers(line))
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// stripLeadingHeadingMarkers removes an ATX heading marker ("# ".."###### ")
// from the start of a line. A run longer than six hashes is not a heading and
// stays untouched; "#tag" / "#1" (no blank after the run) are plain text and
// survive. The strip loops so a line like "# # x" cannot re-render as a
// heading after one pass.
func stripLeadingHeadingMarkers(line string) string {
	for {
		n := 0
		for n < len(line) && line[n] == '#' {
			n++
		}
		if n == 0 || n > 6 {
			return line
		}
		if n < len(line) && line[n] != ' ' && line[n] != '\t' {
			return line
		}
		j := n
		for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
			j++
		}
		line = line[j:]
	}
}

// renderStateFilesSection renders the Externalized State section: every path
// is a model-declared reference rendered as its own bullet.
//
// The model submits these paths as part of its checkpoint; the system treats
// each as an unverified claim. Existence is not checked at checkpoint time:
// the compact_context contract says state_files are pure references, never
// read, injected, or existence-verified, so stamping them would turn a
// checkpoint request into a cross-permission-boundary existence probe. The
// continuation must re-read any path with the read tool before relying on it,
// and a missing file surfaces then.
func renderStateFilesSection(paths []string) string {
	if len(paths) == 0 {
		return "- (none reported by the model)\n- Model-declared references only; existence is not verified."
	}
	var sb strings.Builder
	sb.WriteString("- Model-declared references only; existence is not verified. Use the read tool to load any path before relying on it:\n")
	for _, p := range paths {
		for line := range strings.SplitSeq(p, "\n") {
			sb.WriteString("- ")
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderPlannedStateFilesSection(paths []string) string {
	if len(paths) == 0 {
		return "- (none reported by the model)"
	}
	var sb strings.Builder
	sb.WriteString("- Planned references only; they are not completion evidence and are not verified:\n")
	for _, path := range paths {
		sb.WriteString("- ")
		sb.WriteString(path)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderEvidenceRefsSection(refs []string) string {
	if len(refs) == 0 {
		return "- (none reported by the model)"
	}
	return "- Model-declared evidence references; runtime verified that these IDs exist:\n- " + strings.Join(refs, "\n- ")
}

func renderClaimEvidenceSection(claims map[string][]string) string {
	if len(claims) == 0 {
		return "- (none reported by the model)"
	}
	keys := make([]string, 0, len(claims))
	for key := range claims {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "- %s | evidence: %s\n", key, strings.Join(claims[key], ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderClaimKindsSection(kinds map[string]string) string {
	if len(kinds) == 0 {
		return "- (none reported by the model)"
	}
	keys := make([]string, 0, len(kinds))
	for key := range kinds {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "- %s | kind: %s\n", key, kinds[key])
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderModelDrivenStageSection(id, status, kind string) string {
	if id == "" && status == "" && kind == "" {
		return "- No stage metadata reported by the model."
	}
	return fmt.Sprintf("- Stage ID: %s\n- Stage status: %s\n- Checkpoint kind: %s\n- Stage metadata is model-declared; runtime facts and acceptance evidence remain authoritative.", id, status, kind)
}

// settleModelDrivenOutcome is the single settlement point for model-driven
// checkpoints that did not apply (skip / failure / cancel / discard). It
// records the lifecycle analytics event exactly once, emits the terminal TUI
// status exactly once with the model_driven trigger, and queues the transient
// continuation notice. It deliberately does NOT clear LastTokenUsage,
// autoCompactRequested, or the usage-driven failure state: the safety net
// stays armed and the next gate decides.
func (a *MainAgent) settleModelDrivenOutcome(status string, reason string, preflight *modelDrivenPreflightStats, eventPlanID uint64) {
	// A terminal outcome must never survive into a later recovery snapshot as
	// an accepted request. The lifecycle event has already captured the
	// request identity before this state is cleared.
	a.transitionModelDrivenProposal(status, reason)
	a.modelDrivenSkipNotice = strings.TrimSpace(reason)
	// The model already took its shot at a checkpoint: the threshold grace
	// (if any) ends here so the usage-driven safety net is not deferred again.
	a.exhaustCompactionGraceAfterModelDriven()
	diagnostic := map[string]string{
		"trigger": compactionTriggerModelDriven.analyticsName(),
		"reason":  a.modelDrivenSkipNotice,
	}
	if requestID := strings.TrimSpace(a.modelDrivenProposal.requestID); requestID != "" {
		diagnostic["request_id"] = requestID
	}
	if preflight != nil {
		diagnostic["current_tokens"] = strconv.Itoa(preflight.CurrentTokens)
		diagnostic["projected_tokens"] = strconv.Itoa(preflight.ProjectedTokens)
		diagnostic["saved_tokens"] = strconv.Itoa(preflight.SavedTokens)
		diagnostic["saved_ratio_pct"] = strconv.Itoa(preflight.SavedRatioPct)
		if preflight.CurrentSource != "" {
			diagnostic["current_source"] = preflight.CurrentSource
		}
		diagnostic["current_bytes"] = strconv.Itoa(preflight.CurrentBytes)
		diagnostic["projected_bytes"] = strconv.Itoa(preflight.ProjectedBytes)
		diagnostic["checkpoint_bytes"] = strconv.Itoa(preflight.CheckpointBytes)
		diagnostic["anchor_bytes"] = strconv.Itoa(preflight.AnchorBytes)
		diagnostic["history_map_bytes"] = strconv.Itoa(preflight.HistoryMapBytes)
		diagnostic["continuation_tokens"] = strconv.Itoa(preflight.ContinuationTokens)
		diagnostic["cache_rebuild_cost"] = strconv.Itoa(preflight.CacheRebuildCost)
	}
	a.recordCompactionLifecycleEvent(status, diagnostic)
	// The terminal TUI/control-plane event always carries the model_driven
	// trigger and the settled plan id. The plan id comes from the explicit
	// argument (the worker draft's, or the state's when the draft is generic):
	// a synchronous settle that never owned the compaction slot must
	// not emit an empty id, nor borrow the id of a running automatic
	// compaction it deliberately left untouched.
	if eventPlanID == 0 {
		eventPlanID = a.compactionState.planID
	}
	evt := CompactionStatusEvent{Status: status, Reason: a.modelDrivenSkipNotice, Trigger: compactionTriggerModelDriven.analyticsName()}
	if eventPlanID > 0 {
		evt.PlanID = strconv.FormatUint(eventPlanID, 10)
	}
	a.emitToTUI(evt)
}

// settleModelDrivenSkip records a model-driven policy skip (low-gain, apply
// interval, or same-reason cooldown) with the worker-computed preflight stats.
// The verdict batch and reason from the draft update the skip-cooldown state
// so a retry with the same reason short-circuits without re-running preflight;
// structural skips ("not enough history") carry no verdict and do not touch
// the cooldown state.
//
// draft must be non-nil: the only production caller reaches this through a
// branch that has already dereferenced draft.Skip. Half-guarding it (as this
// function once did) only hides a caller bug that the very next line would
// trip over anyway.
func (a *MainAgent) settleModelDrivenSkip(draft *compactionDraft) {
	if draft.ModelDrivenSkipReason != "" && draft.ModelDrivenSkipBatch > 0 {
		a.lastModelDrivenSkipBatch = draft.ModelDrivenSkipBatch
		a.lastModelDrivenSkipReason = draft.ModelDrivenSkipReason
	}
	reason := strings.TrimSpace(draft.InfoMessage)
	reason = strings.TrimPrefix(reason, "Context checkpoint skipped: ")
	if reason == "" {
		reason = "projected savings were too small"
	}
	a.settleModelDrivenOutcome(CompactionStatusSkipped, reason, draft.ModelDrivenPreflight, draft.PlanID)
}

// settleModelDrivenFailure records a failed model-driven compaction without
// clearing the usage-driven safety net.
func (a *MainAgent) settleModelDrivenFailure(err error) {
	reason := "the checkpoint request failed: " + shortCompactionFailureReason(err)
	a.settleModelDrivenOutcome(CompactionStatusFailed, reason, nil, a.compactionState.planID)
	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Model-driven context checkpoint failed: %v", err), Level: "warn"})
}

// settleModelDrivenCancelled records a user-cancelled or discarded
// model-driven checkpoint.
func (a *MainAgent) settleModelDrivenCancelled(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "the checkpoint request was cancelled"
	}
	a.settleModelDrivenOutcome(CompactionStatusCancelled, reason, nil, a.compactionState.planID)
}

// appendModelDrivenContinuationNotice queues the model-driven continuation
// notice as a one-shot transient turn overlay. It is NOT appended to ctxMgr:
// an internal diagnostic must never become a durable user message that a later
// compaction could misread as the latest user request.
func (a *MainAgent) appendModelDrivenContinuationNotice() {
	reason := strings.TrimSpace(a.modelDrivenSkipNotice)
	a.modelDrivenSkipNotice = ""
	if reason == "" {
		reason = "projected savings were too small"
	}
	a.pendingModelDrivenNotice = "Context checkpoint not applied: " + reason + " The session continues on the previous context."
	a.emitToTUI(ToastEvent{Message: "Context checkpoint not applied: " + reason, Level: "info"})
}
