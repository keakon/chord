package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/tools"
)

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

	requestID := makeRequestID()
	ch := a.interaction.registerConfirm(requestID)
	defer a.interaction.unregisterConfirm(requestID)

	a.fireHookBackground(ctx, hook.OnWaitConfirm, a.currentTurnID(), map[string]any{
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
	}); err != nil {
		return ConfirmResponse{}, err
	}

	return a.interaction.awaitConfirm(ctx, ch, timeout, toolName)
}

// AskQuestions emits question request events one at a time, waits for each
// answer, and returns the collected responses in tool-compatible form.
func (a *MainAgent) AskQuestions(ctx context.Context, questions []tools.QuestionItem, timeout time.Duration) ([]tools.QuestionAnswer, error) {
	a.interaction.beginQuestionFlow()
	defer a.interaction.endQuestionFlow()

	a.toolWg.Add(1)
	defer a.toolWg.Done()

	answers := make([]tools.QuestionAnswer, 0, len(questions))
	for _, q := range questions {
		requestID := makeRequestID()
		ch := a.interaction.registerQuestion(requestID)

		options := make([]string, len(q.Options))
		optionDetails := make([]string, len(q.Options))
		for i, opt := range q.Options {
			options[i] = opt.Label
			optionDetails[i] = opt.Description
		}

		defaultAnswer := ""
		if len(options) > 0 {
			defaultAnswer = options[0]
		}

		a.fireHookBackground(ctx, hook.OnWaitQuestion, a.currentTurnID(), map[string]any{
			hook.DataKeyToolName: tools.NameQuestion,
			"header":             q.Header,
			"question":           q.Question,
			"options":            append([]string(nil), options...),
			"default_answer":     defaultAnswer,
			"multiple":           q.Multiple,
			"timeout_ms":         timeout.Milliseconds(),
		})

		if err := a.emitInteractiveToTUI(ctx, QuestionRequestEvent{
			ToolName:      tools.NameQuestion,
			Header:        q.Header,
			Question:      q.Question,
			Options:       options,
			OptionDetails: optionDetails,
			DefaultAnswer: defaultAnswer,
			Multiple:      q.Multiple,
			RequestID:     requestID,
			Timeout:       timeout,
		}); err != nil {
			a.interaction.unregisterQuestion(requestID)
			return nil, err
		}

		resp, err := a.interaction.awaitQuestion(ctx, ch, timeout)
		a.interaction.unregisterQuestion(requestID)
		if err != nil {
			return nil, err
		}
		if resp.Cancelled {
			return nil, fmt.Errorf("question cancelled by user")
		}

		answers = append(answers, tools.QuestionAnswer{
			Header:   q.Header,
			Selected: append([]string(nil), resp.Answers...),
		})
	}

	return answers, nil
}
