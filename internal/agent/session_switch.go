package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

func cloneMessageForForkSeed(msg message.Message) message.Message {
	cloned := msg
	if len(msg.Parts) == 0 {
		return cloned
	}
	parts := make([]message.ContentPart, len(msg.Parts))
	copy(parts, msg.Parts)
	for i := range parts {
		if !parts[i].IsBinary() {
			continue
		}
		if len(parts[i].Data) > 0 {
			parts[i].Data = append([]byte(nil), parts[i].Data...)
		}
		if parts[i].ImagePath != "" {
			parts[i].ImagePath = ""
		}
	}
	cloned.Parts = parts
	return cloned
}

const (
	sessionControlNew      = "new"
	sessionControlResumeID = "resume_id"
	sessionControlFork     = "fork"
)

type sessionControlPayload struct {
	Kind      string
	SessionID string
	MsgIndex  int // for sessionControlFork: fork before this message index
}

func (a *MainAgent) requestSessionControl(kind, sessionID string) {
	a.sendEvent(Event{
		Type: EventSessionControl,
		Payload: &sessionControlPayload{
			Kind:      kind,
			SessionID: sessionID,
		},
	})
}

func (a *MainAgent) handleSessionControlEvent(evt Event) {
	payload, ok := evt.Payload.(*sessionControlPayload)
	if !ok {
		log.Errorf("handleSessionControlEvent: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}

	switch payload.Kind {
	case sessionControlNew:
		a.handleNewSessionCommand()
	case sessionControlResumeID:
		a.handleResumeCommand(payload.SessionID)
	case sessionControlFork:
		a.handleForkSessionCommand(payload.MsgIndex)
	default:
		log.Warnf("handleSessionControlEvent: unknown kind kind=%v", payload.Kind)
	}
}

func (a *MainAgent) handleNewSessionCommand() {
	defer a.finishSessionSwitch()
	a.emitToTUI(SessionSwitchStartedEvent{Kind: "new"})
	newSessionDir, err := a.createRuntimeSessionDir()
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("create new session: %w", err)})
		return
	}

	newLock, err := recovery.AcquireSessionLock(newSessionDir)
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("new session lock: %w", err)})
		return
	}

	oldRecovery, turnCtx := a.prepareSessionSwitch()
	if err := a.ensureSessionBuilt(turnCtx); err != nil {
		_ = newLock.Release()
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("prepare new session: %w", err)})
		a.setIdleAndDrainPending()
		return
	}

	oldLock := a.sessionLock
	a.freezeCurrentSession(oldRecovery)
	if oldLock != nil {
		if releaseErr := oldLock.Release(); releaseErr != nil {
			log.Warnf("new session: failed to release old session lock error=%v", releaseErr)
		}
	}
	a.sessionLock = newLock
	a.resetSessionRuntimeState()
	a.installSessionTarget(newSessionDir)
	a.llmClient.SetSessionID(filepath.Base(newSessionDir))

	a.emitToTUI(SessionRestoredEvent{})
	a.emitToTUI(ToastEvent{
		Message: fmt.Sprintf("Started new session %s", filepath.Base(newSessionDir)),
		Level:   "info",
	})
	a.setIdleAndDrainPending()
}

func (a *MainAgent) prepareSessionSwitch() (*recovery.RecoveryManager, context.Context) {
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	a.admissionPaused.Store(true)
	a.admissionEpoch.Add(1)
	oldRecovery := a.recovery
	a.subs.cancelTaskActivations(fmt.Errorf("task activation cancelled by session switch"))
	a.cancelSubAgentAdmissions()
	a.focusedAgent.Store(nil)
	a.setFocusedTaskID("")
	a.clearSystemPromptOverride()
	a.newTurn()
	turnCtx := a.turn.Ctx
	a.pendingUserMessages = nil
	a.pendingHandoff = nil
	a.clearUsageDrivenAutoCompactRequest()
	a.resetAutoCompactionFailureState()

	stoppedBackground := tools.StopAllSpawnedForSessionSwitch()
	if stoppedBackground > 0 {
		log.Infof("terminated background objects for session switch count=%v instance=%v", stoppedBackground, a.instanceID)
	}

	if abandoned := a.abandonSubAgentsForSessionSwitch(); abandoned > 0 {
		log.Infof("abandoned running subagents for session switch count=%v instance=%v", abandoned, a.instanceID)
	}

	return oldRecovery, turnCtx
}

func (a *MainAgent) finishSessionSwitch() {
	a.admissionPaused.Store(false)
}

func (a *MainAgent) abandonSubAgentsForSessionSwitch() int {
	a.subs.mu.Lock()
	if len(a.subs.subAgents) == 0 {
		a.subs.mu.Unlock()
		return 0
	}

	ids, subs := a.subs.removeAllLiveLocked()
	a.subs.mu.Unlock()

	for i, id := range ids {
		if subs[i] != nil {
			subs[i].setState(SubAgentStateCancelled, "terminated on session switch")
			a.syncTaskRecordFromSub(subs[i], "terminated on session switch")
		}
		a.fileTrack.ReleaseAll(id)
		a.releaseSubAgentSlot(subs[i])
		if subs[i] != nil {
			tools.StopAllSpawnedForAgent(id, "terminated on session switch")
			subs[i].cancel()
			subs[i].closeLLMClient()
		}
	}

	return len(ids)
}

func (a *MainAgent) freezeCurrentSession(oldRecovery *recovery.RecoveryManager) {
	if oldRecovery == nil {
		return
	}
	a.flushPersist()
	a.saveRecoverySnapshot()
	oldRecovery.Close()
	if a.recovery == oldRecovery {
		a.recovery = nil
	}
}

func (a *MainAgent) resetSessionRuntimeState() {
	a.mailboxDeliveryPaused.Store(false)
	a.subAgentMailboxIDsMu.Lock()
	a.subAgentMailboxIDs = make(map[string]struct{})
	a.subAgentMailboxConsumed = make(map[string]struct{})
	a.subAgentMailboxIDsMu.Unlock()
	loopWasEnabled := a.loopState.Enabled
	a.loopState.disable()
	a.pendingLoopContinuation = nil
	a.pendingLSPDiagnosticOverlay = ""
	a.pendingRecoveryPrompt = ""
	a.pendingAutoContinuePrompt = ""
	a.pendingAutoContinueReplayPrompt = ""
	a.clearPendingCompactionResume()
	if loopWasEnabled {
		a.refreshSystemPrompt()
		a.emitLoopStateChanged()
	}
	a.ctxMgr.RestoreMessages(nil)
	a.fileTrack = filelock.NewFileTracker()
	a.clearEvidenceCandidates()
	a.ctxMgr.RestoreStats(message.TokenUsage{})
	a.ctxMgr.SetLastInputTokens(0)
	a.ctxMgr.SetLastTotalContextTokens(0)
	a.resetContextReductionStats()
	if a.usageTracker != nil {
		a.usageTracker.RestoreStats(analytics.SessionStats{})
	}
	if a.lspSessionResetFn != nil {
		a.lspSessionResetFn()
	}
	a.todoMu.Lock()
	a.todoItems = nil
	a.todoMu.Unlock()
	a.skillsMu.Lock()
	a.invokedSkills = make(map[string]*skill.Meta)
	a.skillsMu.Unlock()
	a.setTaskRecords(nil)
	a.gitStatusInjected.Store(false)
	a.explicitUserTurnCount.Store(0)
	a.subs.resetStateEnteredTurns()
}

func (a *MainAgent) installSessionTarget(sessionDir string) {
	a.sessionEpoch++
	a.resetThinkingTranslationSeen()
	a.sessionDir = sessionDir
	if a.fileBackups != nil {
		a.fileBackups.SetSessionDir(sessionDir)
	}
	a.recovery = recovery.NewRecoveryManager(sessionDir)
	a.usageLedger = analytics.NewUsageLedger(sessionDir, a.projectRoot)
	a.setTaskRecords(nil)
	a.setSessionSummary(buildSessionSummaryForDir(sessionDir, a.sessionLock != nil))
	a.resetSessionBuildState()
	if a.sessionTargetChangedFn != nil {
		a.sessionTargetChangedFn(sessionDir)
	}
}

func (a *MainAgent) createRuntimeSessionDir() (string, error) {
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		return "", err
	}
	return recovery.CreateNewSessionDir(sessionsDir)
}

// ForkSession queues a fork of the current session at msgIndex. The fork
// creates a new session seeded with messages[:msgIndex] as history; the
// message at msgIndex is returned via ForkSessionEvent so the TUI can load
// it into the composer.
func (a *MainAgent) ForkSession(msgIndex int) {
	a.sendEvent(Event{
		Type: EventSessionControl,
		Payload: &sessionControlPayload{
			Kind:     sessionControlFork,
			MsgIndex: msgIndex,
		},
	})
}

func (a *MainAgent) editTailUserMessageInPlace(prefix []message.Message, forkMsg message.Message) error {
	if a.recovery != nil {
		if err := a.recovery.RewriteLog("main", prefix); err != nil {
			return fmt.Errorf("rewrite current session log: %w", err)
		}
	}

	a.ctxMgr.RestoreMessages(prefix)
	a.fileTrack = filelock.NewFileTracker()
	a.restoreMainTrackedFileState(prefix)
	a.resetRuntimeEvidenceFromMessages(prefix)
	if a.lspSessionLoadFn != nil {
		a.lspSessionLoadFn(prefix)
	}

	firstUser := ""
	for _, msg := range prefix {
		if msg.Role != "user" {
			continue
		}
		firstUser = message.UserPromptPlainText(msg)
		if strings.TrimSpace(firstUser) != "" {
			break
		}
	}
	if a.usageLedger != nil {
		if err := a.usageLedger.RewriteFirstUserMessage(firstUser); err != nil {
			return fmt.Errorf("rewrite usage summary first user message: %w", err)
		}
	}
	a.updateSessionSummary(func(summary *SessionSummary) {
		if summary == nil {
			return
		}
		summary.FirstUserMessage = strings.TrimSpace(firstUser)
		summary.FirstUserMessageIsCompactionSummary = false
		if strings.TrimSpace(firstUser) == "" {
			summary.OriginalFirstUserMessage = ""
			return
		}
		if summary.OriginalFirstUserMessage == "" || summary.OriginalFirstUserMessage == summary.FirstUserMessage {
			summary.OriginalFirstUserMessage = strings.TrimSpace(firstUser)
		}
	})

	todos := rebuildTodosFromMessages(prefix)
	a.todoMu.Lock()
	a.todoItems = append([]tools.TodoItem(nil), todos...)
	a.todoMu.Unlock()

	a.emitToTUI(SessionRestoredEvent{})
	a.emitToTUI(ForkSessionEvent{Parts: forkMsgParts(forkMsg)})
	a.emitToTUI(ToastEvent{
		Message: "Removed the tail user message from the current session and loaded it into the composer",
		Level:   "info",
	})
	return nil
}

// handleForkSessionCommand creates a new session seeded with messages before
// msgIndex, emits SessionRestoredEvent + ForkSessionEvent so the TUI can
// load the forked message into the composer.
func (a *MainAgent) handleForkSessionCommand(msgIndex int) {
	defer a.finishSessionSwitch()
	msgs := a.ctxMgr.Snapshot()
	if msgIndex < 0 || msgIndex >= len(msgs) {
		log.Warnf("handleForkSessionCommand: msgIndex out of range msgIndex=%v len=%v", msgIndex, len(msgs))
		a.setIdleAndDrainPending()
		return
	}

	// The message at msgIndex becomes the editable draft; prefix is messages before it.
	prefix := append([]message.Message(nil), msgs[:msgIndex]...)
	forkMsg := msgs[msgIndex]
	forkedFrom := filepath.Base(a.sessionDir)
	if forkMsg.Role != "user" {
		log.Warnf("handleForkSessionCommand: msgIndex does not point to a user message msgIndex=%v role=%v", msgIndex, forkMsg.Role)
		a.setIdleAndDrainPending()
		return
	}

	if msgIndex == len(msgs)-1 {
		if err := a.editTailUserMessageInPlace(prefix, forkMsg); err != nil {
			a.emitToTUI(ErrorEvent{Err: fmt.Errorf("edit tail user message: %w", err)})
			a.setIdleAndDrainPending()
			return
		}
		a.setIdleForComposerEdit()
		return
	}

	a.emitToTUI(SessionSwitchStartedEvent{Kind: "fork"})

	newSessionDir, err := a.createRuntimeSessionDir()
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("fork session: create dir: %w", err)})
		a.setIdleAndDrainPending()
		return
	}

	newLock, err := recovery.AcquireSessionLock(newSessionDir)
	if err != nil {
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("fork session: acquire lock: %w", err)})
		a.setIdleAndDrainPending()
		return
	}

	// Seed the target session before touching the current one so failures keep
	// the current session intact. Fork must follow the same acquire-before-
	// activate rule as /resume and /new: if target preparation fails, we release
	// the new lock and leave the old session/lock untouched.
	seedRecovery := recovery.NewRecoveryManager(newSessionDir)
	seededMessages := 0
	for _, msg := range prefix {
		seedMsg := cloneMessageForForkSeed(msg)
		if err := seedRecovery.PersistMessage("main", seedMsg); err != nil {
			seedRecovery.Close()
			_ = newLock.Release()
			a.emitToTUI(ErrorEvent{Err: fmt.Errorf("fork session: seed prefix: %w", err)})
			a.setIdleAndDrainPending()
			return
		}
		seededMessages++
	}
	if err := recovery.SaveSessionMeta(newSessionDir, recovery.SessionMeta{ForkedFrom: forkedFrom}); err != nil {
		seedRecovery.Close()
		_ = newLock.Release()
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("fork session: save metadata: %w", err)})
		a.setIdleAndDrainPending()
		return
	}
	seedRecovery.Close()

	oldRecovery, _ := a.prepareSessionSwitch()
	oldLock := a.sessionLock
	a.freezeCurrentSession(oldRecovery)
	if oldLock != nil {
		if releaseErr := oldLock.Release(); releaseErr != nil {
			log.Warnf("fork session: failed to release old session lock error=%v", releaseErr)
		}
	}
	a.sessionLock = newLock
	a.resetSessionRuntimeState()
	a.installSessionTarget(newSessionDir)
	a.llmClient.SetSessionID(filepath.Base(newSessionDir))

	a.ctxMgr.RestoreMessages(prefix)
	a.restoreMainTrackedFileState(prefix)
	a.resetRuntimeEvidenceFromMessages(prefix)
	todos := rebuildTodosFromMessages(prefix)
	a.todoMu.Lock()
	a.todoItems = append([]tools.TodoItem(nil), todos...)
	a.todoMu.Unlock()
	if a.lspSessionLoadFn != nil {
		a.lspSessionLoadFn(prefix)
	}

	a.emitToTUI(SessionRestoredEvent{})
	a.emitToTUI(ForkSessionEvent{Parts: forkMsgParts(forkMsg)})
	a.emitToTUI(ToastEvent{
		Message: fmt.Sprintf("Forked session %s from %s with %d prior messages; draft loaded into composer", filepath.Base(newSessionDir), forkedFrom, seededMessages),
		Level:   "info",
	})
	a.setIdleForComposerEdit()
}

func forkMsgParts(msg message.Message) []message.ContentPart {
	if len(msg.Parts) > 0 {
		copied := make([]message.ContentPart, len(msg.Parts))
		copy(copied, msg.Parts)
		return copied
	}
	if msg.Content != "" {
		return []message.ContentPart{{Type: "text", Text: msg.Content}}
	}
	return nil
}
