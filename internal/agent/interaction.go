package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/tools"
)

// agentIDForInteraction returns the walltime/ownership agent for a confirm or
// question flow. Tool execution contexts carry the instance ID ("main-N", and
// subinstance "worker-1"); the sidebar reads walltime under
// identity.MainAgentID ("main"), so the MainAgent instance id is normalized.
// SubAgent contexts keep their instance id so their waits aggregate under the
// task's instance history. A bare turn context (loop-exit Done approval,
// repeated tool-call interception) also falls back to the main agent.
func (a *MainAgent) agentIDForInteraction(ctx context.Context) string {
	id := strings.TrimSpace(tools.AgentIDFromContext(ctx))
	if id == "" || id == a.instanceID {
		return identity.MainAgentID
	}
	return id
}

func (a *MainAgent) agentNameForInteraction(agentID string) string {
	if agentID == identity.MainAgentID || agentID == a.instanceID {
		return a.currentAgentName()
	}
	if sub := a.subs.subAgent(agentID); sub != nil {
		return sub.agentDefName
	}
	return ""
}

func (a *MainAgent) turnIDForInteraction(ctx context.Context) uint64 {
	if turnID := tools.TurnIDFromContext(ctx); turnID != 0 {
		return turnID
	}
	return a.currentTurnID()
}

// AwaitConfirm emits a confirmation request event, waits for the user's reply,
// and returns the resolved response. Only one confirm flow may be active at a
// time because the TUI supports a single modal dialog.
func (a *MainAgent) AwaitConfirm(ctx context.Context, toolName, argsJSON string, timeout time.Duration, needsApproval []string, alreadyAllowed []string, summary ...string) (ConfirmResponse, error) {
	return a.AwaitConfirmWithRuleContext(ctx, toolName, argsJSON, timeout, needsApproval, alreadyAllowed, nil, nil, summary...)
}

func (a *MainAgent) AwaitForceDenyConfirm(ctx context.Context, toolName, argsJSON string, timeout time.Duration, needsApproval []string, alreadyAllowed []string, summary ...string) (ConfirmResponse, error) {
	return a.awaitConfirm(ctx, toolName, argsJSON, timeout, needsApproval, alreadyAllowed, nil, nil, true, summary...)
}

func (a *MainAgent) AwaitConfirmWithRuleContext(ctx context.Context, toolName, argsJSON string, timeout time.Duration, needsApproval []string, alreadyAllowed []string, needsApprovalRules []string, alreadyAllowedRules []string, summary ...string) (ConfirmResponse, error) {
	return a.awaitConfirm(ctx, toolName, argsJSON, timeout, needsApproval, alreadyAllowed, needsApprovalRules, alreadyAllowedRules, false, summary...)
}

func (a *MainAgent) awaitConfirm(ctx context.Context, toolName, argsJSON string, timeout time.Duration, needsApproval []string, alreadyAllowed []string, needsApprovalRules []string, alreadyAllowedRules []string, forceDenyReason bool, summary ...string) (ConfirmResponse, error) {
	a.interaction.beginConfirmFlow()
	defer a.interaction.endConfirmFlow()

	a.toolWg.Add(1)
	defer a.toolWg.Done()

	ownerID := a.agentIDForInteraction(ctx)
	agentName := a.agentNameForInteraction(ownerID)
	turnID := a.turnIDForInteraction(ctx)
	requestID := makeRequestID()
	ch := a.interaction.registerConfirm(requestID, a.walltime.captureAt(ownerID, agentName, turnID))
	defer a.interaction.unregisterConfirm(requestID)

	a.fireHookBackground(ctx, hook.OnWaitConfirm, turnID, map[string]any{
		hook.DataKeyToolName:    toolName,
		"args_json":             argsJSON,
		"timeout_ms":            timeout.Milliseconds(),
		"needs_approval":        append([]string(nil), needsApproval...),
		"already_allowed":       append([]string(nil), alreadyAllowed...),
		"needs_approval_rules":  append([]string(nil), needsApprovalRules...),
		"already_allowed_rules": append([]string(nil), alreadyAllowedRules...),
	})

	summaryVal := ""
	if len(summary) > 0 {
		summaryVal = summary[0]
	}

	if err := a.emitInteractiveToTUI(ctx, ConfirmRequestEvent{
		ToolName:            toolName,
		ArgsJSON:            argsJSON,
		RequestID:           requestID,
		Timeout:             timeout,
		NeedsApproval:       append([]string(nil), needsApproval...),
		AlreadyAllowed:      append([]string(nil), alreadyAllowed...),
		NeedsApprovalRules:  append([]string(nil), needsApprovalRules...),
		AlreadyAllowedRules: append([]string(nil), alreadyAllowedRules...),
		DoneReport:          summaryVal,
		ForceDenyReason:     forceDenyReason,
		AgentID:             ownerID,
	}); err != nil {
		return ConfirmResponse{}, err
	}
	a.emitToTUI(NotificationEvent{Reason: NotificationReasonUserInputRequired, Message: "Chord: Permission confirmation required"})
	return a.interaction.awaitConfirm(ctx, ch, timeout, toolName)
}

// AskQuestions emits question request events one at a time, waits for each
// answer, and returns the collected responses in tool-compatible form.
//
// Every published request is closed exactly once: a normal decline, timeout,
// or supersede fills the rest of the batch with not_asked and returns success,
// while a system cancellation or shutdown returns an error rather than a
// fabricated batch.
func (a *MainAgent) AskQuestions(ctx context.Context, questions []tools.QuestionItem, timeout time.Duration) ([]tools.QuestionAnswer, error) {
	release, err := a.interaction.acquireQuestionFlow(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	a.toolWg.Add(1)
	defer a.toolWg.Done()

	answers := make([]tools.QuestionAnswer, 0, len(questions))
	ownerID := a.agentIDForInteraction(ctx)
	agentName := a.agentNameForInteraction(ownerID)
	turnID := a.turnIDForInteraction(ctx)
	for i, q := range questions {
		// A cancellation observed while waiting for the batch slot or for a
		// previous answer must not register another request.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case <-a.stoppingCh:
			return nil, ErrAgentShutdown
		default:
		}

		requestID := makeRequestID()
		var deadline time.Time
		if timeout > 0 {
			deadline = time.Now().Add(timeout)
		}
		entry := a.interaction.registerQuestion(requestID, deadline, a.walltime.captureAt(ownerID, agentName, turnID))

		options := make([]string, len(q.Options))
		optionDetails := make([]string, len(q.Options))
		for j, opt := range q.Options {
			options[j] = opt.Label
			optionDetails[j] = opt.Description
		}

		a.fireHookBackground(ctx, hook.OnWaitQuestion, turnID, map[string]any{
			hook.DataKeyToolName: tools.NameQuestion,
			"header":             q.Header,
			"question":           q.Question,
			"options":            append([]string(nil), options...),
			"multiple":           q.Multiple,
			"timeout_ms":         timeout.Milliseconds(),
		})

		// The send shares the request deadline: a request that cannot reach the
		// output channel in time fails as a send timeout and never publishes.
		sendCtx := ctx
		var cancelSend context.CancelFunc
		if !deadline.IsZero() {
			sendCtx, cancelSend = context.WithDeadline(ctx, deadline)
		}
		sendErr := a.emitInteractiveToTUI(sendCtx, QuestionRequestEvent{
			ToolName:      tools.NameQuestion,
			Header:        q.Header,
			Question:      q.Question,
			Options:       options,
			OptionDetails: optionDetails,
			Multiple:      q.Multiple,
			RequestID:     requestID,
			Deadline:      deadline,
			AgentID:       ownerID,
		})
		if cancelSend != nil {
			cancelSend()
		}
		if sendErr != nil {
			a.interaction.abortQuestion(requestID)
			if timeout > 0 && errors.Is(sendErr, context.DeadlineExceeded) && ctx.Err() == nil {
				return nil, fmt.Errorf("question request send timed out after %s", timeout)
			}
			return nil, sendErr
		}
		a.emitToTUI(NotificationEvent{Reason: NotificationReasonUserInputRequired, Message: "Chord: Question requires your input"})

		reason, respAnswers, waitErr := a.interaction.awaitQuestion(ctx, entry, requestID)
		// Close the client-side request before moving on: the resolved event
		// must follow the request and precede the next question. It uses the
		// agent context rather than the answer-wait context so an expired
		// answer wait does not suppress it.
		a.emitQuestionResolved(requestID, reason)
		if waitErr != nil {
			return nil, waitErr
		}
		if reason != tools.QuestionOutcomeAnswered {
			answers = append(answers, tools.QuestionAnswer{
				Header:   q.Header,
				Selected: []string{},
				Outcome:  reason,
			})
			for _, rest := range questions[i+1:] {
				answers = append(answers, tools.QuestionAnswer{
					Header:   rest.Header,
					Selected: []string{},
					Outcome:  tools.QuestionOutcomeNotAsked,
				})
			}
			return answers, nil
		}
		// ResolveQuestion accepts an answered response only with a non-empty
		// selection, so respAnswers is never empty here; the copy keeps the
		// batch from aliasing whatever the broker stored.
		answers = append(answers, tools.QuestionAnswer{
			Header:   q.Header,
			Selected: append([]string{}, respAnswers...),
			Outcome:  tools.QuestionOutcomeAnswered,
		})
	}

	return answers, nil
}

// emitQuestionResolved publishes a QuestionResolvedEvent for a request whose
// terminal state was already decided. Failure to enqueue is not surfaced: the
// terminal state is authoritative in core, and a client that never sees the
// event still clears its pending dialog when the connection closes.
func (a *MainAgent) emitQuestionResolved(requestID, reason string) {
	if requestID == "" || reason == "" {
		return
	}
	ctx := a.parentCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.emitInteractiveToTUI(ctx, QuestionResolvedEvent{RequestID: requestID, Reason: reason}); err != nil {
		log.Debugf("question resolved event not delivered request_id=%v reason=%v error=%v", requestID, reason, err)
	}
}
