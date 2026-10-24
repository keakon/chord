package agent

import (
	"crypto/rand"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

// queuePausedFollowup runs on the main owner loop. Its new input joins the
// durable checkpoint before admission resumes; ordinary delivery would append
// the same input again after the task has restored it from that checkpoint.
func (a *MainAgent) queuePausedFollowup(payload *SubAgentSendMessagePayload) {
	taskID := strings.TrimSpace(payload.TaskID)
	text := strings.TrimSpace(payload.Message)
	if text == "" && len(payload.Parts) == 0 {
		a.respondPausedFollowup(payload, tools.TaskHandle{}, fmt.Errorf("message is required"))
		return
	}
	var record *DurableTaskRecord
	var err error
	if payload.Reply == nil && payload.CallerAgentID == "" && payload.CallerTaskID == "" {
		// Focused TUI input is an explicit user action, including input to a
		// nested task. Agent-originated notify still requires direct ownership.
		record = a.taskRecordByTaskID(taskID)
		if record == nil {
			err = fmt.Errorf("unknown task_id %q", taskID)
		}
	} else {
		record, err = a.canCallerControlTask(payload.CallerAgentID, payload.CallerTaskID, taskID)
	}
	if err != nil {
		a.respondPausedFollowup(payload, tools.TaskHandle{}, err)
		return
	}
	sub := a.subAgentByTaskID(taskID)
	if (sub == nil || taskAlreadySettled(record, sub)) && !record.allowsRehydrate(taskResumeByTargetedNotify) {
		a.respondPausedFollowup(payload, tools.TaskHandle{}, fmt.Errorf("task %s cannot accept a follow-up in state %s", taskID, record.State))
		return
	}
	if record.ExternalSession != nil {
		// External workers resume through their own durable input path, which
		// requires an active session. Persisting a paused follow-up here would
		// land it in the internal checkpoint and never reach the worker.
		a.respondPausedFollowup(payload, tools.TaskHandle{}, fmt.Errorf("external task %s cannot take a follow-up while the session is paused; continue the session first, then send the message", taskID))
		return
	}
	if a.recoveryManager() == nil {
		a.respondPausedFollowup(payload, tools.TaskHandle{}, fmt.Errorf("paused session recovery writer is unavailable"))
		return
	}
	a.pause.inputMu.Lock()
	if a.pause.inputClosed {
		a.pause.inputMu.Unlock()
		a.respondPausedFollowup(payload, tools.TaskHandle{}, &executiongate.ErrSessionPaused{})
		return
	}
	if a.pause.inputPrefix == "" {
		a.pause.inputPrefix = rand.Text()
	}
	order := a.acceptedRawMessages.Add(1)
	inputID := fmt.Sprintf("%s:%d", a.pause.inputPrefix, order)
	a.pause.inputMu.Unlock()
	sequence := int64(1)
	for _, input := range a.pause.parkedInputs[taskID] {
		sequence = max(sequence, input.AcceptedOrder+1)
	}
	input := pendingUserMessage{
		InputID: inputID, AcceptedOrder: sequence, Revision: 1,
		Content:  normalizeSubAgentMessage(payload.Kind, text),
		FromUser: payload.CallerAgentID == "", Stage: recovery.PendingInputStageQueued,
		Mailbox: &message.MailboxMetadata{AgentID: controlPlaneAgentID(payload.CallerAgentID), TaskID: payload.CallerTaskID, Kind: payload.Kind},
	}
	if len(payload.Parts) > 0 {
		input.Content = text
		input.Parts = cloneContentParts(payload.Parts)
	}
	if a.pause.parkedInputs == nil {
		a.pause.parkedInputs = make(map[string][]pendingUserMessage)
	}
	a.pause.parkedInputs[taskID] = append(a.pause.parkedInputs[taskID], input)
	a.pause.resumeFollowups = append(a.pause.resumeFollowups, payload)
	a.resumePausedSessionForUser()
}

// finishPausedFollowups starts only explicitly addressed tasks. Inputs for
// other owners remain parked until those owners receive their own request.
func (a *MainAgent) finishPausedFollowups(saveErr error) {
	followups := a.pause.resumeFollowups
	a.pause.resumeFollowups = nil
	type taskResult struct {
		handle tools.TaskHandle
		err    error
	}
	results := make(map[string]taskResult)
	for _, payload := range followups {
		if saveErr != nil {
			a.respondPausedFollowup(payload, tools.TaskHandle{}, fmt.Errorf("save paused follow-up: %w", saveErr))
			continue
		}
		taskID := strings.TrimSpace(payload.TaskID)
		result, exists := results[taskID]
		if !exists {
			result.handle, result.err = a.resumePausedFollowupTask(taskID, payload.Message)
			results[taskID] = result
		}
		a.respondPausedFollowup(payload, result.handle, result.err)
	}
}

func (a *MainAgent) resumePausedFollowupTask(taskID, summary string) (tools.TaskHandle, error) {
	record := a.taskRecordByTaskID(taskID)
	if record == nil {
		return tools.TaskHandle{}, fmt.Errorf("unknown task_id %q", taskID)
	}
	sub := a.subAgentByTaskID(taskID)
	rehydrated := false
	previous := ""
	var err error
	if sub == nil {
		sub, previous, rehydrated, err = a.getOrRehydrateTask(record)
	} else {
		err = a.beginNextTaskAttemptForLiveSub(sub)
		if err == nil {
			var inputs []pendingUserMessage
			for _, input := range a.PendingSessionInputs() {
				if input.OwnerTaskID == taskID {
					inputs = append(inputs, restorePauseInput(input))
				}
			}
			sub.RestorePauseInputs(inputs)
		}
	}
	if err != nil {
		return tools.TaskHandle{}, err
	}
	if err := a.acquireSubAgentSlot(sub); err != nil {
		return tools.TaskHandle{}, err
	}
	a.markSubAgentReactivated(sub, summary)
	sub.ContinueFromContext()
	a.persistSubAgentMeta(sub)
	a.syncTaskRecordFromSub(sub, "")
	return tools.TaskHandle{Status: "resumed", TaskID: taskID, AgentID: sub.instanceID, PreviousAgentID: previous, Rehydrated: rehydrated, Message: "paused inputs restored and worker resumed"}, nil
}

func (a *MainAgent) respondPausedFollowup(payload *SubAgentSendMessagePayload, handle tools.TaskHandle, err error) {
	if payload.Reply == nil && err != nil {
		a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn"})
	}
	a.emitExternalInputDelivery(payload, handle, err)
	respondSubAgentControl(payload.Reply, handle, err)
}

// emitExternalInputDelivery retires the TUI's optimistic draft for a follow-up
// that will not run right away. Only focused input (no reply channel) tracks a
// draft, and only external tasks need it: an internal worker reports its own
// phases, while a queued external message has no process to report from.
func (a *MainAgent) emitExternalInputDelivery(payload *SubAgentSendMessagePayload, handle tools.TaskHandle, err error) {
	if payload == nil || payload.Reply != nil {
		return
	}
	record := a.taskRecordByTaskID(strings.TrimSpace(payload.TaskID))
	if record == nil || record.ExternalSession == nil {
		return
	}
	status := ""
	switch {
	case err != nil:
		status = SubAgentInputDeliveryFailed
	case handle.Status == externalInputQueued:
		status = SubAgentInputDeliveryQueued
	default:
		return
	}
	agentID := strings.TrimSpace(handle.AgentID)
	if agentID == "" {
		agentID = record.LatestInstanceID
	}
	a.emitToTUI(SubAgentInputDeliveryEvent{AgentID: agentID, Status: status})
}
