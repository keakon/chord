package agent

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/externalsupervisor"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) sendMessageToExternalTask(record *DurableTaskRecord, callerAgentID, callerTaskID, text, kind string, trigger taskResumeTrigger) (tools.TaskHandle, error) {
	metadata := &message.MailboxMetadata{AgentID: controlPlaneAgentID(callerAgentID), TaskID: callerTaskID, Kind: kind}
	if w, ok := a.subs.runtimeByTaskID(record.TaskID).(*externalWorker); ok {
		if w.State() == SubAgentStateCancelled {
			return tools.TaskHandle{}, fmt.Errorf("task %s was cancelled; delegate the work again", record.TaskID)
		}
		deferred, err := w.enqueueExternalInput(text, metadata)
		if err != nil {
			return tools.TaskHandle{}, err
		}
		status, detail := "sent", "message queued for the external worker"
		if deferred {
			status, detail = "queued", "message persisted for the next task attempt"
		}
		return tools.TaskHandle{Status: status, TaskID: record.TaskID, AgentID: w.instanceID, Message: detail}, nil
	}
	if !record.allowsRehydrate(trigger) {
		return tools.TaskHandle{}, fmt.Errorf("task %s is %s; this message cannot resume it", record.TaskID, record.State)
	}
	w, err := a.rehydrateExternalTask(record, text, metadata)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	return tools.TaskHandle{Status: "rehydrated", TaskID: record.TaskID, AgentID: w.instanceID,
		PreviousAgentID: record.LatestInstanceID, Rehydrated: true, Message: "message delivered and external task resumed"}, nil
}

func (a *MainAgent) resumeQueuedExternalInputs(taskID string) {
	record := a.taskRecordByTaskID(taskID)
	if record == nil || record.ExternalSession == nil || SubAgentState(record.State) != SubAgentStateCompleted {
		return
	}
	for _, input := range record.ExternalSession.Inputs {
		if input.State == externalInputQueued {
			if _, err := a.rehydrateExternalTask(record, "", nil); err != nil {
				a.emitToTUI(ToastEvent{AgentID: record.LatestInstanceID, Message: err.Error(), Level: "warn"})
			}
			return
		}
	}
}

func (a *MainAgent) rehydrateExternalTask(record *DurableTaskRecord, text string, metadata *message.MailboxMetadata) (worker *externalWorker, err error) {
	finish, err := a.beginSessionStructural(a.parentCtx, false)
	if err != nil {
		return nil, err
	}
	defer finish()
	if a.shuttingDown.Load() || a.admissionPaused.Load() {
		return nil, fmt.Errorf("task reactivation is unavailable during session or lifecycle change")
	}
	taskID, admissionEpoch := record.TaskID, a.admissionEpoch.Load()
	if rt, activation, leader := a.subs.beginTaskActivation(taskID); rt != nil {
		w, ok := rt.(*externalWorker)
		if !ok {
			return nil, fmt.Errorf("task backend changed during activation")
		}
		if text != "" {
			_, err = w.enqueueExternalInput(text, metadata)
		}
		return w, err
	} else if !leader {
		// The event loop must not wait for another activation to finish.
		return nil, fmt.Errorf("task %s activation is already in progress", taskID)
	} else {
		defer func() { a.subs.completeTaskActivation(taskID, activation, worker, record.LatestInstanceID, err) }()
		return a.rehydrateExternalTaskLeader(record, activation, admissionEpoch, text, metadata)
	}
}

// externalSessionReceiptProvesEnded accepts the receipt of exactly the run the
// task last persisted as proof that its process group was torn down. Looking it
// up by run id is what makes the proof unambiguous: a receipt of an earlier
// turn, another task, or a run that is still cleaning up can never match.
func (a *MainAgent) externalSessionReceiptProvesEnded(record *DurableTaskRecord) bool {
	binding := record.ExternalSession
	if binding == nil || !externalsupervisor.ValidRunID(binding.RunID) {
		return false
	}
	dir := a.externalSupervisorReceiptDir()
	if dir == "" {
		return false
	}
	receipt, err := externalsupervisor.LoadReceipt(dir, binding.RunID)
	if err != nil {
		return false
	}
	return receipt.Cleanup.Proven
}

func (a *MainAgent) rehydrateExternalTaskLeader(record *DurableTaskRecord, activation *subAgentActivation, admissionEpoch uint64, text string, metadata *message.MailboxMetadata) (w *externalWorker, err error) {
	binding := record.ExternalSession
	if binding == nil || binding.Backend != config.BackendKindClaudeCode || !externalclaude.ValidSessionID(binding.SessionID) {
		return nil, fmt.Errorf("external task has no valid session binding")
	}
	if !binding.ProcessEnded {
		if !a.externalSessionReceiptProvesEnded(record) {
			return nil, fmt.Errorf("external task process termination is not proven; resume is unsafe")
		}
	}
	for _, input := range binding.Inputs {
		if input.State == message.ToolRecoveryStateOutcomeUnknown {
			return nil, fmt.Errorf("external task input outcome is unknown; inspect it before delegating again")
		}
	}
	def, err := a.resolveAgentDef(record.AgentDefName)
	if err != nil {
		return nil, err
	}
	if !def.IsSubAgent() || !def.IsExternalBackend() ||
		def.Backend.EffectiveCommand() != binding.Command ||
		!slices.Equal(def.Backend.LaunchArgs, binding.LaunchArgs) ||
		!slices.Equal(slices.Sorted(maps.Keys(def.Backend.Env)), binding.EnvKeys) ||
		strings.TrimSpace(def.SystemPrompt+"\n\n"+def.PromptAppend) != binding.Prompt {
		return nil, fmt.Errorf("external task execution configuration changed; delegate a new task")
	}
	if info, statErr := os.Stat(binding.WorkDir); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("external task working directory is unavailable")
	}
	manager := a.recoveryManager()
	if manager == nil {
		return nil, fmt.Errorf("external task requires durable session storage")
	}
	history, err := loadTaskHistoryMessages(manager, record, loadToolActivityStarted(manager))
	if err != nil {
		return nil, fmt.Errorf("load external task history: %w", err)
	}
	ctx, cancel := context.WithCancel(a.parentCtx)
	w, err = a.newExternalWorker(externalWorkerConfig{
		agentDef: def, instanceID: NextInstanceID(def.Name), taskID: record.TaskID, taskDesc: record.TaskDesc,
		planTaskRef: record.PlanTaskRef, semanticTaskKey: record.SemanticTaskKey,
		resultSchemaJSON: record.ResultSchema,
		workDir:          binding.WorkDir, ctx: ctx, cancel: cancel,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	committed := false
	prepared := w
	defer func() {
		if !committed {
			a.releaseRuntimeSlot(prepared)
			prepared.abortStartup()
		}
	}()
	w.sessionID, w.resumeNext, w.processEnded, w.runID = binding.SessionID, true, true, binding.RunID
	w.ownerAgentID, w.ownerTaskID, w.depth, w.joinToOwner = record.OwnerAgentID, record.OwnerTaskID, record.Depth, record.JoinToOwner
	w.workDirState = binding.WorkDirState
	w.ctxMgr.RestoreMessages(history)
	w.inputs = cloneExternalTaskSession(binding).Inputs
	for _, input := range w.inputs {
		if input.State == message.ToolRecoveryStateNotStarted || input.State == externalInputQueued {
			w.promptQueue = append(w.promptQueue, input)
		}
	}
	if text != "" {
		w.inputSeq++
		input := externalTaskInput{ID: fmt.Sprintf("%s:%d", w.instanceID, w.inputSeq), Content: text, Mailbox: metadata, State: message.ToolRecoveryStateNotStarted}
		w.inputs = append(w.inputs, input)
		w.promptQueue = append(w.promptQueue, input)
	}
	if len(w.promptQueue) == 0 {
		return nil, fmt.Errorf("external task has no input to resume")
	}
	w.initialPrompt = w.promptQueue[0].Content
	w.setState(SubAgentStateRunning, "external task resuming")
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	if a.shuttingDown.Load() || a.admissionPaused.Load() || a.admissionEpoch.Load() != admissionEpoch {
		return nil, fmt.Errorf("external task reactivation invalidated by session or lifecycle change")
	}
	current := a.taskRecordByTaskID(record.TaskID)
	if current == nil || current.LatestInstanceID != record.LatestInstanceID || current.Attempt != record.Attempt ||
		SubAgentState(current.State) == SubAgentStateCancelled {
		return nil, fmt.Errorf("external task changed during activation")
	}
	if isTerminalSubAgentState(SubAgentState(current.State)) && !current.SettlementDurable {
		current, err = a.retryTaskSettlementDurability(record.TaskID)
		if err != nil || current == nil || !current.SettlementDurable {
			return nil, fmt.Errorf("external task terminal settlement is not durable")
		}
	}
	if a.governor == nil || !a.governor.tryAcquireRuntime() {
		return nil, fmt.Errorf("max concurrent agents reached; wait for a running agent to complete")
	}
	w.takeSlot()
	registration := w.buildTaskRecord(current, "", a.explicitUserTurnCount.Load(), time.Now())
	if isTerminalSubAgentState(SubAgentState(current.State)) {
		registration.Attempt++
		registration.LatestSettlement, registration.LastCompletion = nil, nil
		registration.SettlementDurable = false
		registration.ClosedReason = ""
	}
	// Pending inputs already have durable transcript entries. Only new input
	// is appended; neither completed nor outcome-unknown turns are replayed.
	if text != "" {
		input := w.inputs[len(w.inputs)-1]
		if err = w.persistExternalMessage(message.Message{Role: message.RoleUser, Content: text, InputID: input.ID, Mailbox: metadata}); err != nil {
			return nil, err
		}
	}
	if err = a.persistTaskRegistryRecord(a.sessionDir, record.TaskID, registration); err != nil {
		return nil, fmt.Errorf("persist external task activation: %w", err)
	}
	a.subs.mu.Lock()
	if a.subs.activations[record.TaskID] != activation || activation.cancelled {
		a.subs.mu.Unlock()
		return nil, fmt.Errorf("external task activation superseded")
	}
	a.publishSubAgentLocked(w, record.TaskID, registration)
	a.subs.mu.Unlock()
	committed = true
	a.migrateSubAgentOwnerIdentity(record.LatestInstanceID, w.instanceID)
	w.startRuntime()
	a.emitToTUI(AgentStartedEvent{AgentID: w.instanceID, PreviousAgentID: record.LatestInstanceID,
		TaskID: w.taskID, AgentType: w.agentDefName, Description: w.taskDesc,
		ParentAgentID: controlPlaneAgentID(w.ownerAgentID), ParentTaskID: w.ownerTaskID})
	return w, nil
}
