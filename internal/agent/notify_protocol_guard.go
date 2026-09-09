package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

// notifyProtocolPauseThreshold is the number of consecutive notify
// response-protocol failures (for one target task) after which the turn stops
// auto-retrying and asks the user for explicit correction. The first failure
// carries the tool's own rejection; the second appends the reinforced
// guidance below; the third pauses.
const notifyProtocolPauseThreshold = 3

// notifyResponseProtocolErrorTexts are the fixed phrases the notify tool and
// the agent-request ledger use for the "no valid response correlation" error
// family: a response that names no correlation, a correlation with no
// matching pending request, a request the caller does not own, or a
// stale/terminal/mismatched request. The stop-loss counts only errors that
// match one of these phrases (and only for message_type=response calls), so
// IO, network, session-change, delivery, and persistence failures never feed
// the counter.
var notifyResponseProtocolErrorTexts = []string{
	"correlation_id is required for response",
	"unknown pending request",
	"not owned by caller",
	"already has a different durable response",
	"no longer current",
	"source task is terminal",
	"cannot be rehydrated",
	" responded",
}

// notifyResponseProtocolGuidance is the corrective note appended to the
// model-visible result on the second consecutive response-protocol failure. It
// is deliberately concrete: a response answers exactly one real pending
// request with that request's own correlation_id, a plain note omits the
// response keys, and guessing an id is never a recovery.
const notifyResponseProtocolGuidance = "Notify correction (repeated failure): notify with message_type=response answers exactly one real pending request, using the correlation_id that request carries - never an invented, guessed, or reused one. A plain note to the target omits message_type and correlation_id entirely (keep target_task_id, message, and optionally kind). If you genuinely need to answer but cannot find the real correlation_id, stop and ask for coordination instead of retrying another guessed id."

// notifyResponseProtocolPauseNote is appended on the third failure, which
// pauses the automatic retry; the turn no longer issues a follow-up request on
// its own.
const notifyResponseProtocolPauseNote = "Notify response-protocol failures have repeated; automatic retry is paused and the user was asked to correct the intended notify. Do not call notify with message_type=response again until the user supplies the genuine correlation_id of the pending request to answer, or confirms a plain message."

// notifyProtocolPauseUserMessage explains to the user why the turn stopped
// auto-retrying.
func notifyProtocolPauseUserMessage(taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "Notify paused: 3 consecutive notify response-protocol errors (missing, unknown, or mismatched correlation_id). Automatic retry is stopped so no further guessed ids are tried; tell the agent what it should send: a plain note (target_task_id + message) or the genuine correlation_id of the pending request it must answer."
	}
	return fmt.Sprintf("Notify paused: 3 consecutive notify response-protocol errors for task %s (missing, unknown, or mismatched correlation_id). Automatic retry is stopped so no further guessed ids are tried; tell the agent what it should send to that task: a plain note (target_task_id + message) or the genuine correlation_id of the pending request it must answer.", taskID)
}

// notifyCallMessageType extracts the message_type of a notify call so the
// stop-loss only counts response-protocol attempts. A missing or unparsable
// payload yields "" and is never counted.
func notifyCallMessageType(argsJSON string) string {
	var args struct {
		MessageType string `json:"message_type"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	return strings.TrimSpace(args.MessageType)
}

// notifyCallTargetTaskID extracts the target_task_id a notify call names, used
// as the per-task failure-streak key.
func notifyCallTargetTaskID(argsJSON string) string {
	var args struct {
		TargetTaskID string `json:"target_task_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	return strings.TrimSpace(args.TargetTaskID)
}

// isNotifyResponseProtocolError reports whether err is one of the
// response-correlation protocol rejections (and only for a message_type=
// response call). Changing the correlation id, message, or kind between
// attempts keeps counting because the decision depends on the error family,
// not on argument equality.
func isNotifyResponseProtocolError(argsJSON string, err error) bool {
	if err == nil || notifyCallMessageType(argsJSON) != "response" {
		return false
	}
	message := err.Error()
	for _, phrase := range notifyResponseProtocolErrorTexts {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

// observeNotifyProtocolFailure tracks consecutive notify response-protocol
// failures per target task for the current turn. A success on that target
// clears the streak (a genuine ordinary notify, a valid response, or a new
// request all recover); changing message/kind/correlation keeps counting. It
// returns a guidance note to append to the model-visible result and whether
// the turn should pause its automatic retry. The event loop is the only
// caller, so the turn fields need no locking.
func (a *MainAgent) observeNotifyProtocolFailure(name, argsJSON string, err error) (note string, pause bool) {
	if a == nil || a.turn == nil || tools.NormalizeName(name) != tools.NameNotify {
		return "", false
	}
	target := notifyCallTargetTaskID(argsJSON)
	if err == nil {
		// A successful notify call means the model re-entered the valid
		// protocol: clear this target's streak so a later isolated mistake
		// restarts the count instead of inheriting stale failures.
		if a.turn.notifyProtocolStreak != nil {
			delete(a.turn.notifyProtocolStreak, target)
		}
		return "", false
	}
	if !isNotifyResponseProtocolError(argsJSON, err) {
		return "", false
	}
	if a.turn.notifyProtocolStreak == nil {
		a.turn.notifyProtocolStreak = make(map[string]int)
	}
	streak := a.turn.notifyProtocolStreak[target] + 1
	a.turn.notifyProtocolStreak[target] = streak
	switch {
	case streak < notifyProtocolPauseThreshold-1:
		return "", false
	case streak == notifyProtocolPauseThreshold-1:
		return notifyResponseProtocolGuidance, false
	default:
		// Third and later consecutive failures flag the turn so the batch
		// closeout pauses the automatic retry (see consumeNotifyProtocolPause).
		a.turn.notifyProtocolPause = true
		a.turn.notifyProtocolPauseTask = target
		return notifyResponseProtocolPauseNote, true
	}
}

// consumeNotifyProtocolPause ends the turn for an explicit notify correction
// when the response-protocol stop-loss flagged it, and reports whether the
// pause was consumed. Called at the tool-batch closeout before the next
// automatic LLM round starts.
func (a *MainAgent) consumeNotifyProtocolPause() bool {
	if a == nil || a.turn == nil || !a.turn.notifyProtocolPause {
		return false
	}
	taskID := strings.TrimSpace(a.turn.notifyProtocolPauseTask)
	a.turn.notifyProtocolPause = false
	a.turn.notifyProtocolPauseTask = ""
	a.pauseTurnForNotifyProtocolCorrection(taskID)
	return true
}

// pauseTurnForNotifyProtocolCorrection ends the current turn without starting
// the next automatic LLM round after repeated notify response-protocol
// failures, and surfaces a user-visible request for explicit correction. Loop
// state is irrelevant to this protection: the loop switch gates the done
// interception, not this protocol stop-loss.
func (a *MainAgent) pauseTurnForNotifyProtocolCorrection(taskID string) {
	message := notifyProtocolPauseUserMessage(taskID)
	a.emitToTUI(InfoEvent{Message: message})
	a.emitToTUI(NotificationEvent{
		Reason:  NotificationReasonUserInputRequired,
		Message: "Chord: notify needs your correction",
	})
	a.clearCurrentTurnKeepLoopState()
}
