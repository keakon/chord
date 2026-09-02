package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
	ToolCallID   string
	Args         tools.CompactContextArgs
	AssistantMsg string // declaring assistant message content (for batch hooks / model context)
}

// requestAcceptedToolResult is the canonical compact_context success text. It
// deliberately says "accepted", not "applied": a crash between acceptance and
// apply must not let the restored transcript read as a successful reset.
const requestAcceptedToolResult = "Context checkpoint request accepted. No reset has occurred yet; only a later model-driven context checkpoint confirms successful application."

const (
	// compactionSummaryModeModelDriven is the stable summary-mode marker for
	// model-driven checkpoints (mirrors model_summary / structured_fallback /
	// truncate_only).
	compactionSummaryModeModelDriven = "model_driven_checkpoint"
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
	// modelDrivenCacheWriteMultiplier / modelDrivenCacheReadMultiplier
	// approximate the Anthropic prompt-cache pricing used to charge the
	// projected side of the low-gain gate the cost of rewriting the archived
	// prefix (cache write ≈ 1.25x, cache read ≈ 0.1x). Only the delta applies:
	// the old prefix would have been cache-read, the new one must be
	// cache-written. modelDrivenCacheRebuildDeltaNumer/Denom carry the same
	// delta (1.15) as integer math.
	modelDrivenCacheWriteMultiplier   = 1.25
	modelDrivenCacheReadMultiplier    = 0.10
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
	// scratchAgent carries the request-reduction policy of the live agent
	// (config, immutable tool registry, project root, recall-protection
	// snapshots) captured at the barrier; preflight runs reduction through it
	// without touching live state.
	scratchAgent *MainAgent
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
	currentRequestBatch uint64
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
	// CacheRebuildCost is the prompt-cache rewrite cost charged to the
	// projected side of the low-gain gate for prompt-cache-capable sessions
	// (rewritten-prefix tokens × (write − read) multiplier). Zero for
	// non-cacheable sessions.
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
	if a.IsCompactionRunning() {
		return "", tools.CompactContextArgs{}, fmt.Errorf("a context compaction is already running or waiting; retry compact_context after it settles")
	}
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
// the usage-calibrated token estimator (the tool's own Execute falls back to
// the plain bytes/3 heuristic because it cannot reach the ctxmgr calibration).
func (a *MainAgent) parseCompactContextArgs(raw json.RawMessage) (tools.CompactContextArgs, error) {
	validator := tools.CompactContextValidator{
		ContinuationStateMaxTokens: compactEvidenceMaxTokens,
		EstimateTokens:             a.EstimateTokensForText,
	}
	return validator.ParseCompactContextArgs(raw)
}

// compactContextSoleToolCall reports whether the declaring assistant message
// carries exactly this one tool call and no sibling calls.
func compactContextSoleToolCall(messages []message.Message, callID string) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
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
	a.pendingModelDriven = &modelDrivenCheckpointRequest{
		ToolCallID:   callID,
		Args:         args,
		AssistantMsg: assistantContentForToolCall(a.ctxMgr.Snapshot(), callID),
	}
	return result, nil
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
	if a.turn == nil {
		log.Warn("model-driven checkpoint pending but turn is gone; dropping request")
		return false
	}
	snapshot := a.ctxMgr.Snapshot()
	bundle := a.captureModelDrivenBarrierSnapshot(snapshot)
	planID, target := a.nextCompactionPlan()
	continuation := continuationPlan{
		kind:      compactionResumeModelDriven,
		turnID:    a.turn.ID,
		turnEpoch: a.turn.Epoch,
	}
	a.startModelDrivenCompactionAsync(bundle, planID, target, continuation, req)
	// The worker is now in charge: the next main LLM request must wait for
	// the checkpoint barrier instead of running on the old context.
	a.emitCompactionSlotActivity()
	return true
}

// captureModelDrivenBarrierSnapshot snapshots every piece of event-loop-owned
// state the model-driven worker may need, on the event loop. The returned
// bundle is immutable; the worker only ever reads it.
func (a *MainAgent) captureModelDrivenBarrierSnapshot(snapshot []message.Message) modelDrivenBarrierSnapshot {
	return modelDrivenBarrierSnapshot{
		snapshot:                    snapshot,
		evidenceItems:               a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens()),
		todos:                       a.GetTodos(),
		subAgents:                   a.taskInfosForCompaction(),
		backgroundObjects:           spawnStatesForSnapshot(),
		maxTokens:                   a.ctxMgr.GetMaxTokens(),
		sessionDir:                  a.sessionDir,
		originalRequest:             a.captureOriginalFirstUserHint(),
		responsesState:              a.currentTurnResponsesState(),
		scratchAgent:                a.compactionReductionScratch(),
		fixedRequestTokens:          a.estimateFixedRequestTokens(),
		queuedUserMessages:          a.pendingUserMessagesForPreflight(),
		postResetFixedRequestTokens: a.estimatePostResetFixedRequestTokens(),
		currentRequestBatch:         a.currentRequestBatch(snapshot),
		lastModelDrivenApplyBatch:   a.lastModelDrivenApplyBatch,
		lastModelDrivenSkipBatch:    a.lastModelDrivenSkipBatch,
		lastModelDrivenSkipReason:   a.lastModelDrivenSkipReason,
		promptCacheCapable:          a.currentModelPromptCacheCapable(),
		calibratedRatio:             a.ctxMgr.CalibratedRatio(),
	}
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

	a.emitCompactionSlotActivity()
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
		return modelDrivenSkipDraft(planID, target, reason, skipReason, bundle.currentRequestBatch, nil), nil
	}

	// Low-gain preflight BEFORE history export. A skip here must not produce
	// orphan history-*.md / metadata / backup files. The preflight stats ride
	// on the returned draft so the event-loop settlement records them once.
	skipReason, skip, preflight := a.modelDrivenLowGainPreflight(bundle, headSplit, head, snapshot, req)
	if skip {
		return modelDrivenSkipDraft(planID, target, skipReason, "low_gain", bundle.currentRequestBatch, &preflight), nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	index, err := nextCompactionIndex(bundle.sessionDir)
	if err != nil {
		return nil, fmt.Errorf("determine compaction index: %w", err)
	}
	absHistoryPath, sourceRefs, sourceFingerprint, err := a.exportCompactionHistory(head, index, evidenceItemTopics(evidenceItems))
	if err != nil {
		return nil, fmt.Errorf("export compacted history: %w", err)
	}
	absHistoryMetaPath := compactionHistoryMetaPath(absHistoryPath)
	// Any failure after the archive is written must remove it: a cancelled or
	// errored worker otherwise leaves orphan history-*.md / .status.json files
	// that no draft path carries (getAbsHistoryPathFromDraft sees a nil draft
	// and the generic cancellation cleanup cannot find them).
	historyCommitted := false
	defer func() {
		if !historyCommitted {
			cleanupOrphanCompactionFiles(absHistoryPath)
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
	checkpointContent, contentStats := a.buildModelDrivenCheckpointContent(bundle, snapshot, headSplit, req)
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
	if reason, skip := modelDrivenLowGainCheck(preflight.CurrentTokens, projectedTokens, saved, preflight.CacheRebuildCost); skip {
		return modelDrivenSkipDraft(planID, target, reason, "low_gain", bundle.currentRequestBatch, &preflight), nil
	}
	contextSummaryMsg := message.Message{
		Role:                "user",
		Content:             checkpointContent,
		IsCompactionSummary: true,
	}

	newMessages := []message.Message{contextSummaryMsg}
	historyCommitted = true
	return &compactionDraft{
		PlanID:               planID,
		Target:               target,
		NewMessages:          newMessages,
		HeadSplit:            headSplit,
		Index:                index,
		AbsHistoryPath:       absHistoryPath,
		AbsHistoryMetaPath:   absHistoryMetaPath,
		SourceRefs:           sourceRefs,
		SourceFingerprint:    sourceFingerprint,
		SummaryMode:          compactionSummaryModeModelDriven,
		Backend:              config.CompactionPresetGeneric,
		Profile:              string(compactionProfileArchival),
		ModelRef:             "model_declared",
		Manual:               false,
		ArchivedCount:        len(head),
		EvidenceCount:        len(evidenceItems),
		EvidenceArtifacts:    len(evidenceMsgs),
		ModelDrivenPreflight: &preflight,
	}, nil
}

// ------------------------------------------------------------- preflight ---

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
func (a *MainAgent) modelDrivenLowGainPreflight(bundle modelDrivenBarrierSnapshot, headSplit int, head []message.Message, snapshot []message.Message, req *modelDrivenCheckpointRequest) (string, bool, modelDrivenPreflightStats) {
	currentSurface := bundle.scratch().prepareMessagesForLLM(snapshot)
	// Queued user messages are merged after reduction on the real request
	// path, so they append to the prepared surface on both sides.
	currentSurface = append(currentSurface, bundle.queuedUserMessages...)
	checkpointContent, preflight := a.buildModelDrivenCheckpointContent(bundle, snapshot, headSplit, req)
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
	// Prompt-cache rewrite cost: replacing the archived prefix invalidates the
	// cached head, so the projected side must pay the delta between writing
	// the new prefix and reading the old one (write ≈ 1.25×, read ≈ 0.1× for
	// Anthropic). Only the head region [0, headSplit) is rewritten; the live
	// tail keeps its cache position. The cost is charged to the savings before
	// the low-gain gates, so a cacheable session cannot approve a reset whose
	// net gain after cache rebuild falls below the gate.
	var cacheRebuildCost int
	if bundle.promptCacheCapable && headSplit > 0 && headSplit <= len(currentSurface) {
		headTokens := bundle.estimateTokens(currentSurface[:headSplit])
		cacheRebuildCost = headTokens * modelDrivenCacheRebuildDeltaNumer / modelDrivenCacheRebuildDeltaDenom
	}
	preflight.CacheRebuildCost = cacheRebuildCost
	if reason, skip := modelDrivenLowGainCheck(currentTokens, projectedTokens, saved, cacheRebuildCost); skip {
		return reason, true, preflight
	}
	return "", false, preflight
}

// modelDrivenLowGainCheck applies the fixed low-gain gates to a savings
// estimate and returns the skip reason when either gate fails. Both the
// pre-export preflight and the post-export re-check share it so the refined
// checkpoint cannot be approved on a stale estimate. cacheRebuildCost is
// subtracted from the raw savings before the gates: prompt-cache-capable
// sessions pay the cache rewrite of the archived prefix on the projected
// side, so a reset is only approved when the net gain survives that cost.
func modelDrivenLowGainCheck(currentTokens, projectedTokens, saved, cacheRebuildCost int) (string, bool) {
	net := saved - cacheRebuildCost
	if net < modelDrivenLowGainMinTokens || net < int(float64(currentTokens)*modelDrivenLowGainMinRatio) {
		reason := fmt.Sprintf("projected savings %d tokens is below the low-gain gate (%d tokens and %d%% of the prepared surface)", saved, modelDrivenLowGainMinTokens, int(modelDrivenLowGainMinRatio*100))
		if cacheRebuildCost > 0 {
			reason += fmt.Sprintf("; prompt-cache rebuild cost %d tokens was subtracted", cacheRebuildCost)
		}
		return reason, true
	}
	return "", false
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
//     apply skips without preflight. The current > last guard prevents the
//     uint64 underflow that would otherwise treat a restored session (batch
//     counter restarts at 0) as "interval satisfied".
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
	if bundle.lastModelDrivenApplyBatch > 0 &&
		(current <= bundle.lastModelDrivenApplyBatch || current-bundle.lastModelDrivenApplyBatch < minModelDrivenApplyIntervalBatches) {
		return fmt.Sprintf("the minimum %d-request-batch interval since the last applied context checkpoint has not elapsed", minModelDrivenApplyIntervalBatches), "interval", true
	}
	if bundle.lastModelDrivenSkipReason == "low_gain" &&
		current > bundle.lastModelDrivenSkipBatch &&
		current-bundle.lastModelDrivenSkipBatch < minModelDrivenSkipCooldownBatches {
		return "cooling down after a previous low_gain skip; wait a couple of model requests before retrying", "low_gain", true
	}
	return "", "", false
}

// --------------------------------------------------------------- checkpoint ---

// buildModelDrivenCheckpointContent renders the deterministic checkpoint the
// way it will appear in the transcript: the summary sections wrapped in the
// canonical checkpoint envelope (header, archived history map, evidence
// artifact, display hint) plus the model-driven wrapper copy. historyRefs are
// listed from the session dir at call time, so the post-export draft naturally
// includes the just-written history file. The returned stats describe the
// checkpoint for the low-gain preflight and lifecycle telemetry.
func (a *MainAgent) buildModelDrivenCheckpointContent(bundle modelDrivenBarrierSnapshot, snapshot []message.Message, headSplit int, req *modelDrivenCheckpointRequest) (string, modelDrivenPreflightStats) {
	headSnapshot := snapshot[:headSplit]
	recentTail := snapshot[headSplit:]
	summaryText := a.buildModelDrivenCheckpointSummary(bundle, headSnapshot, recentTail, req)
	historyRefs, err := listHistoryReferences(bundle.sessionDir)
	if err != nil {
		log.Warnf("model-driven checkpoint: list history references error=%v", err)
	}
	historyRefs = formatHistoryMapLines(historyRefs, readCompactionHistoryMetas(historyRefs))
	evidenceItems := filterCompactionEvidenceForArchival(bundle.evidenceItems)
	checkpointContent := buildCompactionCheckpointMessage(summaryText, historyRefs, compactionSummaryModeModelDriven, evidenceItems)

	var historyMapBytes int
	for _, ref := range historyRefs {
		historyMapBytes += len(ref)
	}
	// AnchorBytes measures the latest-request anchor section (the checkpoint's
	// continuation core), not the raw summary head/tail whitespace.
	anchorBytes := 0
	if _, section, ok := strings.Cut(summaryText, "## Current User Request"); ok {
		if before, _, found := strings.Cut(section, "\n## "); found {
			section = before
		}
		anchorBytes = len("## Current User Request") + len(section)
	}
	return checkpointContent, modelDrivenPreflightStats{
		CheckpointBytes:    len(checkpointContent),
		AnchorBytes:        anchorBytes,
		HistoryMapBytes:    historyMapBytes,
		ContinuationTokens: bundle.estimateTokens([]message.Message{{Role: message.RoleUser, Content: req.Args.ActiveObjective + "\n" + req.Args.NextStep + "\n" + strings.Join(req.Args.Completed, "\n") + "\n" + strings.Join(req.Args.Decisions, "\n") + "\n" + strings.Join(req.Args.OpenIssues, "\n") + "\n" + strings.Join(req.Args.StateFiles, "\n")}}),
	}
}

// buildModelDrivenCheckpointSummary renders the deterministic checkpoint
// sections. Current User Request comes from the authoritative runtime
// resolver (real user messages / Done-rejected reasons in message order, with
// inheritance from the previous checkpoint when the tail has nothing new),
// never from the model-authored args. Every model-authored field is quoted
// line by line so headings, markers, or wrapper shapes cannot escape their
// section; state_files are labeled as model-declared references.
func (a *MainAgent) buildModelDrivenCheckpointSummary(bundle modelDrivenBarrierSnapshot, headSnapshot []message.Message, recentTail []message.Message, req *modelDrivenCheckpointRequest) string {
	snapshot := append(append([]message.Message(nil), headSnapshot...), recentTail...)
	anchor := resolveLatestUserRequestAnchor(snapshot)
	constraints := renderEvidenceKindForFallback(&compactionInput{EvidenceItems: bundle.evidenceItems}, evidenceUserCorrection, "- No preserved user constraints.")
	openIssues := quoteModelStateList(req.Args.OpenIssues, "(none reported by the model)")
	decisions := quoteModelStateList(req.Args.Decisions, "(none reported by the model)")
	completed := quoteModelStateList(req.Args.Completed, "(none reported by the model)")
	stateFiles := quoteStateFilesSection(req.Args.StateFiles)

	sections := []fallbackSummarySection{
		{"## Current User Request", modelDrivenCurrentUserRequestSection(anchor)},
		{"## Active Objective", quoteModelState(req.Args.ActiveObjective)},
		{"## Background Goals", "- Earlier goals are background; follow only the Current User Request and Active Objective above."},
		{"## User Constraints", constraints},
		{"## Progress", completed},
		{"## Key Decisions", decisions},
		{"## Files and Evidence", "- Precise archived history is listed in the checkpoint wrapper's archived history map."},
		{"## Externalized State", stateFiles},
		{"## Todo State", formatTodosAsRelevanceBullets(bundle.todos, anchor)},
		{"## SubAgent State", formatSubAgentsAsBullets(bundle.subAgents)},
		{"## Open Problems", openIssues},
		{"## Next Step", quoteModelState(req.Args.NextStep)},
	}
	summary := renderFallbackSummarySections(sections, bundle.backgroundObjects)
	// The model-driven checkpoint has no model classification to fill the
	// relevance skeleton above, so the runtime-owned snapshot carries the
	// complete todo state — the same guarantee the summarization runner
	// applies, and the line-escaping that keeps todo content from escaping
	// its section. The stale bucket must stay empty: restore only drops
	// runtime todos when it sees entries there.
	summary = ensureCompactionTodoSnapshot(summary, bundle.todos)
	anchors := buildCompactionAnchors(latestCompactionAnchors(headSnapshot), bundle.originalRequest, bundle.evidenceItems)
	return withCompactionAnchors(summary, anchors)
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

// quoteModelState renders a model-authored line as a quoted bullet so `##`
// headings, compaction markers, or wrapper shapes in the model text cannot
// escape the section they belong to.
func quoteModelState(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "- (none)"
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, "> "+line)
	}
	return strings.Join(out, "\n")
}

// quoteModelStateList renders model-authored list items as quoted bullets
// ("- item" -> "> - item" per line), preventing embedded headings/markers
// from escaping the section. The fallback text is used when the list is empty.
func quoteModelStateList(items []string, empty string) string {
	if len(items) == 0 {
		return "- " + empty
	}
	var sb strings.Builder
	for i, item := range items {
		if i > 0 {
			sb.WriteByte('\n')
		}
		for _, line := range strings.Split(item, "\n") {
			sb.WriteString("> - ")
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// quoteStateFilesSection renders the Externalized State section: every path is
// a model-declared reference quoted line by line, explicitly labeled as
// existence-unverified so the checkpoint cannot be read as a file-existence
// probe nor smuggle formatting out of its section.
func quoteStateFilesSection(paths []string) string {
	if len(paths) == 0 {
		return "- (none reported by the model)\n- Model-declared references only; existence is not verified at checkpoint time."
	}
	var sb strings.Builder
	sb.WriteString("- Model-declared references only; existence is not verified at checkpoint time. Use the read tool to load any path before relying on it:\n")
	for _, p := range paths {
		for _, line := range strings.Split(p, "\n") {
			sb.WriteString("> - ")
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ------------------------------------------------------------ helpers ----

func (b modelDrivenBarrierSnapshot) scratch() *MainAgent {
	// The reduction scratch is built on the event loop in
	// captureModelDrivenBarrierSnapshot. This accessor exists so preflight
	// reads stay on the barrier snapshot even though the field is constructed
	// there; it never touches live agent state.
	return b.scratchAgent
}

func assistantContentForToolCall(messages []message.Message, callID string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		for _, tc := range msg.ToolCalls {
			if tc.ID == callID {
				return msg.Content
			}
		}
	}
	return ""
}

// settleModelDrivenOutcome is the single settlement point for model-driven
// checkpoints that did not apply (skip / failure / cancel / discard). It
// records the lifecycle analytics event exactly once, emits the terminal TUI
// status exactly once with the model_driven trigger, and queues the transient
// continuation notice. It deliberately does NOT clear LastTokenUsage,
// autoCompactRequested, or the usage-driven failure state: the safety net
// stays armed and the next gate decides.
func (a *MainAgent) settleModelDrivenOutcome(status string, reason string, preflight *modelDrivenPreflightStats) {
	a.modelDrivenSkipNotice = strings.TrimSpace(reason)
	// The model already tried the active-reset path and it settled here
	// (skipped / failed / cancelled). The usage-driven safety net must take
	// over promptly rather than waiting out a fresh grace period: the grace
	// window exists to give a wrapping-up model a chance to actively reset,
	// and that chance was just spent. Cleared again on the next durable apply
	// / model switch / session switch.
	if status == CompactionStatusSkipped || status == CompactionStatusFailed || status == CompactionStatusCancelled {
		// Grace telemetry: only a settle that closes an *open* grace window is
		// attributed to it — a settle outside any window (no crossing yet) has
		// no grace outcome to record, even though it still exhausts the flag.
		if a.gracePeriodStartBatch != 0 {
			a.recordGracePolicyEvent("grace_closed_by_model_driven_settle")
		}
		a.gracePeriodExhausted = true
		a.gracePeriodStartBatch = 0
	}
	diagnostic := map[string]string{
		"trigger": compactionTriggerModelDriven.analyticsName(),
		"reason":  a.modelDrivenSkipNotice,
	}
	if preflight != nil {
		diagnostic["current_tokens"] = strconv.Itoa(preflight.CurrentTokens)
		diagnostic["projected_tokens"] = strconv.Itoa(preflight.ProjectedTokens)
		diagnostic["saved_tokens"] = strconv.Itoa(preflight.SavedTokens)
		diagnostic["saved_ratio_pct"] = strconv.Itoa(preflight.SavedRatioPct)
		diagnostic["current_bytes"] = strconv.Itoa(preflight.CurrentBytes)
		diagnostic["projected_bytes"] = strconv.Itoa(preflight.ProjectedBytes)
		diagnostic["checkpoint_bytes"] = strconv.Itoa(preflight.CheckpointBytes)
		diagnostic["anchor_bytes"] = strconv.Itoa(preflight.AnchorBytes)
		diagnostic["history_map_bytes"] = strconv.Itoa(preflight.HistoryMapBytes)
		diagnostic["continuation_tokens"] = strconv.Itoa(preflight.ContinuationTokens)
		diagnostic["cache_rebuild_cost"] = strconv.Itoa(preflight.CacheRebuildCost)
	}
	a.recordCompactionLifecycleEvent(status, diagnostic)
	a.emitToTUI(a.compactionStatusEvent(status, a.modelDrivenSkipNotice))
}

// settleModelDrivenSkip records a model-driven policy skip (low-gain, apply
// interval, or same-reason cooldown) with the worker-computed preflight stats.
// The verdict batch and reason from the draft update the skip-cooldown state
// so a retry with the same reason short-circuits without re-running preflight;
// structural skips ("not enough history") carry no verdict and do not touch
// the cooldown state.
func (a *MainAgent) settleModelDrivenSkip(draft *compactionDraft) {
	if draft != nil && draft.ModelDrivenSkipReason != "" && draft.ModelDrivenSkipBatch > 0 {
		a.lastModelDrivenSkipBatch = draft.ModelDrivenSkipBatch
		a.lastModelDrivenSkipReason = draft.ModelDrivenSkipReason
	}
	reason := ""
	if draft != nil {
		reason = strings.TrimSpace(draft.InfoMessage)
		reason = strings.TrimPrefix(reason, "Context checkpoint skipped: ")
	}
	if reason == "" {
		reason = "projected savings were too small"
	}
	a.settleModelDrivenOutcome(CompactionStatusSkipped, reason, draft.ModelDrivenPreflight)
}

// settleModelDrivenFailure records a failed model-driven compaction without
// clearing the usage-driven safety net.
func (a *MainAgent) settleModelDrivenFailure(err error) {
	reason := "the checkpoint request failed: " + shortCompactionFailureReason(err)
	a.settleModelDrivenOutcome(CompactionStatusFailed, reason, nil)
	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Model-driven context checkpoint failed: %v", err), Level: "warn"})
}

// settleModelDrivenCancelled records a user-cancelled or discarded
// model-driven checkpoint.
func (a *MainAgent) settleModelDrivenCancelled(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "the checkpoint request was cancelled"
	}
	a.settleModelDrivenOutcome(CompactionStatusCancelled, reason, nil)
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
