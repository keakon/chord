package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

type sessionRestoreResult struct {
	SessionPath  string
	MessageCount int
	TodoCount    int
	AgentCount   int
}

type loadedSessionState struct {
	SessionPath            string
	Messages               []message.Message
	TodoItems              []tools.TodoItem
	TaskRecords            map[string]*DurableTaskRecord
	ActiveRole             string
	UsageStats             analytics.SessionStats
	ContextUsage           message.TokenUsage
	LastInputTokens        int
	LastTotalContextTokens int
	SubAgentStates         []loadedSubAgentState
	MailboxMessages        []SubAgentMailboxMessage
	MailboxSeqMax          uint64
	Summary                *SessionSummary
}

type loadedSubAgentState struct {
	InstanceID              string
	TaskID                  string
	AgentDefName            string
	TaskDesc                string
	OwnerAgentID            string
	OwnerTaskID             string
	Depth                   int
	JoinToOwner             bool
	Messages                []message.Message
	State                   SubAgentState
	LastSummary             string
	PendingComplete         *AgentResult
	PendingCompleteIntent   bool
	PendingCompleteSummary  string
	PendingCompleteEnvelope json.RawMessage
	LastMailboxID           string
	LastReplyMessageID      string
	LastReplyToMailboxID    string
	LastReplyKind           string
	LastReplySummary        string
	LastArtifact            tools.ArtifactRef
}

type restoredSubAgentBuilder struct {
	state loadedSubAgentState
}

func newRestoredSubAgentBuilder(instanceID string) *restoredSubAgentBuilder {
	return &restoredSubAgentBuilder{state: loadedSubAgentState{InstanceID: instanceID}}
}

func (b *restoredSubAgentBuilder) seedFromSnapshot(snap recovery.AgentSnapshot) {
	if b.state.TaskID == "" {
		b.state.TaskID = strings.TrimSpace(snap.TaskID)
	}
	if b.state.AgentDefName == "" {
		b.state.AgentDefName = strings.TrimSpace(snap.AgentDefName)
	}
	if b.state.TaskDesc == "" {
		b.state.TaskDesc = strings.TrimSpace(snap.TaskDesc)
	}
	b.state.State = SubAgentState(strings.TrimSpace(snap.State))
	b.state.LastSummary = strings.TrimSpace(snap.LastSummary)
	b.state.OwnerAgentID = strings.TrimSpace(snap.OwnerAgentID)
	b.state.OwnerTaskID = strings.TrimSpace(snap.OwnerTaskID)
	b.state.Depth = snap.Depth
	b.state.JoinToOwner = snap.JoinToOwner
	b.state.PendingCompleteIntent = snap.PendingCompleteIntent
	b.state.PendingCompleteSummary = strings.TrimSpace(snap.PendingCompleteSummary)
	b.state.PendingCompleteEnvelope = append(json.RawMessage(nil), snap.PendingCompleteEnvelope...)
	if b.state.PendingCompleteIntent {
		b.state.PendingComplete = &AgentResult{
			Summary:  b.state.PendingCompleteSummary,
			Envelope: unmarshalCompletionEnvelope(b.state.PendingCompleteEnvelope),
		}
		if strings.TrimSpace(b.state.PendingComplete.Summary) == "" && b.state.PendingComplete.Envelope == nil {
			b.state.PendingComplete = nil
			b.state.PendingCompleteIntent = false
		}
	}
}

func (b *restoredSubAgentBuilder) overlayMeta(meta *subAgentMeta) {
	if meta == nil {
		return
	}
	if b.state.TaskID == "" {
		b.state.TaskID = strings.TrimSpace(meta.TaskID)
	}
	if b.state.State == "" {
		b.state.State = SubAgentState(strings.TrimSpace(meta.State))
	}
	if b.state.LastSummary == "" {
		b.state.LastSummary = strings.TrimSpace(meta.LastSummary)
	}
	if owner := strings.TrimSpace(meta.OwnerAgentID); owner != "" {
		b.state.OwnerAgentID = owner
	}
	if ownerTask := strings.TrimSpace(meta.OwnerTaskID); ownerTask != "" {
		b.state.OwnerTaskID = ownerTask
	}
	if meta.Depth > 0 {
		b.state.Depth = meta.Depth
	}
	b.state.PendingCompleteIntent = meta.PendingCompleteIntent
	if summary := strings.TrimSpace(meta.PendingCompleteSummary); summary != "" {
		b.state.PendingCompleteSummary = summary
	}
	b.state.PendingComplete = nil
	if b.state.PendingCompleteIntent {
		b.state.PendingComplete = &AgentResult{
			Summary:  b.state.PendingCompleteSummary,
			Envelope: normalizeCompletionEnvelope(meta.PendingCompleteEnvelope),
		}
		if strings.TrimSpace(b.state.PendingComplete.Summary) == "" && b.state.PendingComplete.Envelope == nil {
			b.state.PendingComplete = nil
			b.state.PendingCompleteIntent = false
		}
	}
	b.state.LastMailboxID = strings.TrimSpace(meta.LastMailboxID)
	b.state.LastReplyMessageID = strings.TrimSpace(meta.LastReplyMessageID)
	b.state.LastReplyToMailboxID = strings.TrimSpace(meta.LastReplyToMailboxID)
	b.state.LastReplyKind = strings.TrimSpace(meta.LastReplyKind)
	b.state.LastReplySummary = strings.TrimSpace(meta.LastReplySummary)
	b.state.LastArtifact = tools.NormalizeArtifactRef(meta.LastArtifact)
}

func (b *restoredSubAgentBuilder) overlayMailbox(mailbox restoredMailboxAgentState) {
	if b.state.State == "" {
		b.state.State = mailbox.State
	}
	if b.state.LastSummary == "" {
		b.state.LastSummary = mailbox.LastSummary
	}
}

func (b *restoredSubAgentBuilder) overlayTaskRecord(rec *DurableTaskRecord) {
	if rec == nil {
		return
	}
	b.state.JoinToOwner = rec.JoinToOwner
}

func (b *restoredSubAgentBuilder) attachTranscript(msgs []message.Message) {
	b.state.Messages = append([]message.Message(nil), msgs...)
	if b.state.TaskDesc == "" {
		for _, m := range msgs {
			if m.Role == "user" {
				b.state.TaskDesc = m.Content
				break
			}
		}
	}
}

func (b *restoredSubAgentBuilder) normalize() {
	if b.state.AgentDefName == "" {
		if idx := strings.LastIndex(b.state.InstanceID, "-"); idx > 0 {
			b.state.AgentDefName = b.state.InstanceID[:idx]
		}
	}
	if b.state.TaskID == "" {
		b.state.TaskID = "restored"
	}
	if b.state.State == SubAgentStateRunning {
		b.state.State = SubAgentStateIdle
	}
}

func (b *restoredSubAgentBuilder) build() loadedSubAgentState {
	return b.state
}

func (r sessionRestoreResult) infoMessage() string {
	msg := fmt.Sprintf("Resumed session from %s: %d messages restored", filepath.Base(r.SessionPath), r.MessageCount)
	if r.TodoCount > 0 {
		msg += fmt.Sprintf(", %d todos restored", r.TodoCount)
	}
	if r.AgentCount > 0 {
		msg += fmt.Sprintf(", %d agents restored", r.AgentCount)
	}
	return msg
}

func (a *MainAgent) projectSessionsDir() (string, error) {
	locator, err := config.DefaultPathLocator()
	if err != nil {
		return "", err
	}
	pl, err := locator.EnsureProject(a.projectRoot)
	if err != nil {
		return "", err
	}
	return pl.ProjectSessionsDir, nil
}

func (a *MainAgent) resolveResumeSessionPath(sessionID string) (string, error) {
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		return "", err
	}
	if sessionID != "" {
		sessionPath := filepath.Join(sessionsDir, sessionID)
		mainPath := filepath.Join(sessionPath, "main.jsonl")
		info, err := os.Stat(mainPath)
		if err != nil || info.Size() == 0 {
			return "", fmt.Errorf("session %s not found or has no messages", sessionID)
		}
		return sessionPath, nil
	}
	sessionPath := recovery.FindMostRecentSession(sessionsDir, a.sessionDir)
	if sessionPath == "" {
		return "", fmt.Errorf("no previous sessions found in %s", sessionsDir)
	}
	return sessionPath, nil
}

func (a *MainAgent) loadMainTranscript(tmpRecovery *recovery.RecoveryManager, sessionPath string) ([]message.Message, time.Duration, time.Duration, error) {
	mainLoadStarted := time.Now()
	msgs, err := tmpRecovery.LoadMessages("main")
	mainLoadDuration := time.Since(mainLoadStarted)
	if err != nil || len(msgs) == 0 {
		return nil, mainLoadDuration, 0, fmt.Errorf("no messages found in session %s", filepath.Base(sessionPath))
	}
	normalizeStarted := time.Now()
	msgs = normalizeRestoredMessages(msgs)
	normalizeDuration := time.Since(normalizeStarted)
	return msgs, mainLoadDuration, normalizeDuration, nil
}

func (a *MainAgent) restoreUsageEvidence(loaded *loadedSessionState, sessionPath string) (time.Duration, int64) {
	if loaded == nil {
		return 0, 0
	}
	if ledger := analytics.NewUsageLedger(sessionPath, a.projectRoot); ledger != nil {
		usageStarted := time.Now()
		if ledgerStats, eventCount, ledgerErr := ledger.BuildSessionStats(); ledgerErr != nil {
			log.Warnf("failed to rebuild usage stats from usage ledger session=%v error=%v", sessionPath, ledgerErr)
		} else if eventCount > 0 {
			loaded.UsageStats = ledgerStats
			loaded.ContextUsage = message.TokenUsage{
				InputTokens:      int(ledgerStats.InputTokens),
				OutputTokens:     int(ledgerStats.OutputTokens),
				CacheReadTokens:  int(ledgerStats.CacheReadTokens),
				CacheWriteTokens: int(ledgerStats.CacheWriteTokens),
				ReasoningTokens:  int(ledgerStats.ReasoningTokens),
			}
			return time.Since(usageStarted), eventCount
		}
		return time.Since(usageStarted), 0
	}
	return 0, 0
}

func (a *MainAgent) applySessionSnapshot(loaded *loadedSessionState, sessionPath string, tmpRecovery *recovery.RecoveryManager) (time.Duration, time.Duration) {
	if loaded == nil || tmpRecovery == nil {
		return 0, 0
	}
	var subAgentRestoreDuration time.Duration
	snapshotStarted := time.Now()
	snap, snapErr := tmpRecovery.Recover()
	snapshotDuration := time.Since(snapshotStarted)
	if snapErr != nil {
		return 0, 0
	}
	loaded.TodoItems = restoreSnapshotTodos(snap.Todos)
	loaded.ActiveRole = strings.TrimSpace(snap.ActiveRole)
	loaded.LastInputTokens = snap.LastInputTokens
	loaded.LastTotalContextTokens = snap.LastTotalContextTokens
	if loaded.UsageStats.LLMCalls == 0 && (snap.UsageLLMCalls > 0 || snap.UsageInputTokens > 0) {
		loaded.ContextUsage = message.TokenUsage{
			InputTokens:      int(snap.UsageInputTokens),
			OutputTokens:     int(snap.UsageOutputTokens),
			CacheReadTokens:  int(snap.UsageCacheReadTokens),
			CacheWriteTokens: int(snap.UsageCacheWriteTokens),
			ReasoningTokens:  int(snap.UsageReasoningTokens),
		}
		loaded.UsageStats = analytics.SessionStats{
			InputTokens:      snap.UsageInputTokens,
			OutputTokens:     snap.UsageOutputTokens,
			CacheReadTokens:  snap.UsageCacheReadTokens,
			CacheWriteTokens: snap.UsageCacheWriteTokens,
			ReasoningTokens:  snap.UsageReasoningTokens,
			LLMCalls:         snap.UsageLLMCalls,
			EstimatedCost:    snap.UsageEstimatedCost,
			ByModel:          snap.UsageByModel,
			ByAgent:          snap.UsageByAgent,
		}
	}
	subAgentStarted := time.Now()
	loaded.SubAgentStates = a.loadRestoredSubAgentStates(sessionPath, tmpRecovery, snap, loaded.MailboxMessages, loaded.TaskRecords)
	subAgentRestoreDuration = time.Since(subAgentStarted)
	return snapshotDuration, subAgentRestoreDuration
}

func (a *MainAgent) loadSessionState(sessionPath string) (*loadedSessionState, error) {
	loadStarted := time.Now()
	tmpRecovery := recovery.NewRecoveryManager(sessionPath)
	defer tmpRecovery.Close()

	msgs, mainLoadDuration, normalizeDuration, err := a.loadMainTranscript(tmpRecovery, sessionPath)
	if err != nil {
		return nil, err
	}

	summaryStarted := time.Now()
	summary := buildSessionSummaryForDir(sessionPath, false)
	summaryDuration := time.Since(summaryStarted)

	loaded := &loadedSessionState{
		SessionPath: sessionPath,
		Messages:    append([]message.Message(nil), msgs...),
		Summary:     summary,
	}

	var (
		usageLedgerDuration     time.Duration
		usageLedgerEventCount   int64
		snapshotDuration        time.Duration
		subAgentRestoreDuration time.Duration
		todoFallbackDuration    time.Duration
		usageFallbackDuration   time.Duration
	)

	usageLedgerDuration, usageLedgerEventCount = a.restoreUsageEvidence(loaded, sessionPath)
	if mailboxMsgs, mailboxErr := loadSubAgentMailboxMessages(sessionPath); mailboxErr != nil {
		log.Warnf("failed to load subagent mailbox log session=%v error=%v", sessionPath, mailboxErr)
	} else {
		loaded.MailboxMessages = mailboxMsgs
		loaded.MailboxSeqMax = maxSubAgentMailboxSeq(sessionPath, mailboxMsgs)
	}
	if taskRecords, taskErr := loadDurableTaskRecords(sessionPath); taskErr != nil {
		log.Warnf("failed to load durable task registry session=%v error=%v", sessionPath, taskErr)
	} else {
		loaded.TaskRecords = taskRecords
	}

	snapshotDuration, subAgentRestoreDuration = a.applySessionSnapshot(loaded, sessionPath, tmpRecovery)
	loaded.TaskRecords = mergeDurableTaskRecords(loaded.TaskRecords, buildDurableTaskRecordsFromLoadedStates(loaded.SubAgentStates))
	loaded.SubAgentStates = filterRestorableSubAgentStates(loaded.SubAgentStates)

	if len(loaded.TodoItems) == 0 {
		todoStarted := time.Now()
		if rebuilt := rebuildTodosFromMessages(msgs); len(rebuilt) > 0 {
			loaded.TodoItems = rebuilt
		}
		todoFallbackDuration = time.Since(todoStarted)
	}

	if loaded.UsageStats.LLMCalls == 0 {
		usageFallbackStarted := time.Now()
		var sumInput, sumOutput, sumCacheR, sumCacheW, sumReasoning int64
		var llmCalls int64
		for _, m := range msgs {
			if m.Usage == nil {
				continue
			}
			sumInput += int64(m.Usage.InputTokens)
			sumOutput += int64(m.Usage.OutputTokens)
			sumCacheR += int64(m.Usage.CacheReadTokens)
			sumCacheW += int64(m.Usage.CacheWriteTokens)
			sumReasoning += int64(m.Usage.ReasoningTokens)
			llmCalls++
		}
		if llmCalls > 0 {
			loaded.ContextUsage = message.TokenUsage{
				InputTokens:      int(sumInput),
				OutputTokens:     int(sumOutput),
				CacheReadTokens:  int(sumCacheR),
				CacheWriteTokens: int(sumCacheW),
				ReasoningTokens:  int(sumReasoning),
			}
			loaded.UsageStats = analytics.SessionStats{
				InputTokens:      sumInput,
				OutputTokens:     sumOutput,
				CacheReadTokens:  sumCacheR,
				CacheWriteTokens: sumCacheW,
				ReasoningTokens:  sumReasoning,
				LLMCalls:         llmCalls,
				EstimatedCost:    0,
				ByModel:          nil,
				ByAgent:          nil,
			}
		}
		usageFallbackDuration = time.Since(usageFallbackStarted)
	}

	log.Debugf("session restore load timing session=%v messages=%v subagents=%v usage_events=%v load_main_ms=%v normalize_main_ms=%v build_summary_ms=%v usage_ledger_ms=%v snapshot_ms=%v restore_subagents_ms=%v todos_fallback_ms=%v usage_fallback_ms=%v total_ms=%v", filepath.Base(sessionPath), len(loaded.Messages), len(loaded.SubAgentStates), usageLedgerEventCount, mainLoadDuration.Milliseconds(), normalizeDuration.Milliseconds(), summaryDuration.Milliseconds(), usageLedgerDuration.Milliseconds(), snapshotDuration.Milliseconds(), subAgentRestoreDuration.Milliseconds(), todoFallbackDuration.Milliseconds(), usageFallbackDuration.Milliseconds(), time.Since(loadStarted).Milliseconds())

	return loaded, nil
}

func (a *MainAgent) loadRestoredSubAgentStates(sessionPath string, rm *recovery.RecoveryManager, snap *recovery.SessionSnapshot, mailboxMsgs []SubAgentMailboxMessage, taskRecords map[string]*DurableTaskRecord) []loadedSubAgentState {
	if rm == nil || snap == nil || a.llmFactory == nil {
		return nil
	}
	subIDs := make([]string, 0, len(snap.ActiveAgents))
	for _, as := range snap.ActiveAgents {
		if id := strings.TrimSpace(as.InstanceID); id != "" {
			subIDs = append(subIDs, id)
		}
	}
	if len(subIDs) == 0 {
		subIDs = listSubAgentMetaIDs(sessionPath)
	}
	if len(subIDs) == 0 {
		return nil
	}

	snapMeta := make(map[string]recovery.AgentSnapshot, len(snap.ActiveAgents))
	for _, as := range snap.ActiveAgents {
		snapMeta[as.InstanceID] = as
	}
	mailboxByAgent := latestMailboxByAgentFromMessages(mailboxMsgs)

	states := make([]loadedSubAgentState, 0, len(subIDs))
	for _, id := range subIDs {
		builder := newRestoredSubAgentBuilder(id)
		if snapState, ok := snapMeta[id]; ok {
			builder.seedFromSnapshot(snapState)
		}
		meta, metaErr := loadSubAgentMeta(sessionPath, id)
		if metaErr != nil {
			log.Warnf("loadRestoredSubAgentStates: failed to load subagent meta id=%v error=%v", id, metaErr)
		}
		builder.overlayMeta(meta)
		taskID := builder.state.TaskID
		rec := cloneDurableTaskRecord(taskRecords[taskID])
		var (
			msgs []message.Message
			err  error
		)
		if rec != nil {
			msgs, err = loadTaskHistoryMessages(rm, rec)
		} else {
			msgs, err = rm.LoadMessages(id)
			msgs = normalizeRestoredMessages(msgs)
		}
		if err != nil {
			log.Warnf("loadRestoredSubAgentStates: failed to load messages, skipping id=%v error=%v", id, err)
			continue
		}
		builder.overlayMailbox(mailboxByAgent[id])
		builder.overlayTaskRecord(rec)
		builder.attachTranscript(msgs)
		builder.normalize()
		states = append(states, builder.build())
	}
	return states
}

func (a *MainAgent) activateLoadedSession(loaded *loadedSessionState) sessionRestoreResult {
	if loaded == nil {
		return sessionRestoreResult{}
	}

	a.ctxMgr.RestoreMessages(append([]message.Message(nil), loaded.Messages...))
	a.resetRuntimeEvidenceFromMessages(loaded.Messages)
	a.ctxMgr.RestoreStats(loaded.ContextUsage)
	a.ctxMgr.SetLastInputTokens(loaded.LastInputTokens)
	a.ctxMgr.SetLastTotalContextTokens(loaded.LastTotalContextTokens)
	if a.usageTracker != nil {
		a.usageTracker.RestoreStats(loaded.UsageStats)
	}
	a.todoMu.Lock()
	a.todoItems = append([]tools.TodoItem(nil), loaded.TodoItems...)
	a.todoMu.Unlock()
	if a.lspSessionLoadFn != nil {
		a.lspSessionLoadFn(loaded.Messages)
	}
	var invokedSkills []*skill.Meta
	if len(loaded.Messages) > 0 {
		invokedSkills = rebuildInvokedSkillsFromMessages(loaded.Messages, a.visibleSkillsSnapshot())
	}
	a.skillsMu.Lock()
	a.invokedSkills = make(map[string]*skill.Meta)
	for _, meta := range invokedSkills {
		if meta == nil {
			continue
		}
		a.invokedSkills[meta.Name] = meta
	}
	a.skillsMu.Unlock()

	if a.recovery != nil {
		a.recovery.Close()
	}
	a.sessionEpoch++
	a.sessionDir = loaded.SessionPath
	a.recovery = recovery.NewRecoveryManager(loaded.SessionPath)
	a.usageLedger = analytics.NewUsageLedger(loaded.SessionPath, a.projectRoot)
	summary := cloneSessionSummary(loaded.Summary)
	if summary != nil {
		summary.Locked = a.sessionLock != nil
	}
	a.setSessionSummary(summary)
	a.resetSessionBuildState()
	a.setTaskRecords(loaded.TaskRecords)
	advanceInstanceCountersForTaskRecords(loaded.TaskRecords)
	if nextAdhoc := nextAdhocSeqFromTaskRecords(loaded.TaskRecords); nextAdhoc > 0 {
		a.adhocSeq.Store(nextAdhoc)
	}
	a.persistTaskRegistry()
	if err := a.restoreMainRoleFromSession(loaded.ActiveRole); err != nil {
		log.Warnf("restore session role failed session=%v role=%v error=%v", loaded.SessionPath, loaded.ActiveRole, err)
	}
	if loaded.MailboxSeqMax > 0 {
		a.subAgentMailboxSeq.Store(loaded.MailboxSeqMax)
	}
	if a.sessionTargetChangedFn != nil {
		a.sessionTargetChangedFn(loaded.SessionPath)
	}
	a.subAgentInbox = newSubAgentInbox()
	for _, msg := range loaded.MailboxMessages {
		if msg.Consumed {
			continue
		}
		if strings.TrimSpace(msg.OwnerAgentID) != "" {
			a.enqueueOwnedSubAgentMailbox(msg)
			continue
		}
		if msg.Kind == SubAgentMailboxKindProgress {
			a.subAgentInbox.progress[msg.AgentID] = msg
			continue
		}
		if msg.Priority == SubAgentMailboxPriorityInterrupt || msg.Priority == SubAgentMailboxPriorityUrgent {
			a.subAgentInbox.urgent = append(a.subAgentInbox.urgent, msg)
		} else {
			a.subAgentInbox.normal = append(a.subAgentInbox.normal, msg)
		}
	}
	for agentID := range a.ownedSubAgentMailboxes {
		a.drainOwnedSubAgentMailboxes(agentID)
	}
	a.refreshSubAgentInboxSummary()

	agentCount := a.restoreLoadedSubAgents(loaded.SubAgentStates)
	return sessionRestoreResult{
		SessionPath:  loaded.SessionPath,
		MessageCount: len(loaded.Messages),
		TodoCount:    len(loaded.TodoItems),
		AgentCount:   agentCount,
	}
}

func (a *MainAgent) restoreMainRoleFromSession(roleName string) error {
	roleName = strings.TrimSpace(roleName)
	if roleName == "" {
		return nil
	}

	cfg, ok := a.agentConfigs[roleName]
	if !ok || cfg == nil {
		return fmt.Errorf("unknown restored role %q", roleName)
	}

	a.stateMu.Lock()
	a.activeConfig = cfg
	a.stateMu.Unlock()
	a.clearSystemPromptOverride()
	a.rebuildRuleset()
	a.refreshSystemPrompt()

	appliedModel := false
	if nextRef := a.defaultRoleModelRef(cfg); nextRef != "" {
		if err := a.ApplyInitialModel(nextRef); err != nil {
			return fmt.Errorf("apply restored role %q model %q: %w", roleName, nextRef, err)
		}
		appliedModel = true
	}
	a.mainModelPolicyDirty.Store(!appliedModel)
	return nil
}

type restoredMailboxAgentState struct {
	State       SubAgentState
	LastSummary string
}

func loadSubAgentMailboxMessages(sessionPath string) ([]SubAgentMailboxMessage, error) {
	path := filepath.Join(sessionPath, "subagents", "mailbox.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	out := make([]SubAgentMailboxMessage, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg SubAgentMailboxMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	acks, err := loadSubAgentMailboxAcks(sessionPath)
	if err != nil {
		return nil, err
	}
	return applyMailboxAcks(out, acks), nil
}

func latestMailboxByAgentFromMessages(msgs []SubAgentMailboxMessage) map[string]restoredMailboxAgentState {
	out := make(map[string]restoredMailboxAgentState)
	for _, msg := range msgs {
		if strings.TrimSpace(msg.AgentID) == "" {
			continue
		}
		state := SubAgentStateIdle
		switch msg.Kind {
		case SubAgentMailboxKindProgress:
			// Progress is historical telemetry, not proof that a restored worker
			// is still live/runnable. Restored workers do not hold semaphore slots
			// and must not be resumed through the normal focused running path.
			state = SubAgentStateIdle
		case SubAgentMailboxKindCompleted:
			state = SubAgentStateCompleted
		case SubAgentMailboxKindBlocked, SubAgentMailboxKindDecisionRequired, SubAgentMailboxKindRiskAlert, SubAgentMailboxKindDirectionChange:
			state = SubAgentStateWaitingPrimary
		}
		out[msg.AgentID] = restoredMailboxAgentState{
			State:       state,
			LastSummary: msg.Summary,
		}
	}
	return out
}

func maxSubAgentMailboxSeq(sessionPath string, msgs []SubAgentMailboxMessage) uint64 {
	maxSeq := uint64(0)
	for _, msg := range msgs {
		if seq := parseSubAgentMailboxMessageSeq(msg.MessageID); seq > maxSeq {
			maxSeq = seq
		}
	}
	acks, err := loadSubAgentMailboxAcks(sessionPath)
	if err == nil {
		for id := range acks {
			if seq := parseSubAgentMailboxMessageSeq(id); seq > maxSeq {
				maxSeq = seq
			}
		}
	}
	return maxSeq
}

func parseSubAgentMailboxMessageSeq(id string) uint64 {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0
	}
	idx := strings.LastIndex(id, "-")
	if idx < 0 || idx == len(id)-1 {
		return 0
	}
	suffix := id[idx+1:]
	n, err := strconv.ParseUint(suffix, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (a *MainAgent) restoreLoadedSubAgents(states []loadedSubAgentState) int {
	if len(states) == 0 || a.recovery == nil || a.llmFactory == nil {
		return 0
	}

	workDir, _ := os.Getwd()
	restored := 0
	for _, state := range states {
		agentDefName := state.AgentDefName
		agentDef, err := a.resolveAgentDef(agentDefName)
		if err != nil {
			agentDef, err = a.resolveAgentDef("builder")
			if err != nil {
				log.Warnf("restoreLoadedSubAgents: failed to resolve agent def, skipping id=%v agent_def=%v error=%v", state.InstanceID, agentDefName, err)
				continue
			}
		}

		subLLMClient := a.llmFactory("", agentDef.Models, agentDef.Variant)
		agentRuleset := a.effectiveRuleset()
		if agentDef.Permission.Kind != 0 {
			agentPermRules := permission.ParsePermission(&agentDef.Permission)
			agentRuleset = permission.Merge(agentRuleset, agentPermRules)
		}

		var extraMCPTools []tools.Tool
		if len(agentDef.MCP) > 0 {
			extraMCPTools = a.getOrCreateAgentMCP(agentDef.MCP)
		}

		ctx, cancel := context.WithCancel(a.parentCtx)
		sub := NewSubAgent(SubAgentConfig{
			InstanceID:    state.InstanceID,
			TaskID:        state.TaskID,
			AgentDefName:  agentDef.Name,
			TaskDesc:      state.TaskDesc,
			OwnerAgentID:  state.OwnerAgentID,
			OwnerTaskID:   state.OwnerTaskID,
			Depth:         state.Depth,
			JoinToOwner:   state.JoinToOwner,
			Delegation:    agentDef.Delegation,
			SystemPrompt:  agentDef.SystemPrompt,
			LLMClient:     subLLMClient,
			Recovery:      a.recovery,
			Parent:        a,
			ParentCtx:     ctx,
			Cancel:        cancel,
			BaseTools:     a.tools,
			ExtraMCPTools: extraMCPTools,
			Ruleset:       agentRuleset,
			WorkDir:       workDir,
			VenvPath:      a.cachedVenvPath,
			SessionDir:    a.sessionDir,
			AgentsMD:      a.cachedAgentsMDSnapshot(),
			Skills:        a.loadedSkillsSnapshot(),
			ModelName:     a.ModelName(),
		})
		if len(state.Messages) > 0 {
			sub.RestoreMessages(append([]message.Message(nil), state.Messages...))
		}
		restoreState := state.State
		if restoreState == "" {
			restoreState = SubAgentStateIdle
		}
		restoreSummary := state.LastSummary
		if strings.TrimSpace(restoreSummary) == "" {
			restoreSummary = fmt.Sprintf("Restored agent %s", state.InstanceID)
		}
		sub.setState(restoreState, restoreSummary)
		if state.PendingCompleteIntent {
			pending := state.PendingComplete
			if pending == nil && strings.TrimSpace(state.PendingCompleteSummary) != "" {
				pending = &AgentResult{Summary: state.PendingCompleteSummary, Envelope: unmarshalCompletionEnvelope(state.PendingCompleteEnvelope)}
			}
			sub.setPendingCompleteIntent(pending)
		}
		a.noteSubAgentStateTransition(sub, restoreState)
		if strings.TrimSpace(state.LastMailboxID) != "" {
			sub.setLastMailboxID(state.LastMailboxID)
		}
		if strings.TrimSpace(state.LastReplyMessageID) != "" || strings.TrimSpace(state.LastReplySummary) != "" {
			sub.setReplyThread(state.LastReplyMessageID, state.LastReplyToMailboxID, state.LastReplyKind, state.LastReplySummary)
		}
		if strings.TrimSpace(state.LastArtifact.RelPath) != "" || strings.TrimSpace(state.LastArtifact.ID) != "" {
			sub.setLastArtifact(state.LastArtifact)
		}

		a.mu.Lock()
		a.subAgents[state.InstanceID] = sub
		a.mu.Unlock()
		a.drainOwnedSubAgentMailboxes(state.InstanceID)
		a.persistSubAgentMeta(sub)
		a.syncTaskRecordFromSub(sub, "")
		go sub.runLoop()
		restoreStatus := string(restoreState)
		switch restoreState {
		case SubAgentStateCompleted:
			restoreStatus = "done"
		case SubAgentStateFailed:
			restoreStatus = "error"
		}
		a.emitToTUI(AgentStatusEvent{
			AgentID: state.InstanceID,
			Status:  restoreStatus,
			Message: restoreSummary,
		})
		AdvancePastID(state.InstanceID)
		restored++
		log.Infof("SubAgent restored instance=%v task_id=%v agent_def=%v messages=%v", state.InstanceID, state.TaskID, agentDef.Name, len(state.Messages))
	}
	return restored
}

func (a *MainAgent) restoreSessionState(sessionPath string) (sessionRestoreResult, error) {
	restoreStarted := time.Now()
	loaded, err := a.loadSessionState(sessionPath)
	if err != nil {
		return sessionRestoreResult{}, err
	}
	activateStarted := time.Now()
	result := a.activateLoadedSession(loaded)
	log.Debugf("session restore activate timing session=%v messages=%v subagents=%v activate_ms=%v total_ms=%v", filepath.Base(sessionPath), len(loaded.Messages), len(loaded.SubAgentStates), time.Since(activateStarted).Milliseconds(), time.Since(restoreStarted).Milliseconds())
	return result, nil
}

func rebuildInvokedSkillsFromMessages(msgs []message.Message, visible []*skill.Meta) []*skill.Meta {
	if len(msgs) == 0 {
		return nil
	}
	visibleByName := make(map[string]*skill.Meta, len(visible))
	for _, meta := range visible {
		if meta == nil || strings.TrimSpace(meta.Name) == "" {
			continue
		}
		copyMeta := *meta
		copyMeta.Discovered = true
		visibleByName[copyMeta.Name] = &copyMeta
	}
	invoked := make(map[string]*skill.Meta)
	for _, msg := range msgs {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.Name != "Skill" {
				continue
			}
			name := extractToolArgument(tc.Name, tc.Args)
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if meta, ok := visibleByName[name]; ok {
				copyMeta := *meta
				copyMeta.Invoked = true
				invoked[name] = &copyMeta
				continue
			}
			invoked[name] = &skill.Meta{Name: name, Invoked: true}
		}
	}
	if len(invoked) == 0 {
		return nil
	}
	out := make([]*skill.Meta, 0, len(invoked))
	for _, meta := range invoked {
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// rebuildTodosFromMessages scans messages in reverse to find the last TodoWrite
// tool call and reconstructs todo items from its arguments. Returns nil if no
// TodoWrite call is found.
func rebuildTodosFromMessages(msgs []message.Message) []tools.TodoItem {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, tc := range msgs[i].ToolCalls {
			if tc.Name != "TodoWrite" {
				continue
			}
			var args struct {
				Todos []struct {
					ID         string `json:"id"`
					Content    string `json:"content"`
					Status     string `json:"status"`
					ActiveForm string `json:"active_form,omitempty"`
				} `json:"todos"`
			}
			if json.Unmarshal(tc.Args, &args) != nil || len(args.Todos) == 0 {
				continue
			}
			items := make([]tools.TodoItem, len(args.Todos))
			for j, t := range args.Todos {
				items[j] = tools.TodoItem{
					ID:         t.ID,
					Content:    t.Content,
					Status:     t.Status,
					ActiveForm: t.ActiveForm,
				}
			}
			return items
		}
	}
	return nil
}

// RestoreSessionAtStartup preloads the current session directory before the
// event loop starts, so the first on_session_start hook and all new writes
// target the resumed session directly.
func (a *MainAgent) RestoreSessionAtStartup() error {
	result, err := a.restoreSessionState(a.sessionDir)
	if err != nil {
		return err
	}
	a.stateMu.Lock()
	a.startupResumePending = true
	a.startupResumeSessionID = filepath.Base(result.SessionPath)
	a.startupResumeLoadedAt = time.Now()
	a.stateMu.Unlock()
	log.Infof("session restored at startup session=%v message_count=%v todo_count=%v", result.SessionPath, result.MessageCount, result.TodoCount)
	// Clean up orphan compaction files from previous cancelled compactions.
	// Files with status "pending_apply" older than 5 minutes and no backup are
	// considered orphaned (the compaction was cancelled but cleanup didn't run).
	cleanupStalePendingCompactions(a.sessionDir, 5*time.Minute)
	a.llmClient.SetSessionID(filepath.Base(result.SessionPath))
	a.emitToTUI(ToastEvent{Message: result.infoMessage(), Level: "info"})
	return nil
}

// handleResumeCommand handles the /resume <sessionID> slash command.
func (a *MainAgent) handleResumeCommand(sessionID string) {
	targetID := strings.TrimSpace(sessionID)
	a.emitToTUI(SessionSwitchStartedEvent{Kind: "resume", SessionID: targetID})
	sessionPath, err := a.resolveResumeSessionPath(targetID)
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: err})
		return
	}

	newLock, err := recovery.AcquireSessionLock(sessionPath)
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("resume: %w", err)})
		a.setIdleAndDrainPending()
		return
	}
	loaded, err := a.loadSessionState(sessionPath)
	if err != nil {
		_ = newLock.Release()
		a.emitToTUI(ErrorEvent{Err: err})
		a.setIdleAndDrainPending()
		return
	}

	oldRecovery, _ := a.prepareSessionSwitch()
	oldLock := a.sessionLock
	a.freezeCurrentSession(oldRecovery)
	if oldLock != nil {
		if releaseErr := oldLock.Release(); releaseErr != nil {
			log.Warnf("resume: failed to release old session lock error=%v", releaseErr)
		}
	}
	a.sessionLock = newLock
	result := a.activateLoadedSession(loaded)

	log.Infof("session resumed via /resume source_session=%v message_count=%v todo_count=%v", result.SessionPath, result.MessageCount, result.TodoCount)

	a.llmClient.SetSessionID(targetID)
	a.emitToTUI(SessionRestoredEvent{})
	a.emitToTUI(ToastEvent{Message: result.infoMessage(), Level: "info"})
	a.setIdleAndDrainPending()
}

// SessionSummary holds display info for one session (current or list entry).
type SessionSummary struct {
	ID                       string
	LastModTime              time.Time
	FirstUserMessage         string
	OriginalFirstUserMessage string // preserved across compaction
	ForkedFrom               string
	Locked                   bool
}

func (a *MainAgent) GetSessionSummary() *SessionSummary {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return cloneSessionSummary(a.sessionSummary)
}

func (a *MainAgent) ListSessionSummaries() ([]SessionSummary, error) {
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		return nil, err
	}
	list, err := recovery.ListSessions(sessionsDir, a.sessionDir)
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(list))
	for _, s := range list {
		if s.Locked {
			continue
		}
		out = append(out, SessionSummary{
			ID:                       s.ID,
			LastModTime:              s.LastModTime,
			FirstUserMessage:         s.FirstUserMessage,
			OriginalFirstUserMessage: s.OriginalFirstUserMessage,
			ForkedFrom:               s.ForkedFrom,
			Locked:                   s.Locked,
		})
	}
	return out, nil
}

func (a *MainAgent) DeleteSession(sessionID string) error {
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		return err
	}
	if err := recovery.DeleteSessionByID(sessionsDir, a.sessionDir, sessionID); err != nil {
		return fmt.Errorf("delete session %s: %w", strings.TrimSpace(sessionID), err)
	}
	return nil
}

func (a *MainAgent) ExportSession(format, path string) {
	cmd := "/export"
	if format == "json" {
		cmd += " --json"
	}
	if path != "" {
		cmd += " " + path
	}
	a.sendEvent(Event{Type: EventUserMessage, Payload: cmd})
}

func (a *MainAgent) ResumeSession() {
	a.sendEvent(Event{Type: EventUserMessage, Payload: "/resume"})
}

func (a *MainAgent) ResumeSessionID(sessionID string) {
	a.requestSessionControl(sessionControlResumeID, strings.TrimSpace(sessionID))
}

func (a *MainAgent) NewSession() {
	a.requestSessionControl(sessionControlNew, "")
}

func (a *MainAgent) StartupResumeStatus() (pending bool, sessionID string) {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.startupResumePending, strings.TrimSpace(a.startupResumeSessionID)
}

// SessionID returns the unique session identifier for this agent instance.
// Used by the C/S server to scope event subscriptions. Goroutine-safe.
func (a *MainAgent) SessionID() string {
	return a.instanceID
}

func (a *MainAgent) ExecutePlan(planPath, agentName string) {
	if agentName == "" {
		agentName = "builder"
	}
	a.sendEvent(Event{Type: EventExecutePlan, Payload: &executePlanPayload{PlanPath: planPath, AgentName: agentName}})
}
