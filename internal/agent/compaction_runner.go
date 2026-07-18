package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
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
	trigger := compactionTrigger{Manual: manual}
	if !manual {
		trigger.UsageDriven = true
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
	if trigger.Manual {
		resumeKind = compactionResumeIdle
	}
	a.startCompactionAsyncWithContinuation(snapshot, planID, target, trigger, continuationPlan{kind: resumeKind, turnEpoch: target.turnEpoch}, trigger.Manual)
}

func (a *MainAgent) startCompactionAsyncWithContinuation(snapshot []message.Message, planID uint64, target compactionTarget, trigger compactionTrigger, continuation continuationPlan, manual bool) {
	// Calculate headSplit: the frozen boundary between archived head and preserved tail.
	headSplit := len(snapshot)
	if headSplit > 0 {
		headSplit = a.ctxMgr.ComputeSafeKeepBoundary(headSplit)
	}

	ctx, cancel := context.WithCancel(a.parentCtx)
	a.beginCompactionState(planID, target, trigger, continuation, headSplit, cancel)

	a.emitActivity("main", ActivityCompacting, "context")
	a.emitToTUI(CompactionStatusEvent{Status: "started"})
	a.compactionWg.Add(1)
	go func(ctx context.Context, snapshot []message.Message, planID uint64, target compactionTarget, headSplit int, manual bool) {
		defer a.compactionWg.Done()
		defer cancel()

		draft, err := a.produceCompactionDraftAsync(ctx, snapshot, manual, planID, target, headSplit)
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
	}(ctx, snapshot, planID, target, headSplit, manual)
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
func (a *MainAgent) produceCompactionDraftAsync(ctx context.Context, snapshot []message.Message, manual bool, planID uint64, target compactionTarget, headSplit int) (*compactionDraft, error) {
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
	evidenceItems := a.evidenceItemsForCompaction(snapshot, a.ctxMgr.GetMaxTokens())
	profile := a.resolveCompactionProfile(todos, subAgents, backgroundObjects, evidenceItems)

	// In async mode, we skip recentTail because [headSplit:] messages
	// will be preserved as the tail by ReplacePrefixAtomic.
	// Only apply the profile for evidence selection; set recentTail to nil.
	evidenceItems, _ = applyCompactionProfile(profile, snapshot, a.ctxMgr.GetMaxTokens(), evidenceItems)
	recentTail := []message.Message(nil) // Async: no recentTail in draft
	keyFiles := extractCompactionKeyFileCandidates(snapshot, a.projectRoot, 8)
	head, evidenceMsgs := splitMessagesForCompactionWithSelections(snapshot, recentTail, evidenceItems)
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

	index, err := nextCompactionIndex(a.sessionDir)
	if err != nil {
		return nil, fmt.Errorf("determine compaction index: %w", err)
	}

	absHistoryPath, relHistoryPath, err := a.exportCompactionHistory(head, index)
	if err != nil {
		return nil, fmt.Errorf("export compacted history: %w", err)
	}
	absHistoryMetaPath := compactionHistoryMetaPath(absHistoryPath)

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	summaryMode := "model_summary"
	backendName := config.CompactionPresetGeneric
	modelRef := ""
	summaryText, backendUsed, usedModel, summarizeErr := a.summarizeCompactionHead(head, relHistoryPath, evidenceItems, recentTail, todos, subAgents, backgroundObjects)
	if strings.TrimSpace(backendUsed) != "" {
		backendName = backendUsed
	}
	if summarizeErr != nil {
		summaryMode = "structured_fallback"
		modelRef = "fallback"
		input, inputErr := buildCompactionInputWithOptions(head, a.ctxMgr.GetMaxTokens(), evidenceItems, recentTail, false)
		if inputErr == nil {
			input.EvidenceItems = evidenceItems
			summaryText = buildStructuredFallbackSummary(relHistoryPath, input, summarizeErr, keyFiles, todos, subAgents, backgroundObjects)
		} else {
			summaryMode = "truncate_only"
			summaryText = buildTruncateOnlySummary(relHistoryPath, summarizeErr, keyFiles, todos, subAgents, backgroundObjects)
		}
	} else {
		modelRef = usedModel
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	historyRefs, err := listHistoryReferences(a.projectRoot, a.sessionDir)
	if err != nil {
		return nil, fmt.Errorf("list history references: %w", err)
	}
	summaryText = ensureCompactionSummaryKeyFiles(strings.TrimSpace(summaryText), keyFiles)
	contextSummaryMsg := message.Message{
		Role:                "user",
		Content:             buildCompactionCheckpointMessage(summaryText, historyRefs, summaryMode, evidenceItems),
		IsCompactionSummary: true,
	}

	// Async mode: NewMessages only contains [summary + evidence], no recentTail
	newMessages := []message.Message{contextSummaryMsg}

	return &compactionDraft{
		PlanID:             planID,
		Target:             target,
		NewMessages:        newMessages,
		HeadSplit:          headSplit,
		Index:              index,
		AbsHistoryPath:     absHistoryPath,
		AbsHistoryMetaPath: absHistoryMetaPath,
		RelHistoryPath:     relHistoryPath,
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

	// Capture the original first user message BEFORE entering ReplacePrefixAtomic,
	// because the rewrite callback runs while ctxmgr's write lock is held and
	// cannot itself call Snapshot() (which RLocks the same mutex). Pulling the
	// snapshot here keeps rewriteSessionAfterCompaction lock-free with respect
	// to ctxmgr.
	originalFirstUserHint := a.captureOriginalFirstUserHint()

	// Use ReplacePrefixAtomic: replace [0, headSplit) with d.NewMessages,
	// preserving [headSplit:) as tail. The under callback atomically rewrites
	// the session file.
	var backupPath string
	err := a.ctxMgr.ReplacePrefixAtomic(headSplit, d.NewMessages, func(tail []message.Message) ([]message.Message, error) {
		// Build the complete new message list: prefix (summary + evidence) + tail
		newMessages := make([]message.Message, 0, len(d.NewMessages)+len(tail))
		newMessages = append(newMessages, d.NewMessages...)
		newMessages = append(newMessages, tail...)

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

	a.resetRuntimeEvidenceFromMessages(d.NewMessages)
	a.resetContextReductionStats()
	a.clearLoopFrozenReductionPrefix()
	if a.llmClient != nil {
		a.llmClient.InvalidateRouting("context_compacted")
	}
	a.ctxMgr.ClearLastTokenUsage()
	a.saveRecoverySnapshot()
	a.clearUsageDrivenAutoCompactRequest()
	a.resetAutoCompactionFailureState()
	if d.AbsHistoryMetaPath != "" {
		meta := compactionHistoryMeta{
			Version:     1,
			HistoryFile: filepath.Base(d.AbsHistoryPath),
			Status:      compactionHistoryApplied,
			AppliedAt:   time.Now(),
		}
		if existing, err := readCompactionHistoryMeta(d.AbsHistoryMetaPath); err == nil {
			if existing.Version != 0 {
				meta.Version = existing.Version
			}
			if strings.TrimSpace(existing.HistoryFile) != "" {
				meta.HistoryFile = existing.HistoryFile
			}
			meta.ExportedAt = existing.ExportedAt
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
		d.RelHistoryPath,
		d.EvidenceCount,
		d.SummaryMode,
		blankToDefault(d.Backend, d.ModelRef),
		blankToDefault(d.Profile, string(compactionProfileContinuation)),
		backupPath,
	)
	if d.SummarizeErr != nil {
		info += fmt.Sprintf(" Summary fallback reason: %v", d.SummarizeErr)
	}
	a.emitToTUI(ToastEvent{Message: info, Level: "info"})
	a.emitToTUI(CompactionStatusEvent{Status: "succeeded"})
	a.emitToTUI(SessionRestoredEvent{PreserveRequestActivity: true})

	a.sessionReminderInjected.Store(false)

	log.Infof("context compacted (async) mode=%v summary_mode=%v backend=%v profile=%v model=%v history_path=%v backup_path=%v archived_messages=%v evidence_artifacts=%v head_split=%v", modeLabel, d.SummaryMode, d.Backend, d.Profile, d.ModelRef, d.AbsHistoryPath, backupPath, d.ArchivedCount, d.EvidenceArtifacts, headSplit)
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
		"history_path":       d.RelHistoryPath,
		"backup_path":        backupPath,
	}); err != nil {
		log.Warnf("on_after_compress hook error error=%v", err)
	}
	return nil
}

func (a *MainAgent) summarizeCompactionHead(head []message.Message, relHistoryPath string, evidenceItems []evidenceItem, recentTail []message.Message, todos []tools.TodoItem, subAgents []SubAgentInfo, backgroundObjects []recovery.BackgroundObjectState) (summary string, backendName string, modelRef string, err error) {
	modelRef = a.compactionModelRef()
	client, utilityContextLimit, err := a.newCompactionClient(modelRef)
	if err != nil {
		return "", "", "", err
	}
	client.SetOutputTokenMax(compactReservedOutput)
	keyFiles := extractCompactionKeyFileCandidates(head, a.projectRoot, 8)

	input, err := buildCompactionInputWithOptions(head, utilityContextLimit, evidenceItems, recentTail, false)
	if err != nil {
		return "", "", modelRef, err
	}
	input, err = fitCompactionInputToContextLimit(head, input, utilityContextLimit, relHistoryPath, keyFiles, todos, subAgents, backgroundObjects, compactReservedOutput)
	if err != nil {
		return "", "", modelRef, err
	}

	prompt := buildCompactionPromptWithKeyFiles(
		input,
		relHistoryPath,
		keyFiles,
		todos,
		subAgents,
		backgroundObjects,
	)

	backend := a.selectCompactionBackend(client)
	backendName = backend.Name()
	summary, modelRef, err = backend.ProduceSummary(client, modelRef, prompt)
	if err == nil {
		return summary, backendName, modelRef, nil
	}
	if !errors.Is(err, errInvalidCompactionSummary) {
		return "", backendName, modelRef, err
	}
	repairPrompt := buildCompactionRepairPrompt(prompt, err)
	if repairPrompt != "" {
		log.Debugf("compaction summary validation failed; requesting corrected summary backend=%v error=%v", backendName, err)
		repairedSummary, repairedModelRef, repairErr := backend.ProduceSummary(client, modelRef, repairPrompt)
		if repairErr == nil {
			return repairedSummary, backendName, repairedModelRef, nil
		}
		log.Debugf("compaction summary repair failed backend=%v error=%v", backendName, repairErr)
	}
	return "", backendName, modelRef, err
}

func (a *MainAgent) callCompactionEndpoint(client *llm.Client, fallbackModelRef, prompt string) (string, string, error) {
	if client == nil {
		return "", fallbackModelRef, fmt.Errorf("compaction client is nil")
	}
	client.SetSystemPrompt(compactionSystemPrompt)
	modelRef := fallbackModelRef
	ctx, cancel := context.WithTimeout(a.parentCtx, 5*time.Minute)
	defer cancel()
	go a.compactionKeepAlive(ctx)

	// Emit initial progress so TUI shows activity before the blocking call.
	promptBytes := int64(len(prompt))
	a.emitToTUI(CompactionStatusEvent{Status: "progress", Bytes: 0, Events: 1})

	resp, err := client.Compact(
		ctx,
		[]message.Message{{Role: "user", Content: prompt}},
		nil,
	)
	if err != nil {
		return "", modelRef, err
	}
	// Emit final progress with response size so TUI updates before the
	// succeeded/failed terminal event arrives.
	respBytes := int64(len(resp.Content))
	a.emitToTUI(CompactionStatusEvent{Status: "progress", Bytes: respBytes, Events: 2})

	selectedRef := client.PrimaryModelRef()
	runningRef := client.RunningModelRef()
	if strings.TrimSpace(runningRef) != "" {
		modelRef = runningRef
	} else if strings.TrimSpace(selectedRef) != "" {
		modelRef = selectedRef
	}
	summary := compactionSummaryFromResponseContent(resp.Content)
	if err := validateCompactionSummary(summary); err != nil {
		return summary, modelRef, fmt.Errorf("%w: %w", errInvalidCompactionSummary, err)
	}
	log.Debugf("compaction endpoint produced summary prompt_bytes=%v response_bytes=%v summary_len=%v", promptBytes, respBytes, len(summary))
	return summary, modelRef, nil
}

func (a *MainAgent) callCompactionSummary(client *llm.Client, fallbackModelRef, prompt string) (string, string, error) {
	if client == nil {
		return "", fallbackModelRef, fmt.Errorf("compaction client is nil")
	}
	client.SetSystemPrompt(compactionSystemPrompt)
	modelRef := fallbackModelRef
	ctx, cancel := context.WithTimeout(a.parentCtx, 5*time.Minute)
	defer cancel()

	var (
		progressBytes  int64
		progressEvents int64
	)
	callback := func(delta message.StreamDelta) {
		updated := false
		if delta.Progress != nil {
			progressBytes = delta.Progress.Bytes
			progressEvents = delta.Progress.Events
			updated = true
		} else if delta.Type == "text" || delta.Type == "thinking" {
			progressBytes += int64(len(delta.Text))
			progressEvents++
			updated = true
		}
		if updated {
			a.emitToTUI(CompactionStatusEvent{Status: "progress", Bytes: progressBytes, Events: progressEvents})
		}
	}

	releaseLLM, err := a.governor.acquireLLM(ctx, client.PrimaryModelRef())
	if err != nil {
		return "", modelRef, fmt.Errorf("acquire compaction LLM request capacity: %w", err)
	}
	defer releaseLLM()
	resp, err := client.CompleteStream(
		ctx,
		[]message.Message{{Role: "user", Content: prompt}},
		nil,
		callback,
	)
	if err != nil {
		return "", modelRef, err
	}
	a.emitToTUI(CompactionStatusEvent{Status: "progress", Bytes: progressBytes, Events: progressEvents})
	selectedRef := client.PrimaryModelRef()
	runningRef := client.RunningModelRef()
	callStatus := client.LastCallStatus()
	serviceTier := callStatus.ServiceTier
	if serviceTier == "" {
		serviceTier = client.EffectiveServiceTierForModelRef(runningRef)
	}
	a.recordUsage("main", "main", a.currentAgentName(), "compaction", selectedRef, runningRef, 0, resp.Usage, serviceTier)
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

// compactionKeepAlive sends periodic activity signals during long compaction
// requests to prevent connection timeouts. It runs in a separate goroutine
// and stops when the context is cancelled.
func (a *MainAgent) compactionKeepAlive(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.emitActivity("main", ActivityCompacting, "context")
		}
	}
}
