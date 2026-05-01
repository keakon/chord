package agent

import (
	"context"
	"fmt"
	"github.com/keakon/golog/log"
	"os"
	"path/filepath"
	"strings"
	"time"

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
func (a *MainAgent) scheduleCompactionAsync(snapshot []message.Message, planID uint64, target compactionTarget, trigger compactionTrigger) {
	a.startCompactionAsyncWithContinuation(snapshot, planID, target, trigger, continuationPlan{kind: compactionResumeIdle, turnEpoch: target.turnEpoch}, trigger.Manual)
}

func (a *MainAgent) startCompactionAsyncWithContinuation(snapshot []message.Message, planID uint64, target compactionTarget, trigger compactionTrigger, continuation continuationPlan, manual bool) {
	// Calculate headSplit: the frozen boundary between archived head and preserved tail.
	headSplit := len(snapshot)
	if headSplit > 0 {
		headSplit = a.ctxMgr.ComputeSafeKeepBoundary(headSplit)
	}

	ctx, cancel := context.WithCancel(a.parentCtx)
	a.compactionState = compactionState{
		running:      true,
		planID:       planID,
		target:       target,
		trigger:      trigger,
		discard:      false,
		continuation: continuation,
		headSplit:    headSplit,
		cancel:       cancel,
	}

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
	a.scheduleCompaction(false)
}

func (a *MainAgent) trySkipUsageDrivenCompactionAfterShrink(snapshot []message.Message) bool {
	if !a.autoCompactRequested.Load() || snapshot == nil {
		return false
	}
	maxTokens := a.ctxMgr.GetMaxTokens()
	if maxTokens <= 0 {
		return false
	}
	threshold := a.ctxMgr.Threshold()
	if threshold <= 0 {
		return false
	}
	prepared := a.prepareMessagesForLLM(snapshot)
	estimate := llm.EstimateRequestInputTokens(
		a.ctxMgr.SystemPrompt().Content,
		prepared,
		a.mainLLMToolDefinitions(),
	)
	thresholdTokens := int(float64(maxTokens) * threshold)
	if estimate >= thresholdTokens {
		return false
	}
	log.Debugf("skipping durable compaction after pre-request shrink estimated_tokens=%v threshold_tokens=%v message_count=%v", estimate, thresholdTokens, len(snapshot))
	a.clearUsageDrivenAutoCompactRequest()
	a.resetAutoCompactionFailureState()
	return true
}

// maybeRunBarrierCompaction runs compaction at the ContinuationBarrier (all
// foreground tools done, about to call LLM again). Unlike maybeRunAutoCompaction
// it does not require the agent to be idle, and does not emit IdleEvent.
func (a *MainAgent) handleCompactCommand() {
	if a.turn != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/compact: context compaction requires idle state")})
		return
	}
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
		backupPath, rewriteErr = a.rewriteSessionAfterCompaction(d.Index, newMessages)
		if rewriteErr != nil {
			return nil, fmt.Errorf("rewrite compacted session: %w", rewriteErr)
		}
		return newMessages, nil
	})
	if err != nil {
		return err
	}

	a.resetRuntimeEvidenceFromMessages(d.NewMessages)
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
		if err := writeCompactionHistoryMeta(d.AbsHistoryMetaPath, meta); err != nil {
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
	a.emitToTUI(SessionRestoredEvent{})

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
	summary := strings.TrimSpace(resp.Content)
	if err := validateCompactionSummary(summary); err != nil {
		return "", modelRef, err
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
	a.recordUsage("main", "main", a.currentAgentName(), "compaction", selectedRef, runningRef, 0, resp.Usage)
	if strings.TrimSpace(runningRef) != "" {
		modelRef = runningRef
	} else if strings.TrimSpace(selectedRef) != "" {
		modelRef = selectedRef
	}
	summary := strings.TrimSpace(resp.Content)
	if err := validateCompactionSummary(summary); err != nil {
		return "", modelRef, err
	}
	return summary, modelRef, nil
}

func (a *MainAgent) compactionModelRef() string {
	if a.projectConfig != nil && strings.TrimSpace(a.projectConfig.Context.CompactModel) != "" {
		return strings.TrimSpace(a.projectConfig.Context.CompactModel)
	}
	if a.globalConfig != nil && strings.TrimSpace(a.globalConfig.Context.CompactModel) != "" {
		return strings.TrimSpace(a.globalConfig.Context.CompactModel)
	}
	return a.ProviderModelRef()
}

func (a *MainAgent) newCompactionClient(ref string) (*llm.Client, int, error) {
	if ref == "" {
		return nil, 0, fmt.Errorf("no model available for context compaction")
	}
	if a.modelSwitchFactory == nil {
		return nil, 0, fmt.Errorf("model switch factory is not configured")
	}

	client, _, contextLimit, err := a.modelSwitchFactory(ref)
	if err == nil {
		if _, rebuilt, rbErr := a.rebuildCompactionClientWithExtendedHeaderTimeout(client); rbErr == nil && rebuilt != nil {
			return rebuilt, contextLimit, nil
		} else if rbErr != nil {
			log.Warnf("failed to rebuild compaction client with extended header timeout model=%v error=%v", ref, rbErr)
		}
		return client, contextLimit, nil
	}

	selected := a.ProviderModelRef()
	if selected == "" || selected == ref {
		return nil, 0, err
	}

	log.Warnf("failed to create compact model client, falling back to selected model compact_model=%v selected_model=%v error=%v", ref, selected, err)
	client, _, contextLimit, selErr := a.modelSwitchFactory(selected)
	if selErr != nil {
		return nil, 0, fmt.Errorf("compact model failed (%v); selected model fallback also failed: %w", err, selErr)
	}
	if _, rebuilt, rbErr := a.rebuildCompactionClientWithExtendedHeaderTimeout(client); rbErr == nil && rebuilt != nil {
		return rebuilt, contextLimit, nil
	} else if rbErr != nil {
		log.Warnf("failed to rebuild fallback compaction client with extended header timeout model=%v error=%v", selected, rbErr)
	}
	return client, contextLimit, nil
}

func (a *MainAgent) rebuildCompactionClientWithExtendedHeaderTimeout(client *llm.Client) (bool, *llm.Client, error) {
	if client == nil {
		return false, nil, nil
	}
	providerCfg := client.ProviderConfig()
	if providerCfg == nil {
		return false, nil, nil
	}
	proxyURL := providerCfg.EffectiveProxyURL()
	responseHeaderTimeout := 5 * time.Minute
	totalTimeout := 5 * time.Minute
	switch providerCfg.Type() {
	case config.ProviderTypeChatCompletions:
		httpClient, err := llm.NewHTTPClientWithProxyAndHeaderTimeout(proxyURL, totalTimeout, responseHeaderTimeout)
		if err != nil {
			return false, nil, err
		}
		impl, err := llm.NewOpenAIProviderWithClient(providerCfg, httpClient, proxyURL)
		if err != nil {
			return true, nil, err
		}
		rebuilt := llm.NewClient(providerCfg, impl, client.ModelID(), client.MaxTokens(), "")
		rebuilt.SetOutputTokenMax(client.OutputTokenMax())
		rebuilt.SetVariant(client.Variant())
		return true, rebuilt, nil
	case config.ProviderTypeResponses:
		httpClient, err := llm.NewHTTPClientWithProxyAndHeaderTimeout(proxyURL, totalTimeout, responseHeaderTimeout)
		if err != nil {
			return false, nil, err
		}
		impl, err := llm.NewResponsesProviderWithClient(providerCfg, httpClient, proxyURL)
		if err != nil {
			return true, nil, err
		}
		rebuilt := llm.NewClient(providerCfg, impl, client.ModelID(), client.MaxTokens(), "")
		rebuilt.SetOutputTokenMax(client.OutputTokenMax())
		rebuilt.SetVariant(client.Variant())
		return true, rebuilt, nil
	case config.ProviderTypeMessages:
		httpClient, err := llm.NewHTTPClientWithProxyAndHeaderTimeout(proxyURL, totalTimeout, responseHeaderTimeout)
		if err != nil {
			return false, nil, err
		}
		impl, err := llm.NewAnthropicProviderWithClient(providerCfg, httpClient, proxyURL)
		if err != nil {
			return true, nil, err
		}
		rebuilt := llm.NewClient(providerCfg, impl, client.ModelID(), client.MaxTokens(), "")
		rebuilt.SetOutputTokenMax(client.OutputTokenMax())
		rebuilt.SetVariant(client.Variant())
		return true, rebuilt, nil
	default:
		return false, nil, nil
	}
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
