package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
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
	currentRequestBatch                  uint64
	runtimeStateFingerprint              string
	lastModelDrivenCheckpointFingerprint string
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
	// workDir is the checkout the checkpoint records repository state for,
	// captured on the event loop at the barrier so the worker's build-time git
	// probe never reads live workdir state.
	workDir string
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
		// The turn that armed the request ended before the barrier could hand
		// it to a worker. The proposal already moved to preparing; settle it
		// through the normal terminal path (cancelled) so the persisted
		// record never keeps an orphaned pre-apply state — a later crash
		// restore would otherwise read "requested but not applied" intent
		// that can no longer be applied — and the terminal settle clears the
		// args audit copy.
		log.Warn("model-driven checkpoint pending but turn is gone; settling request as cancelled")
		a.settleModelDrivenCancelled("the turn ended before the model-driven checkpoint could start; no reset occurred")
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
	if reason, skipReason, skip := a.modelDrivenCheckpointSkipVerdict(bundle, req); skip {
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
		if readyDraft.TransactionID != "" {
			removeCompactionTransactionManifest(readyDraft.TransactionSessionDir, readyDraft.TransactionID)
		}
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
	// A failure the checkpoint re-attaches as a live record must not also ride
	// the evidence pack: the live record already shows it verbatim.
	retainedFailures := retainedFailureCallIDs(checkpointRetainedFailureRecords(snapshot))
	bundle := modelDrivenBarrierSnapshot{
		snapshot:                             snapshot,
		evidenceItems:                        excludeRetainedFailureEvidence(a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens()), retainedFailures),
		todos:                                a.GetTodos(),
		subAgents:                            a.taskInfosForCompaction(),
		backgroundObjects:                    jobStatesForSnapshot(),
		maxTokens:                            a.ctxMgr.GetMaxTokens(),
		sessionDir:                           a.sessionDir,
		originalRequest:                      a.captureOriginalFirstUserHint(),
		responsesState:                       a.currentTurnResponsesState(),
		prepareReducedRequest:                a.compactionReductionScratch().prepareMessagesForLLM,
		fixedRequestTokens:                   a.estimateFixedRequestTokens(),
		queuedUserMessages:                   a.pendingUserMessagesForPreflight(),
		postResetFixedRequestTokens:          a.estimatePostResetFixedRequestTokens(),
		currentRequestBatch:                  a.currentRequestBatch(snapshot),
		lastModelDrivenApplyBatch:            a.lastModelDrivenApplyBatch,
		lastModelDrivenCheckpointFingerprint: a.lastModelDrivenCheckpointFingerprint,
		lastModelDrivenSkipBatch:             a.lastModelDrivenSkipBatch,
		lastModelDrivenSkipReason:            a.lastModelDrivenSkipReason,
		promptCacheCapable:                   a.currentModelPromptCacheCapable(),
		calibratedRatio:                      a.ctxMgr.CalibratedRatio(),
		retainRecentTokens:                   a.effectiveCompactionRetainRecentTokens(),
		archiveMeta:                          a.captureCompactionArchiveMeta(),
		workDir:                              a.workDir(),
		lastPreparedTurnID:                   lastPreparedTurnID,
		lastPreparedSource:                   lastPreparedSource,
		lastPreparedPrefix:                   lastPreparedPrefix,
	}
	bundle.runtimeStateFingerprint = modelDrivenRuntimeStateFingerprint(bundle)
	return bundle
}

// modelDrivenRuntimeInput is the subset of live event-loop state the
// runtime-state staleness fingerprint hashes: todos, live sub-agents,
// background objects and the evidence candidates. Everything else the barrier
// bundle carries (prepared surfaces, response state, estimates, transcripts)
// is work the fingerprint never reads.
type modelDrivenRuntimeInput struct {
	todos             []tools.TodoItem
	subAgents         []SubAgentInfo
	backgroundObjects []recovery.BackgroundObjectState
	evidenceItems     []evidenceItem
}

// captureModelDrivenRuntimeInput fetches only the four runtime inputs the
// staleness fingerprint hashes. The apply-time re-check used to rebuild the
// whole barrier bundle (cloned prepared surfaces, response state, token
// estimates, a second transcript snapshot) just to re-derive the same
// fingerprint, so the event-loop apply and the barrier capture share this
// lean capture instead.
func (a *MainAgent) captureModelDrivenRuntimeInput() modelDrivenRuntimeInput {
	return modelDrivenRuntimeInput{
		todos:             a.GetTodos(),
		subAgents:         a.taskInfosForCompaction(),
		backgroundObjects: jobStatesForSnapshot(),
		evidenceItems: excludeRetainedFailureEvidence(
			a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens()),
			retainedFailureCallIDs(checkpointRetainedFailureRecords(a.ctxMgr.Snapshot())),
		),
	}
}

func modelDrivenRuntimeStateFingerprint(bundle modelDrivenBarrierSnapshot) string {
	return modelDrivenRuntimeInputFingerprint(modelDrivenRuntimeInput{
		todos:             bundle.todos,
		subAgents:         bundle.subAgents,
		backgroundObjects: bundle.backgroundObjects,
		evidenceItems:     bundle.evidenceItems,
	})
}

func modelDrivenRuntimeInputFingerprint(input modelDrivenRuntimeInput) string {
	// The live SubAgent slice is built from an unsorted map
	// (taskInfosForCompaction iterates a.subs.subAgents), so two captures of
	// unchanged state can order the entries differently. The fingerprint must
	// not read that noise as a state change (an apply would then reject a
	// perfectly current draft as stale), so both the barrier capture and the
	// apply-time re-capture are sorted canonically before hashing — they go
	// through this one function, which makes the ordering identical on both
	// sides.
	subAgents := append([]SubAgentInfo(nil), input.subAgents...)
	slices.SortFunc(subAgents, func(a, b SubAgentInfo) int {
		if c := strings.Compare(a.InstanceID, b.InstanceID); c != 0 {
			return c
		}
		return strings.Compare(a.TaskID, b.TaskID)
	})
	// queuedUserMessages is deliberately excluded: user input that arrives
	// while the checkpoint draft is pending is merged into the continuation
	// request after the apply (never into the checkpoint itself), so it is not
	// state the apply depends on — counting it would void every draft that a
	// user interrupts by typing. The draft's own boundary (generation) still
	// guards against genuinely changed runtime state.
	payload, _ := json.Marshal(struct {
		Todos      []tools.TodoItem
		SubAgents  []SubAgentInfo
		Background []recovery.BackgroundObjectState
		Evidence   []evidenceItem
	}{input.todos, subAgents, input.backgroundObjects, input.evidenceItems})
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
		if IsIdleOnlySlashCommand(c) {
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
