package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

const subAgentContextRecoveryHeadroom = 4096

// applyModelCompactionConfig resolves the next request's model through the same
// configuration path as the main agent. Read it at each request boundary so
// model switches and fallback pool cursors do not retain another model's line.
// It returns the resolved model ref (for the reminder-line lookup) and the
// compaction threshold ratio.
func (s *SubAgent) applyModelCompactionConfig() (string, float64) {
	client, _ := s.llmSnapshot()
	modelRef := ""
	if client != nil {
		modelRef = client.NextRequestModelRef()
		if limit := client.ContextLimitForModelRef(modelRef); limit > 0 {
			reserved := s.parent.effectiveCompactionReservedInput()
			s.ctxMgr.SetTokenBudgets(limit, client.InputLimitForModelRef(modelRef), client.CompactionBudgetForModelRef(modelRef), reserved)
		}
	}
	threshold := s.parent.effectiveCompactionThreshold(modelRef)
	return modelRef, threshold
}

func (s *SubAgent) recoverFromContextLength(err error) bool {
	if s == nil || s.turn == nil || !llm.IsContextLengthExceeded(err) || s.turn.SubAgentContextRecoveryCount >= 1 {
		return false
	}
	messages := s.ctxMgr.Snapshot()
	target := s.ctxMgr.GetUsableInputBudget()
	if target <= 0 {
		target = s.ctxMgr.GetMaxTokens()
	}
	if target <= 0 {
		return false
	}
	if target > subAgentContextRecoveryHeadroom {
		target -= subAgentContextRecoveryHeadroom
	} else {
		target = target * 3 / 4
	}
	compressed, ok := s.compactContextForTarget(messages, target, "provider rejected an oversized request")
	if !ok {
		return false
	}
	s.turn.SubAgentContextRecoveryCount++
	log.Infof("SubAgent context length recovery compressed history agent=%v turn_id=%v before=%v after=%v target_tokens=%v", s.instanceID, s.turn.ID, len(messages), len(compressed), target)
	s.asyncCallLLMWithFlightMarked(s.turn)
	return true
}

func (s *SubAgent) prepareContextForLLM(input llmRequestInput) (prepared llmRequestInput) {
	messages := input.Messages
	// Compression must consume and persist the full history. Preview selection
	// happens only on the returned request copy.
	defer func() { prepared.Messages = projectGeneratedImagePreviews(prepared.Messages) }()
	if s == nil || len(messages) <= 2 {
		return input
	}
	modelRef, threshold := s.applyModelCompactionConfig()
	if threshold <= 0 {
		return input
	}
	budget := s.ctxMgr.GetUsableCompactionBudget()
	if budget <= 0 {
		budget = s.ctxMgr.GetMaxTokens()
	}
	if budget <= 0 {
		return input
	}
	estimated := estimateMessagesTokens(s.ctxMgr, projectGeneratedImagePreviews(messages))
	requestBudget := s.ctxMgr.GetUsableInputBudget()
	// Pressure observation stages this request's overlay and grants the
	// model-driven grace; a deferred request goes out without the
	// deterministic compaction so the model can checkpoint first.
	if s.observeSubContextPressure(modelRef, estimated, budget, threshold, requestBudget) {
		return input
	}
	if estimated < int(float64(budget)*threshold) && (requestBudget <= 0 || estimated < requestBudget) {
		return input
	}
	target := int(float64(budget) * threshold * 0.85)
	if requestBudget > 0 {
		target = min(target, int(float64(requestBudget)*0.85))
	}
	if compressed, ok := s.compactContextForTarget(messages, target, "proactive context budget protection"); ok {
		log.Infof("SubAgent proactively compressed context agent=%v turn_id=%v before=%v after=%v estimated_tokens=%v target_tokens=%v", s.instanceID, s.turn.ID, len(messages), len(compressed), estimated, target)
		return snapshotLLMRequest(s.ctxMgr)
	}
	return input
}

func (s *SubAgent) compactContextForTarget(messages []message.Message, target int, reason string) ([]message.Message, bool) {
	compressed, ok, _ := s.compactContextForTargetWithCheckpoint(messages, target, reason, nil)
	return compressed, ok
}

// compactContextForTargetWithCheckpoint compresses the history to target and
// replaces the removed prefix with a checkpoint whose body comes from
// buildContent — the deterministic structured checkpoint when nil, the
// model-authored continuation state for a model-driven request. It returns the
// compressed list, whether the compaction applied, and the skip reason for a
// not-applied result.
func (s *SubAgent) compactContextForTargetWithCheckpoint(messages []message.Message, target int, reason string, buildContent subCheckpointContentBuilder) ([]message.Message, bool, string) {
	beforeReconcile := s.ctxMgr.MessageCount()
	if s.parent != nil {
		s.parent.flushPersist()
		if err := s.skillPublisher.reconcileAfterFlush(s.parent.persist, s.ctxMgr.Snapshot(), s.ctxMgr.Append); err != nil {
			return nil, false, subModelDrivenSkipRewriteFailed
		}
	}
	if err := s.reconcileDirectoryInstructionsAfterFlush(); err != nil {
		return nil, false, subModelDrivenSkipRewriteFailed
	}
	if s.ctxMgr.MessageCount() != beforeReconcile {
		messages = s.ctxMgr.Snapshot()
	}
	skillView := s.skillActivationView()
	compressed := s.ctxMgr.CompressForTarget(messages, target)
	if s.compactionDropsPendingPauseInputs(messages, compressed) {
		return nil, false, subModelDrivenSkipDropsPauseInput
	}
	if len(compressed) == 0 || len(compressed) >= len(messages) {
		return nil, false, subModelDrivenSkipCompression
	}
	// A successful compaction ends the pressure window that wrote them, so
	// the compacted context must not keep replaying pre-apply notices.
	compressed = dropContextNoticeMessages(compressed)
	archiveRef, archiveErr := s.archiveContextRecoveryHistory(messages)
	if archiveErr != nil {
		log.Warnf("SubAgent context compression could not archive durable history agent=%v error=%v", s.instanceID, archiveErr)
		return nil, false, subModelDrivenSkipArchiveFailed
	}
	removed := len(messages) - len(compressed)
	var content string
	if buildContent != nil {
		content = buildContent(removed, archiveRef)
	} else {
		content = buildSubAgentStructuredCheckpoint(s, messages, removed, reason, archiveRef)
	}
	checkpoint := message.Message{
		Role: message.RoleUser,
		// The checkpoint is harness text, not user input: without the flag the
		// shared IsUserAuthored predicate reports it as user-authored, and the
		// restore path would take it for the task description. The main agent's
		// checkpoint carries the same marker.
		IsCompactionSummary:       true,
		CompactionSkillActivation: buildSkillActivationCarry(messages, skillView, skillActivationEpoch(messages, skillView.TaskID)+1),
		Content:                   content,
	}
	if buildContent != nil {
		// A model-authored body labels the row so display and diagnostics can
		// tell it apart from the deterministic checkpoint.
		checkpoint.CompactionSummaryMode = compactionSummaryModeModelDriven
	}
	if len(compressed) > 1 && strings.HasPrefix(compressed[1].Content, "[system] Context was compressed") {
		compressed[1] = checkpoint
	} else {
		compressed = append(compressed[:1], append([]message.Message{checkpoint}, compressed[1:]...)...)
	}
	compressed = retainDirectoryInstructionFacts(messages, compressed)
	if estimateMessagesTokens(s.ctxMgr, compressed) >= estimateMessagesTokens(s.ctxMgr, messages) {
		return nil, false, subModelDrivenSkipCompression
	}
	if manager := s.recoveryManager(); manager != nil {
		if s.parent != nil {
			s.parent.flushPersist()
		}
		if rewriteErr := manager.RewriteLog(s.instanceID, compressed); rewriteErr != nil {
			log.Warnf("SubAgent context compression could not rewrite durable history agent=%v error=%v", s.instanceID, rewriteErr)
			return nil, false, subModelDrivenSkipRewriteFailed
		}
		if s.parent != nil {
			s.parent.persist.clearCheckpointFailures(manager, s.instanceID)
		}
	}
	s.ctxMgr.RestoreMessages(compressed)
	// A deterministic rewrite supersedes any checkpoint outcome that was
	// waiting for the next request. The model-driven caller sets its own
	// applied notice after this method returns.
	s.setCompactionTransient("")
	// The dropped messages carried whatever skill instructions this subagent
	// had loaded, so its invoked-skill state is recomputed from what remains —
	// the same rebuild restore performs, for the same reason.
	s.restoreInvokedSkills(compressed)
	stats := highLevelContextReductionStats(s.ctxMgr, messages, compressed)
	s.reductionMu.Lock()
	s.reductionStats = stats
	s.reductionMu.Unlock()
	if s.parent != nil {
		s.parent.orchestrationMetrics.subAgentCompactions.Add(1)
		if stats.TokensSaved > 0 {
			s.parent.orchestrationMetrics.subAgentTokensSaved.Add(uint64(stats.TokensSaved))
		}
	}
	// The rewrite is the new durable truth: end the notice window (fresh
	// delivery claims, grace, and compact_context mark) and tell the TUI to
	// rebuild a focused view — every surviving block's transcript index
	// shifted with the rewrite.
	s.advanceSubNoticeWindow()
	if s.parent != nil {
		s.parent.emitToTUI(AgentTranscriptRebuiltEvent{AgentID: s.instanceID})
	}
	return compressed, true, ""
}

func (s *SubAgent) archiveContextRecoveryHistory(messages []message.Message) (string, error) {
	if s == nil || strings.TrimSpace(s.sessionDir) == "" || strings.TrimSpace(s.instanceID) == "" {
		return "unavailable", nil
	}
	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal context archive: %w", err)
	}
	baseID := fmt.Sprintf("context-recovery-%d-%d-%d", s.turn.ID, s.turn.SubAgentContextRecoveryCount+1, time.Now().UnixNano())
	_, relPath, _, _, err := persistSubAgentArtifact(
		s.sessionDir,
		s.instanceID,
		baseID,
		"context_history",
		fmt.Sprintf("SubAgent %s context recovery history", s.instanceID),
		"```json\n"+string(data)+"\n```",
	)
	if err != nil {
		return "", err
	}
	if relPath == "" {
		return "unavailable", nil
	}
	return filepath.ToSlash(relPath), nil
}

// A pending checkpoint must retain its transcript identity until input
// retirement is durable. Otherwise a crash cannot reconcile staged input.
func (s *SubAgent) compactionDropsPendingPauseInputs(before, after []message.Message) bool {
	protected := make(map[string]bool)
	for _, input := range s.stagedPauseInputs {
		if input.InputID != "" {
			protected[input.InputID] = true
		}
	}
	if s.parent != nil {
		for _, input := range s.parent.PendingSessionInputs() {
			if input.OwnerTaskID == s.taskID {
				protected[input.InputID] = true
			}
		}
	}
	kept := make(map[string]bool)
	for _, msg := range after {
		if msg.InputID != "" {
			kept[msg.InputID] = true
		}
	}
	for _, msg := range before {
		if protected[msg.InputID] && !kept[msg.InputID] {
			return true
		}
	}
	return false
}
