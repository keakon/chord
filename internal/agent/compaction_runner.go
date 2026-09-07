package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) scheduleCompaction(manual bool) bool {
	if a.IsCompactionRunning() {
		log.Debugf("context compaction already in progress; skipping duplicate schedule manual=%v", manual)
		if manual {
			a.emitToTUI(InfoEvent{Message: "Context compaction is already in progress"})
		}
		return false
	}
	snapshot := a.ctxMgr.Snapshot()
	a.fireBeforeCompressHook(snapshot, manual)
	planID, target := a.nextCompactionPlan()
	trigger := compactionTriggerUsageDriven
	if manual {
		trigger = compactionTriggerManual
	}
	a.scheduleCompactionAsync(snapshot, planID, target, trigger)
	return true
}

// scheduleCompactionAsync starts a non-blocking compaction. The main event loop
// continues processing events while compaction runs in the background.
//
// Manual /compact uses a compactionResumeIdle continuation: when the summary
// is applied the agent goes back to idle and waits for the user. Automatic
// compaction (usage-driven / threshold-based) uses compactionResumeAutoContinue
// so the agent proactively spawns a new LLM turn with the compacted context
// after the summary is applied — otherwise the work would silently stall the
// moment auto compaction succeeds while no fresh user input is queued.
func (a *MainAgent) scheduleCompactionAsync(snapshot []message.Message, planID uint64, target compactionTarget, trigger compactionTrigger) {
	resumeKind := compactionResumeAutoContinue
	if trigger == compactionTriggerManual {
		resumeKind = compactionResumeIdle
	}
	a.startCompactionAsyncWithContinuation(snapshot, planID, target, trigger, continuationPlan{kind: resumeKind, turnEpoch: target.turnEpoch}, trigger == compactionTriggerManual)
}

// Draft production must reach a terminal event within this window. The
// summarize calls derive their own 5-minute timeouts from the draft context
// (one initial call plus one validation-repair retry), so the deadline and
// user cancellation interrupt them mid-request; the remaining steps are local
// file I/O. The watchdog grace covers a goroutine stuck in a call that ignores
// context cancellation (e.g. blocking file-lock or filesystem I/O): after it
// fires, a synthetic failure event releases the compaction state so automatic
// compaction can trigger again.
const (
	compactionDraftTimeout  = 12 * time.Minute
	compactionWatchdogGrace = time.Minute
	// compactionKeepAliveInterval is how often the keep-alive re-emits the
	// compacting activity during a compaction LLM request. Compaction progress
	// events only reach the TUI pill (the headless control plane drops them),
	// so without this heartbeat a long compaction leaves gateway-side
	// last-activity timestamps frozen until the 5-minute request timeout.
	compactionKeepAliveInterval = 30 * time.Second
)

// errCompactionWatchdog is the synthetic failure emitted when the draft
// goroutine never reaches a terminal event. It classifies as transient like
// other timeouts so the failure breaker still allows a retry.
var errCompactionWatchdog = errors.New("compaction draft production timed out (watchdog)")

// compactionActivityDetail is the stable detail string carried by every
// compaction-originated compacting activity emission.
const compactionActivityDetail = "context"

func (a *MainAgent) startCompactionAsyncWithContinuation(snapshot []message.Message, planID uint64, target compactionTarget, trigger compactionTrigger, continuation continuationPlan, manual bool) {
	a.recordCompactionLifecycleEvent("started", map[string]string{"trigger": trigger.analyticsName(), "message_count": strconv.Itoa(len(snapshot))})
	todos := a.GetTodos()
	subAgents := a.taskInfosForCompaction()
	backgroundObjects := spawnStatesForSnapshot()
	// evidenceItemsForCompaction reads the event-loop-owned evidence tracker;
	// capture the slice here and hand it to the worker so the draft never
	// touches the tracker from another goroutine.
	evidenceItems := a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens())
	profile := a.resolveCompactionProfile(todos, subAgents, backgroundObjects, evidenceItems)
	if a.configuredCompactionProfile() == compactionProfileAuto && continuationRequiresRawTail(continuation.kind) {
		profile = compactionProfileContinuation
	}
	headSplit := compactionHeadSplitForProfile(a.ctxMgr, profile, snapshot, a.ctxMgr.GetMaxTokens())
	// Read the original request on the event loop: captureOriginalFirstUserHint
	// consults the usage ledger and the pre-rewrite session log, and must not
	// race the apply step that replaces both.
	originalRequest := a.captureOriginalFirstUserHint()
	// Capture the archive metadata on the event loop: the worker exports the
	// head to the session directory and stamps model/project/session identity,
	// and must not read live MainAgent fields from its goroutine.
	archiveMeta := a.captureCompactionArchiveMeta()

	ctx, cancel := context.WithTimeout(a.parentCtx, compactionDraftTimeout)
	// The worker may outlive the turn that scheduled it because compaction runs
	// in parallel with the main request. Capture the scheduler's turn state now
	// instead of reading whichever turn happens to be current at HTTP dispatch.
	ctx = llm.WithResponsesTurnState(ctx, a.currentTurnResponsesState())
	a.beginCompactionState(planID, target, trigger, continuation, headSplit, cancel)
	if a.walltime != nil {
		a.walltime.startCompactionAt(planID, a.currentAgentName(), target.turnID)
	}

	a.emitCompactionSlotActivity()
	a.emitToTUI(a.compactionStatusEvent(CompactionStatusStarted, ""))
	a.compactionWg.Add(1)
	go func(ctx context.Context, snapshot []message.Message, planID uint64, target compactionTarget, headSplit int, profile compactionProfile, manual bool, originalRequest string, evidenceItems []evidenceItem, archiveMeta compactionArchiveMeta) {
		defer a.compactionWg.Done()
		defer cancel()

		// Watchdog: if this goroutine never reaches a terminal event (stuck in
		// a call that ignores ctx), emit a synthetic failure so the event loop
		// releases the compaction state. A late genuine Ready/Failed event is
		// then ignored by the plan-ID stale checks in its handler.
		draftDone := make(chan struct{})
		defer close(draftDone)
		go func() {
			timer := time.NewTimer(compactionDraftTimeout + compactionWatchdogGrace)
			defer timer.Stop()
			select {
			case <-draftDone:
			case <-timer.C:
				log.Errorf("compaction draft watchdog fired plan_id=%v head_split=%v", planID, headSplit)
				a.sendEvent(Event{Type: EventCompactionFailed, Payload: &compactionFailure{planID: planID, target: target, err: errCompactionWatchdog}})
			}
		}()

		draft, err := a.produceCompactionDraftAsync(ctx, snapshot, manual, planID, target, headSplit, profile, originalRequest, evidenceItems, archiveMeta)
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
	}(ctx, snapshot, planID, target, headSplit, profile, manual, originalRequest, evidenceItems, archiveMeta)
}

func (a *MainAgent) maybeRunAutoCompaction() {
	if !a.autoCompactRequested.Load() {
		return
	}
	decision := a.ctxMgr.AutoCompactDecision()
	if !decision.ShouldCompact {
		log.Infof("automatic context compaction request cleared before idle compaction last_input_tokens=%v threshold_tokens=%v input_budget=%v reserved_input=%v usable_input_budget=%v threshold=%v", decision.LastInputTokens, decision.ThresholdTokens, decision.InputBudget, decision.ReservedInput, decision.UsableInputBudget, decision.Threshold)
		a.clearUsageDrivenAutoCompactRequest()
		a.resetAutoCompactionFailureState()
		return
	}
	if a.isUsageDrivenAutoCompactSuppressed() {
		log.Debugf("automatic context compaction suppressed after repeated failures suppressed_until_turn=%v current_turn=%v", a.autoCompactFailureState.SuppressedUntilTurn, a.usageDrivenAutoCompactCheckTurn())
		return
	}
	if a.IsCompactionRunning() {
		return
	}
	if a.turn != nil {
		log.Warn("automatic context compaction skipped: agent not idle")
		return
	}
	log.Infof("automatic context compaction starting from idle last_input_tokens=%v threshold_tokens=%v input_budget=%v reserved_input=%v usable_input_budget=%v threshold=%v", decision.LastInputTokens, decision.ThresholdTokens, decision.InputBudget, decision.ReservedInput, decision.UsableInputBudget, decision.Threshold)
	a.scheduleCompaction(false)
}

// maybeRunBarrierCompaction runs compaction at the ContinuationBarrier (all
// foreground tools done, about to call LLM again). Unlike maybeRunAutoCompaction
// it does not require the agent to be idle, and does not emit IdleEvent.
func (a *MainAgent) handleCompactCommand() {
	a.scheduleCompaction(true)
}

// produceCompactionDraftAsync archives the head, summarizes it, and builds the
// new message list. Safe to call from a background goroutine (read-only use of
// MainAgent fields + LLM / filesystem). Tail messages [headSplit:) are preserved
// by ReplacePrefixAtomic at apply time, so the draft only carries the summary.
func (a *MainAgent) produceCompactionDraftAsync(ctx context.Context, snapshot []message.Message, manual bool, planID uint64, target compactionTarget, headSplit int, profile compactionProfile, originalRequest string, evidenceItems []evidenceItem, archiveMeta compactionArchiveMeta) (*compactionDraft, error) {
	// Check for cancellation before starting expensive work
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if len(snapshot) < 4 {
		return &compactionDraft{
			Skip:           true,
			TooFewMessages: true,
			InfoMessage:    "Not enough history to compact.",
			Manual:         manual,
			PlanID:         planID,
			Target:         target,
		}, nil
	}

	if headSplit <= 0 {
		return &compactionDraft{
			Skip:         true,
			SmallContext: true,
			InfoMessage:  "Cannot find a safe compaction boundary; nothing to compact.",
			Manual:       manual,
			PlanID:       planID,
			Target:       target,
		}, nil
	}

	todos := a.GetTodos()
	subAgents := a.taskInfosForCompaction()
	backgroundObjects := spawnStatesForSnapshot()
	headSnapshot := snapshot[:headSplit]

	evidenceItems, _ = applyCompactionProfile(a.ctxMgr, profile, headSnapshot, a.ctxMgr.GetMaxTokens(), evidenceItems)
	// Anchors are inherited from the previous checkpoint rather than re-derived,
	// so recursive compaction cannot erode the original request or a standing
	// constraint one summary at a time.
	sessionAnchors := buildCompactionAnchors(latestCompactionAnchors(snapshot), originalRequest, evidenceItems)
	recentTail := append([]message.Message(nil), snapshot[headSplit:]...)
	keyFiles := extractCompactionKeyFileCandidates(snapshot, a.projectRoot, 8)
	head, evidenceMsgs := splitMessagesForCompactionWithSelections(headSnapshot, nil, evidenceItems)
	if len(head) == 0 {
		return &compactionDraft{
			Skip:         true,
			SmallContext: true,
			InfoMessage:  "Current context is already small enough; nothing to compact.",
			Manual:       manual,
			PlanID:       planID,
			Target:       target,
		}, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	index, err := a.nextCompactionIndexForAgent(archiveMeta.sessionDir)
	if err != nil {
		return nil, fmt.Errorf("determine compaction index: %w", err)
	}

	absHistoryPath, sourceRefs, sourceFingerprint, err := a.exportCompactionHistory(head, index, evidenceItemTopics(evidenceItems), archiveMeta)
	if err != nil {
		return nil, fmt.Errorf("export compacted history: %w", err)
	}
	absHistoryMetaPath := compactionHistoryMetaPath(absHistoryPath)
	// Any failure after the archive is written must remove it: a cancelled
	// worker otherwise leaves orphan history-*.md / .status.json files behind.
	historyCommitted := false
	defer func() {
		if !historyCommitted {
			cleanupOrphanCompactionFiles(absHistoryPath)
		}
	}()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	summaryMode := message.CompactionSummaryModeModelSummary
	backendName := config.CompactionPresetGeneric
	modelRef := ""
	keepAlive := newCompactionKeepAlive(a)
	defer keepAlive.Stop()
	summaryText, backendUsed, usedModel, summarizeErr := a.summarizeCompactionHead(ctx, head, pathutil.AbbreviateHome(absHistoryPath), evidenceItems, recentTail, todos, subAgents, backgroundObjects, sessionAnchors)
	if strings.TrimSpace(backendUsed) != "" {
		backendName = backendUsed
	}
	if summarizeErr != nil {
		summaryMode = message.CompactionSummaryModeStructuredFallback
		modelRef = "fallback"
		input, inputErr := a.buildCompactionInputWithOptions(head, a.ctxMgr.GetMaxTokens(), evidenceItems, recentTail, sessionAnchors)
		if inputErr == nil {
			input.EvidenceItems = evidenceItems
			summaryText = buildStructuredFallbackSummary(pathutil.AbbreviateHome(absHistoryPath), input, summarizeErr, keyFiles, todos, subAgents, backgroundObjects)
		} else {
			summaryMode = message.CompactionSummaryModeTruncateOnly
			summaryText = buildTruncateOnlySummary(pathutil.AbbreviateHome(absHistoryPath), summarizeErr, keyFiles, todos, subAgents, backgroundObjects)
		}
	} else {
		modelRef = usedModel
	}
	// The model may classify todos by relevance, but it must not be able to
	// erase the runtime's complete todo state from the durable checkpoint.
	summaryText = ensureCompactionTodoSnapshot(summaryText, todos)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	historyChain, historyMetas, err := listCheckpointHistoryReferences(archiveMeta.sessionDir, absHistoryPath)
	if err != nil {
		return nil, fmt.Errorf("list history references: %w", err)
	}
	// The checkpoint lists the archived history files as a content map (path +
	// topics) so the model knows what each archive covers and can read the exact
	// archive back by its stable relative address. Archives still pending apply
	// (a concurrently cancelled draft whose cleanup has not landed) are left
	// out; only this draft's own archive is listed while pending.
	historyRefs := formatHistoryMapLines(historyChain, historyMetas)
	summaryText = ensureCompactionSummaryKeyFiles(strings.TrimSpace(summaryText), keyFiles)
	// A prior checkpoint inside the archived head is carried forward verbatim
	// as a final section, so the checkpoint that replaces it always references
	// the structured content of the one before (recursive compaction must not
	// erode it one summary at a time). The append runs on every mode — the
	// model summary, the structured fallback, and the truncate-only fallback —
	// because the guarantee is deterministic, not summarizer-dependent.
	summaryText = appendPriorCheckpointCarry(summaryText, latestPriorCheckpointBody(head))
	// Anchor coherence (duplicate, co-existing, or mutually contradictory
	// active/superseded constraints) is a diagnostic, not a gate: refusing the
	// checkpoint here would leave the context that triggered compaction growing
	// unchecked, which is worse than shipping a checkpoint with a redundant
	// constraint line. Problems go to the log only — they are maintainer-facing
	// and must not reach the model or the UI as conversation text.
	if problems := verifyAnchorsCoherence(sessionAnchors); len(problems) > 0 {
		log.Warnf("compaction checkpoint anchor coherence problems: %v", problems)
	}
	// The newest real user messages of the archived head (and any dangling
	// interrupted reply) are kept verbatim inside the checkpoint within the
	// retention budget, so the continuation can resume the actual work
	// boundary without re-reading the archives.
	retainedRecent := renderCheckpointRetainedRecentMessages(headSnapshot, compactRetainRecentUserMessages, a.effectiveCompactionRetainRecentTokens(), func(text string) int {
		return estimateMessageTokens(a.ctxMgr, message.Message{Role: message.RoleUser, Content: text})
	})
	checkpointContent := buildCompactionCheckpointMessage(withCompactionAnchors(summaryText, sessionAnchors), historyRefs, summaryMode, evidenceItems, retainedRecent)
	checkpointKeyFiles := extractCompactionKeyFiles(checkpointContent, a.projectRoot)
	keyFileRevisions := captureCompactionFileRevisions(checkpointKeyFiles, a.resolveCheckpointFilePath)
	contextSummaryMsg := message.Message{
		Role:                    "user",
		Content:                 checkpointContent,
		IsCompactionSummary:     true,
		CompactionSummaryMode:   summaryMode,
		CompactionFileRevisions: keyFileRevisions,
	}

	// Async mode: NewMessages only contains [summary + evidence], no recentTail
	newMessages := []message.Message{contextSummaryMsg}

	historyCommitted = true
	return &compactionDraft{
		PlanID:             planID,
		Target:             target,
		NewMessages:        newMessages,
		HeadSplit:          headSplit,
		Index:              index,
		AbsHistoryPath:     absHistoryPath,
		AbsHistoryMetaPath: absHistoryMetaPath,
		SourceRefs:         sourceRefs,
		SourceFingerprint:  sourceFingerprint,
		SummaryMode:        summaryMode,
		Backend:            backendName,
		Profile:            string(profile),
		ModelRef:           modelRef,
		SummarizeErr:       summarizeErr,
		Manual:             manual,
		ArchivedCount:      len(head),
		EvidenceCount:      len(evidenceItems),
		EvidenceArtifacts:  len(evidenceMsgs),
	}, nil
}

func (a *MainAgent) fireBeforeCompressHook(snapshot []message.Message, manual bool) {
	beforeData := map[string]any{
		"message_count":  len(snapshot),
		"context_tokens": a.ctxMgr.LastInputTokens(),
		"target_tokens":  a.ctxMgr.GetMaxTokens(),
		"manual":         manual,
	}
	if _, err := a.fireHook(a.parentCtx, hook.OnBeforeCompress, 0, beforeData); err != nil {
		log.Warnf("on_before_compress hook error error=%v", err)
	}
}

// applyCompactionDraft commits a draft produced by produceCompactionDraftAsync,
// using ReplacePrefixAtomic to preserve tail messages added during compaction.
func (a *MainAgent) applyCompactionDraft(d *compactionDraft) error {
	if d == nil || d.Skip {
		a.ctxMgr.ClearLastTokenUsage()
		a.clearUsageDrivenAutoCompactRequest()
		a.resetAutoCompactionFailureState()
		if d != nil && d.InfoMessage != "" && d.Manual {
			a.emitToTUI(InfoEvent{Message: d.InfoMessage})
		}
		return nil
	}

	return a.applyCompactionDraftAsync(d)
}

// applyCompactionDraftAsync applies a compaction draft using ReplacePrefixAtomic,
// preserving tail messages that were added during the async compaction goroutine.
func (a *MainAgent) applyCompactionDraftAsync(d *compactionDraft) error {
	headSplit := d.HeadSplit
	if len(d.SourceRefs) > 0 {
		started := time.Now()
		currentMessages := a.ctxMgr.Snapshot()
		if headSplit > len(currentMessages) {
			a.recordCompactionProvenanceEvent("boundary_mismatch", map[string]string{"source_ref_count": strconv.Itoa(len(d.SourceRefs)), "head_split": strconv.Itoa(headSplit), "current_message_count": strconv.Itoa(len(currentMessages)), "duration_us": strconv.FormatInt(time.Since(started).Microseconds(), 10)})
			return fmt.Errorf("compaction source boundary %d exceeds current message count %d", headSplit, len(currentMessages))
		}
		if err := validateCheckpointSourceRefs(d.SourceRefs, currentMessages[:headSplit]); err != nil {
			result := "source_identity_mismatch"
			if strings.Contains(err.Error(), "fingerprint changed") {
				result = "payload_fingerprint_mismatch"
			}
			a.recordCompactionProvenanceEvent(result, map[string]string{"source_ref_count": strconv.Itoa(len(d.SourceRefs)), "head_split": strconv.Itoa(headSplit), "current_message_count": strconv.Itoa(len(currentMessages)), "duration_us": strconv.FormatInt(time.Since(started).Microseconds(), 10)})
			return fmt.Errorf("validate compaction source provenance: %w", err)
		}
		if got := checkpointSourceFingerprint(d.SourceRefs); got != d.SourceFingerprint {
			a.recordCompactionProvenanceEvent("source_fingerprint_mismatch", map[string]string{"source_ref_count": strconv.Itoa(len(d.SourceRefs)), "head_split": strconv.Itoa(headSplit), "current_message_count": strconv.Itoa(len(currentMessages)), "duration_us": strconv.FormatInt(time.Since(started).Microseconds(), 10)})
			return fmt.Errorf("validate compaction source provenance: fingerprint changed")
		}
		a.recordCompactionProvenanceEvent("success", map[string]string{"source_ref_count": strconv.Itoa(len(d.SourceRefs)), "head_split": strconv.Itoa(headSplit), "current_message_count": strconv.Itoa(len(currentMessages)), "duration_us": strconv.FormatInt(time.Since(started).Microseconds(), 10)})
	} else {
		a.recordCompactionProvenanceEvent("legacy_unvalidated", map[string]string{"head_split": strconv.Itoa(headSplit)})
	}
	d.NewMessages = a.refreshCompactionFileRevisions(d.NewMessages)
	// Stamp the checkpoint message with the request batch of this apply — the
	// same value lastModelDrivenApplyBatch records below. RequestBatch is
	// otherwise only stamped on assistant messages, so after a restart whose
	// transcript is a lone checkpoint (archival apply left no live tail) the
	// restored counter would restart from 0 and read as a stale anchor,
	// re-admitting requests the interval gate meant to throttle. The stamp
	// keeps the persisted sequence continuous across the restart.
	applyBatch := a.currentRequestBatch(a.ctxMgr.Snapshot())
	d.NewMessages[0].RequestBatch = applyBatch

	// Capture the original first user message BEFORE entering ReplacePrefixAtomic,
	// because the rewrite callback runs while ctxmgr's write lock is held and
	// cannot itself call Snapshot() (which RLocks the same mutex). Pulling the
	// snapshot here keeps rewriteSessionAfterCompaction lock-free with respect
	// to ctxmgr.
	originalFirstUserHint := a.captureOriginalFirstUserHint()
	// Measured before the replace so the apply log can report what durable
	// compaction actually reclaimed. Without it the only published number is
	// "archived N messages", which says nothing about the context those
	// messages occupied and cannot be compared against what request-level
	// reduction saves.
	tokensBeforeApply := estimateMessagesTokens(a.ctxMgr, a.ctxMgr.Snapshot())
	var compactedMessages []message.Message

	// Use ReplacePrefixAtomic: replace [0, headSplit) with d.NewMessages,
	// preserving [headSplit:) as tail. The under callback atomically rewrites
	// the session file.
	var backupPath string
	err := a.ctxMgr.ReplacePrefixAtomic(headSplit, d.NewMessages, func(tail []message.Message) ([]message.Message, error) {
		// Build the complete new message list: prefix (summary + evidence) + tail
		newMessages := make([]message.Message, 0, len(d.NewMessages)+len(tail))
		newMessages = append(newMessages, d.NewMessages...)
		newMessages = append(newMessages, tail...)
		compactedMessages = newMessages

		// Rewrite session file atomically
		var rewriteErr error
		backupPath, rewriteErr = a.rewriteSessionAfterCompaction(d.Index, newMessages, originalFirstUserHint)
		if rewriteErr != nil {
			return nil, fmt.Errorf("rewrite compacted session: %w", rewriteErr)
		}
		return newMessages, nil
	})
	if err != nil {
		return err
	}

	// Durable compaction rewrites the message prefix, so any cache-friendly
	// dynamic MCP mount anchors become invalid. Revert to top-level MCP tools
	// instead of re-anchoring additional_tools / mcp_system_tools_message
	// declarations against the compacted history.
	a.forceFullMCPToolInjection()

	// Rebuild the runtime evidence candidates from the complete compacted message
	// list (checkpoint + preserved tail), not just the checkpoint. The summary
	// itself is exempt (recordEvidenceFromMessage skips IsCompactionSummary), but
	// the latest user request / correction / tool error in the preserved tail must
	// re-enter the candidates so the next compaction cannot erode them away one
	// round at a time; the archived head is replaced by the checkpoint and never
	// re-scanned.
	a.resetRuntimeEvidenceFromMessages(compactedMessages)
	a.recordCompactionAppliedAnalyticsEvent(d, headSplit, compactedMessages)
	// A durable apply starts a fresh compaction window: drop any overlay texts
	// queued for the pre-apply window (the reminder reported the old usage
	// baseline and the warning is moot once auto-compact applied). The next
	// beginMainLLMAfterPreparation re-queues against the new window claims.
	a.pendingContextPressureReminder = ""
	a.pendingCompactionWarning = ""
	// The apply itself — not the worker's history export — advances the
	// overlay window key, so requests dispatched while an async compaction is
	// still running (or was discarded) stay on the pre-apply window claim.
	a.compactionWindowGeneration++
	// A successful model-driven apply records its request batch as the new
	// interval anchor: the next model-driven request must wait
	// minModelDrivenApplyIntervalBatches batches. Every durable apply — model-
	// driven, usage-driven, or manual — clears the skip-cooldown state and the
	// threshold grace window: the prepared surface the last low-gain verdict
	// was computed on no longer exists, and the new window re-derives its own
	// grace from a fresh crossing.
	if d.SummaryMode == compactionSummaryModeModelDriven {
		a.lastModelDrivenApplyBatch = applyBatch
	}
	a.lastModelDrivenSkipBatch = 0
	a.lastModelDrivenSkipReason = ""
	a.clearCompactionGrace()
	a.resetContextReductionStats()
	a.clearPreparedReductionCache()
	if a.llmClient != nil {
		a.llmClient.ResetReplayCompatibility()
		if modelcompat.HasNativeReplayPayload(compactedMessages) {
			portableReplay := modelcompat.ReplayCompatSynthesized
			a.llmClient.MergeNextRequestTuningOverride(llm.RequestTuning{ReplayCompat: &portableReplay})
		}
		a.llmClient.InvalidateRouting("context_compacted")
	}
	a.ctxMgr.ClearLastTokenUsage()
	a.saveRecoverySnapshot()
	a.clearUsageDrivenAutoCompactRequest()
	a.resetAutoCompactionFailureState()
	if d.AbsHistoryMetaPath != "" {
		meta := compactionHistoryMeta{
			Version:           1,
			HistoryFile:       filepath.Base(d.AbsHistoryPath),
			Status:            compactionHistoryApplied,
			AppliedAt:         time.Now(),
			SourceRefs:        append([]checkpointSourceRef(nil), d.SourceRefs...),
			SourceFingerprint: d.SourceFingerprint,
		}
		if len(d.SourceRefs) > 0 {
			meta.SourceGeneration = d.SourceRefs[0].TranscriptGeneration
		}
		if existing, err := readCompactionHistoryMeta(d.AbsHistoryMetaPath); err == nil {
			if existing.Version != 0 {
				meta.Version = existing.Version
			}
			if strings.TrimSpace(existing.HistoryFile) != "" {
				meta.HistoryFile = existing.HistoryFile
			}
			meta.ExportedAt = existing.ExportedAt
			if existing.SourceGeneration != "" {
				meta.SourceGeneration = existing.SourceGeneration
			}
			if len(existing.SourceRefs) > 0 {
				meta.SourceRefs = existing.SourceRefs
			}
			if existing.SourceFingerprint != "" {
				meta.SourceFingerprint = existing.SourceFingerprint
			}
		} else if !os.IsNotExist(err) {
			log.Warnf("failed to read compaction history meta before apply path=%v error=%v", d.AbsHistoryMetaPath, err)
		}
		if err := writeCompactionHistoryMeta(a.sessionDir, d.AbsHistoryMetaPath, meta); err != nil {
			log.Warnf("failed to update compaction history meta path=%v error=%v", d.AbsHistoryMetaPath, err)
		}
	}

	modeLabel := "automatically"
	if d.Manual {
		modeLabel = "manually"
	}
	info := fmt.Sprintf(
		"Context compacted %s: archived %d messages into %s, preserved %d evidence item(s) (%s via %s, profile=%s). Backup: %s",
		modeLabel,
		d.ArchivedCount,
		pathutil.AbbreviateHome(d.AbsHistoryPath),
		d.EvidenceCount,
		d.SummaryMode,
		blankToDefault(d.Backend, d.ModelRef),
		blankToDefault(d.Profile, string(compactionProfileContinuation)),
		pathutil.AbbreviateHome(backupPath),
	)
	if d.SummarizeErr != nil {
		info += fmt.Sprintf(" Summary fallback reason: %v", d.SummarizeErr)
	}
	a.emitToTUI(ToastEvent{Message: info, Level: "info"})
	a.emitToTUI(a.compactionStatusEvent(CompactionStatusSucceeded, ""))
	a.emitModelDownshiftAppliedNotice()
	a.emitToTUI(SessionRestoredEvent{PreserveRequestActivity: true})

	tokensAfterApply := estimateMessagesTokens(a.ctxMgr, compactedMessages)
	log.Infof("context compacted (async) mode=%v summary_mode=%v backend=%v profile=%v model=%v history_path=%v backup_path=%v archived_messages=%v evidence_artifacts=%v head_split=%v tokens_before=%v tokens_after=%v tokens_reclaimed=%v", modeLabel, d.SummaryMode, d.Backend, d.Profile, d.ModelRef, d.AbsHistoryPath, backupPath, d.ArchivedCount, d.EvidenceArtifacts, headSplit, tokensBeforeApply, tokensAfterApply, max(tokensBeforeApply-tokensAfterApply, 0))
	if _, err := a.fireHook(a.parentCtx, hook.OnAfterCompress, 0, map[string]any{
		"message_count":      a.ctxMgr.MessageCount(),
		"context_tokens":     a.ctxMgr.LastTotalContextTokens(),
		"manual":             d.Manual,
		"summary_mode":       d.SummaryMode,
		"backend":            d.Backend,
		"profile":            d.Profile,
		"model":              d.ModelRef,
		"archived_messages":  d.ArchivedCount,
		"evidence_artifacts": d.EvidenceArtifacts,
		"history_path":       d.AbsHistoryPath,
		"backup_path":        backupPath,
	}); err != nil {
		log.Warnf("on_after_compress hook error error=%v", err)
	}
	return nil
}

func (a *MainAgent) summarizeCompactionHead(ctx context.Context, head []message.Message, historyPath string, evidenceItems []evidenceItem, recentTail []message.Message, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState, sessionAnchors compactionAnchors) (summary string, backendName string, modelRef string, err error) {
	modelRef = a.compactionModelRef()
	client, utilityContextLimit, err := a.newCompactionClient(modelRef)
	if err != nil {
		return "", "", "", err
	}
	client.SetOutputTokenMax(compactReservedOutput)
	keyFiles := extractCompactionKeyFileCandidates(head, a.projectRoot, 8)

	input, err := a.buildCompactionInputWithOptions(head, utilityContextLimit, evidenceItems, recentTail, sessionAnchors)
	if err != nil {
		return "", "", modelRef, err
	}
	input, err = a.fitCompactionInputToContextLimit(head, input, utilityContextLimit, historyPath, keyFiles, todos, subAgents, backgroundObjects, compactReservedOutput)
	if err != nil {
		return "", "", modelRef, err
	}

	prompt := buildCompactionPromptWithKeyFiles(
		input,
		historyPath,
		keyFiles,
		todos,
		subAgents,
		backgroundObjects,
	)

	backend := a.selectCompactionBackend(client)
	backendName = backend.Name()
	progress := newCompactionProgressReporter(a)
	summary, modelRef, err = backend.ProduceSummary(ctx, client, modelRef, prompt, progress)
	if err == nil {
		return summary, backendName, modelRef, nil
	}
	if !errors.Is(err, errInvalidCompactionSummary) {
		return "", backendName, modelRef, err
	}
	repairPrompt := buildCompactionRepairPrompt(prompt, err)
	if repairPrompt != "" {
		log.Debugf("compaction summary validation failed; requesting corrected summary backend=%v error=%v", backendName, err)
		progress.startAttempt()
		repairedSummary, repairedModelRef, repairErr := backend.ProduceSummary(ctx, client, modelRef, repairPrompt, progress)
		if repairErr == nil {
			return repairedSummary, backendName, repairedModelRef, nil
		}
		log.Debugf("compaction summary repair failed backend=%v error=%v", backendName, repairErr)
	}
	return "", backendName, modelRef, err
}

func (a *MainAgent) callCompactionEndpoint(ctx context.Context, client *llm.Client, fallbackModelRef, prompt string, progress *compactionProgressReporter) (string, string, error) {
	if client == nil {
		return "", fallbackModelRef, fmt.Errorf("compaction client is nil")
	}
	client.SetSystemPrompt(compactionSystemPrompt)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	releaseLLM, err := a.governor.acquireLLM(ctx, client.PrimaryModelRef())
	if err != nil {
		return "", fallbackModelRef, fmt.Errorf("acquire compaction LLM request capacity: %w", err)
	}
	defer releaseLLM()

	resp, err := client.Compact(
		ctx,
		[]message.Message{{Role: "user", Content: prompt}},
		nil,
		progress.Callback(),
	)
	if err != nil {
		return "", fallbackModelRef, err
	}
	progress.EmitCurrent()
	summary, modelRef, err := a.finishCompactionCall(client, fallbackModelRef, resp)
	if err != nil {
		return summary, modelRef, err
	}
	log.Debugf("compaction endpoint produced summary prompt_bytes=%v response_bytes_total=%v summary_len=%v", len(prompt), progress.Bytes(), len(summary))
	return summary, modelRef, nil
}

func (a *MainAgent) callCompactionSummary(ctx context.Context, client *llm.Client, fallbackModelRef, prompt string, progress *compactionProgressReporter) (string, string, error) {
	if client == nil {
		return "", fallbackModelRef, fmt.Errorf("compaction client is nil")
	}
	client.SetSystemPrompt(compactionSystemPrompt)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	releaseLLM, err := a.governor.acquireLLM(ctx, client.PrimaryModelRef())
	if err != nil {
		return "", fallbackModelRef, fmt.Errorf("acquire compaction LLM request capacity: %w", err)
	}
	defer releaseLLM()
	resp, err := client.CompleteStream(
		ctx,
		[]message.Message{{Role: "user", Content: prompt}},
		nil,
		progress.Callback(),
	)
	if err != nil {
		return "", fallbackModelRef, err
	}
	progress.EmitCurrent()
	return a.finishCompactionCall(client, fallbackModelRef, resp)
}

type compactionProgressReporter struct {
	agent *MainAgent

	bytes  int64
	events int64

	attemptBytesBase     int64
	attemptEventsBase    int64
	hasTransportProgress bool
	transportBytes       int64
	transportEvents      int64
	lastEmitAt           time.Time
	lastEmitBytes        int64
	lastEmitEvents       int64
}

// compactionKeepAlive re-emits the compacting activity while a compaction LLM
// request is in flight. CompactionStatusEvent progress updates are TUI-only,
// so this timer-driven signal is the only heartbeat external control planes
// (headless gateways tracking last-activity for idle reaping) receive during
// a request that streams no transport progress at all. It must live for the
// whole summarize phase, not per backend attempt, because key/model retries
// and summary-repair attempts replace each other back-to-back.
type compactionKeepAlive struct {
	agent *MainAgent
	done  chan struct{}
	stop  chan struct{}
}

func newCompactionKeepAlive(a *MainAgent) *compactionKeepAlive {
	k := &compactionKeepAlive{
		agent: a,
		done:  make(chan struct{}),
		stop:  make(chan struct{}),
	}
	go k.run()
	return k
}

func (k *compactionKeepAlive) run() {
	defer close(k.done)
	ticker := time.NewTicker(compactionKeepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-k.stop:
			return
		case <-k.agent.stoppingCh:
			return
		case <-ticker.C:
			k.tick()
		}
	}
}

// tick emits one heartbeat. Split out of run so tests can drive a single beat
// without waiting a full interval.
func (k *compactionKeepAlive) tick() {
	k.agent.emitCompactionSlotActivity()
}

func (k *compactionKeepAlive) Stop() {
	select {
	case <-k.stop:
	default:
		close(k.stop)
	}
	<-k.done
}

func newCompactionProgressReporter(a *MainAgent) *compactionProgressReporter {
	return &compactionProgressReporter{agent: a}
}

func (p *compactionProgressReporter) Callback() llm.StreamCallback {
	return func(delta message.StreamDelta) {
		if p.update(delta) {
			p.emitIfDue(time.Now())
		}
	}
}

func (p *compactionProgressReporter) update(delta message.StreamDelta) bool {
	if delta.Status != nil && strings.HasPrefix(delta.Status.Type, "retrying") {
		p.startAttempt()
		return false
	}

	if delta.Progress != nil {
		progress := delta.Progress
		if p.hasTransportProgress && (progress.Bytes < p.transportBytes || progress.Events < p.transportEvents) {
			p.startAttempt()
		}
		p.hasTransportProgress = true
		p.transportBytes = progress.Bytes
		p.transportEvents = progress.Events
		nextBytes := p.attemptBytesBase + progress.Bytes
		nextEvents := p.attemptEventsBase + progress.Events
		if nextBytes < p.bytes {
			nextBytes = p.bytes
		}
		if nextEvents < p.events {
			nextEvents = p.events
		}
		updated := nextBytes != p.bytes || nextEvents != p.events
		p.bytes = nextBytes
		p.events = nextEvents
		return updated
	}

	// Keep compaction progress on the same transport-only contract as the main
	// request lane. Content deltas are not response-byte or stream-event counts.
	return false
}

func (p *compactionProgressReporter) startAttempt() {
	p.attemptBytesBase = p.bytes
	p.attemptEventsBase = p.events
	p.hasTransportProgress = false
	p.transportBytes = 0
	p.transportEvents = 0
}

func (p *compactionProgressReporter) EmitCurrent() {
	if p == nil || p.agent == nil {
		return
	}
	p.agent.emitToTUI(CompactionStatusEvent{Status: CompactionStatusProgress, Bytes: p.bytes, Events: p.events})
}

func (p *compactionProgressReporter) emitIfDue(now time.Time) bool {
	if p == nil || !shouldEmitRequestProgress(now, p.lastEmitAt, p.bytes, p.events, p.lastEmitBytes, p.lastEmitEvents) {
		return false
	}
	p.lastEmitAt = now
	p.lastEmitBytes = p.bytes
	p.lastEmitEvents = p.events
	p.EmitCurrent()
	return true
}

func (p *compactionProgressReporter) Bytes() int64 {
	if p == nil {
		return 0
	}
	return p.bytes
}

// finishCompactionCall settles a completed summarize response identically for
// the streaming and the provider-endpoint path: record the call's usage under
// the compaction purpose (otherwise these full-context requests are invisible
// in analytics or attributed to chat), resolve which model actually ran, and
// validate the summary.
func (a *MainAgent) finishCompactionCall(client *llm.Client, fallbackModelRef string, resp *message.Response) (string, string, error) {
	selectedRef := client.PrimaryModelRef()
	runningRef := client.RunningModelRef()
	callStatus := client.LastCallStatus()
	serviceTier := callStatus.ServiceTier
	if serviceTier == "" {
		serviceTier = client.EffectiveServiceTierForModelRef(runningRef)
	}
	a.recordUsage("main", "main", a.currentAgentName(), "compaction", selectedRef, runningRef, 0, resp.Usage, serviceTier, nil)
	modelRef := fallbackModelRef
	if strings.TrimSpace(runningRef) != "" {
		modelRef = runningRef
	} else if strings.TrimSpace(selectedRef) != "" {
		modelRef = selectedRef
	}
	summary := compactionSummaryFromResponseContent(resp.Content)
	if err := validateCompactionSummary(summary); err != nil {
		return summary, modelRef, fmt.Errorf("%w: %w", errInvalidCompactionSummary, err)
	}
	return summary, modelRef, nil
}

var errInvalidCompactionSummary = errors.New("invalid compaction summary")

var (
	leadingThinkBlockRe = regexp.MustCompile(`(?is)^\s*<think\b[^>]*>.*?</think>\s*`)
	// Some providers emit reasoning on a separate channel but still leak the
	// closing </think> into the visible content, leaving an orphan close tag at
	// the very start. Strip that leading orphan directly instead of forcing a
	// summary repair retry. Inline tags mid-body are left for validation.
	leadingOrphanThinkCloseRe = regexp.MustCompile(`(?is)^\s*</think>\s*`)
)

func compactionSummaryFromResponseContent(content string) string {
	summary := strings.TrimSpace(content)
	for {
		stripped := strings.TrimSpace(leadingThinkBlockRe.ReplaceAllString(summary, ""))
		if stripped == summary {
			stripped = strings.TrimSpace(leadingOrphanThinkCloseRe.ReplaceAllString(summary, ""))
		}
		if stripped == summary {
			return summary
		}
		summary = stripped
	}
}

func buildCompactionRepairPrompt(originalPrompt string, validationErr error) string {
	originalPrompt = strings.TrimSpace(originalPrompt)
	if originalPrompt == "" || validationErr == nil {
		return ""
	}
	return fmt.Sprintf(`Write a valid compaction summary from the original compaction input below.

Requirements:
- Write only the summary.
- Keep the same facts; do not invent details.
- Use exactly the required Markdown headings, in order.
- Make "Current User Request" identify the latest user request explicitly.
- Make "Active Objective" and "Next Step" directly serve that latest user request.
- Do not restart work listed as completed, background, stale, or superseded.
- Make "Next Step" a concrete action that can be performed immediately.

Original compaction input:
%s`, originalPrompt)
}

func (a *MainAgent) configuredCompactionModelRefs() ([]string, bool, error) {
	if a.projectConfig != nil {
		if pool := strings.TrimSpace(a.projectConfig.Context.Compaction.ModelPool); pool != "" {
			refs, err := a.resolveConfiguredModelPool(pool)
			if err != nil {
				return nil, true, err
			}
			return refs, true, nil
		}
	}
	if a.globalConfig != nil {
		if pool := strings.TrimSpace(a.globalConfig.Context.Compaction.ModelPool); pool != "" {
			refs, err := a.resolveConfiguredModelPool(pool)
			if err != nil {
				return nil, true, err
			}
			return refs, true, nil
		}
	}
	return nil, false, nil
}

func (a *MainAgent) compactionModelRef() string {
	if a == nil {
		return ""
	}
	refs, configured, err := a.configuredCompactionModelRefs()
	if err == nil && configured && len(refs) > 0 {
		return refs[0]
	}
	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	if client == nil {
		return ""
	}
	return client.PrimaryModelRef()
}

func (a *MainAgent) newCompactionClient(_ string) (*llm.Client, int, error) {
	refs, configured, err := a.configuredCompactionModelRefs()
	if err != nil {
		return nil, 0, err
	}
	var client *llm.Client
	if configured {
		client, err = a.newAuxModelPoolClient(refs, 5*time.Minute, 0)
		if err != nil {
			return nil, 0, err
		}
	} else {
		client = a.newCompactionClientFromMainModelPool()
		if client == nil {
			return nil, 0, fmt.Errorf("no model pool available for context compaction")
		}
	}
	client.SetStreamRetryRounds(1)
	entry := client.PrimaryModelEntry()
	if entry.ContextLimit <= 0 {
		if ref := strings.TrimSpace(client.PrimaryModelRef()); ref != "" {
			entry.ContextLimit = client.ContextLimitForModelRef(ref)
		}
	}
	if entry.ContextLimit <= 0 {
		entry.ContextLimit = client.ContextLimitForModelRef(client.PrimaryModelRef())
	}
	return client, entry.ContextLimit, nil
}

func (a *MainAgent) newCompactionClientFromMainModelPool() *llm.Client {
	if a == nil {
		return nil
	}
	a.llmMu.RLock()
	mainClient := a.llmClient
	a.llmMu.RUnlock()
	if mainClient == nil {
		return nil
	}
	pool, selectedIdx := mainClient.ModelPoolSnapshot()
	if len(pool) == 0 {
		return nil
	}
	return newAuxClientFromPool(pool, selectedIdx, 0, a.ServiceTier())
}
