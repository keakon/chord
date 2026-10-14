package agent

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

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
	if a.ctxMgr != nil {
		a.notePressureStage(pressureStageCompacting, a.currentOverlayWindowKey())
	}
	if a.walltime != nil {
		a.walltime.startCompactionAt(planID, a.currentAgentName(), target.turnID)
		ctx = withCompactionUsageTarget(ctx, a.walltime, planID)
	}

	// The barrier freezes this turn's next request until the checkpoint
	// settles, so there is no live foreground work left to protect: the tool
	// batch that just ended left the shared main activity slot on
	// "executing N tools" (mainSlotForeground held) and nobody else releases
	// it. Hand the slot to compaction — a plain emitCompactionSlotActivity
	// would be swallowed by the foreground guard and the status bar would show
	// the stale executing state for the whole worker run.
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
			draft.ModelDrivenRequestID = req.ToolCallID
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
	if reason, skipReason, ok := a.modelDrivenCheckpointSkipVerdict(bundle, req); ok {
		return modelDrivenSkipDraft(planID, target, reason, skipReason, modelDrivenPolicySkipRecordBatch(skipReason, bundle.currentRequestBatch), nil), nil
	}

	// Low-gain preflight BEFORE history export. A skip here must not produce
	// orphan history-*.md / metadata / backup files. The preflight stats ride
	// on the returned draft so the event-loop settlement records them once.
	checkpointBuilder := a.newModelDrivenCheckpointBuilder(ctx, bundle, snapshot, headSplit, req)
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
	absHistoryPath, sourceRefs, sourceFingerprint, err := a.exportCompactionHistory(head, headSnapshot, index, evidenceItemTopics(evidenceItems), bundle.archiveMeta)
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
	// Failed tool batches of the current turn stay live: the archival profile
	// drops every record of the head, so without them the failure would be
	// readable only as the card's excerpt and could no longer be forked.
	retainedFailures := checkpointRetainedFailureRecords(headSnapshot)
	projected := []message.Message{{Role: message.RoleUser, Content: checkpointContent, IsCompactionSummary: true}}
	projected = append(projected, retainedFailures...)
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
		Role:                     "user",
		Content:                  checkpointContent,
		IsCompactionSummary:      true,
		CompactionSummaryMode:    compactionSummaryModeModelDriven,
		CompactionRequestSources: checkpointBuilder.requestSources,
	}

	newMessages := make([]message.Message, 0, 1+len(retainedFailures))
	newMessages = append(newMessages, contextSummaryMsg)
	newMessages = append(newMessages, retainedFailures...)
	historyCommitted = true
	return &compactionDraft{
		PlanID:                     planID,
		Target:                     target,
		ModelDrivenArgsFingerprint: req.ArgsFingerprint,
		NewMessages:                newMessages,
		HeadSplit:                  headSplit,
		Index:                      index,
		AbsHistoryPath:             absHistoryPath,
		AbsHistoryMetaPath:         absHistoryMetaPath,
		SourceRefs:                 sourceRefs,
		SourceFingerprint:          sourceFingerprint,
		TransactionID:              transactionID,
		TransactionSessionDir:      bundle.archiveMeta.sessionDir,
		SummaryMode:                compactionSummaryModeModelDriven,
		Backend:                    CompactionBackendGeneric,
		Profile:                    string(compactionProfileArchival),
		ModelRef:                   "model_declared",
		Manual:                     false,
		ArchivedCount:              len(head),
		EvidenceCount:              len(evidenceItems),
		EvidenceArtifacts:          len(evidenceMsgs),
		ModelDrivenPreflight:       &preflight,
	}, nil
}
