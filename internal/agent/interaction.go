package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/tools"
)

// AwaitConfirm emits a confirmation request event, waits for the user's reply,
// and returns the resolved response. Only one confirm flow may be active at a
// time because the TUI supports a single modal dialog.
func (a *MainAgent) AwaitConfirm(ctx context.Context, toolName, argsJSON string, timeout time.Duration, needsApproval []string, alreadyAllowed []string) (ConfirmResponse, error) {
	a.confirmFlowMu.Lock()
	defer a.confirmFlowMu.Unlock()

	a.toolWg.Add(1)
	defer a.toolWg.Done()

	requestID := makeRequestID()
	ch := make(chan ConfirmResponse, 1)

	a.fireHookBackground(ctx, hook.OnWaitConfirm, a.currentTurnID(), map[string]any{
		"tool_name":       toolName,
		"args_json":       argsJSON,
		"timeout_ms":      timeout.Milliseconds(),
		"needs_approval":  append([]string(nil), needsApproval...),
		"already_allowed": append([]string(nil), alreadyAllowed...),
	})

	a.confirmMapMu.Lock()
	a.confirmCh[requestID] = ch
	a.confirmMapMu.Unlock()
	defer func() {
		a.confirmMapMu.Lock()
		delete(a.confirmCh, requestID)
		a.confirmMapMu.Unlock()
	}()

	if err := a.emitInteractiveToTUI(ctx, ConfirmRequestEvent{
		ToolName:       toolName,
		ArgsJSON:       argsJSON,
		RequestID:      requestID,
		Timeout:        timeout,
		NeedsApproval:  append([]string(nil), needsApproval...),
		AlreadyAllowed: append([]string(nil), alreadyAllowed...),
	}); err != nil {
		return ConfirmResponse{}, err
	}

	if timeout <= 0 {
		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			return ConfirmResponse{}, ctx.Err()
		case <-a.stoppingCh:
			return ConfirmResponse{}, ErrAgentShutdown
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return resp, nil
	case <-timer.C:
		log.Warnf("tool confirmation timed out, auto-denying tool=%v timeout=%v", toolName, timeout)
		return ConfirmResponse{Approved: false}, nil
	case <-ctx.Done():
		return ConfirmResponse{}, ctx.Err()
	case <-a.stoppingCh:
		return ConfirmResponse{}, ErrAgentShutdown
	}
}

// AskQuestions emits question request events one at a time, waits for each
// answer, and returns the collected responses in tool-compatible form.
func (a *MainAgent) AskQuestions(ctx context.Context, questions []tools.QuestionItem, timeout time.Duration) ([]tools.QuestionAnswer, error) {
	a.questionFlowMu.Lock()
	defer a.questionFlowMu.Unlock()

	a.toolWg.Add(1)
	defer a.toolWg.Done()

	answers := make([]tools.QuestionAnswer, 0, len(questions))
	for _, q := range questions {
		requestID := makeRequestID()
		ch := make(chan QuestionResponse, 1)

		a.questionMapMu.Lock()
		a.questionCh[requestID] = ch
		a.questionMapMu.Unlock()

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
			"tool_name":      "Question",
			"header":         q.Header,
			"question":       q.Question,
			"options":        append([]string(nil), options...),
			"default_answer": defaultAnswer,
			"multiple":       q.Multiple,
			"timeout_ms":     timeout.Milliseconds(),
		})

		if err := a.emitInteractiveToTUI(ctx, QuestionRequestEvent{
			ToolName:      "Question",
			Header:        q.Header,
			Question:      q.Question,
			Options:       options,
			OptionDetails: optionDetails,
			DefaultAnswer: defaultAnswer,
			Multiple:      q.Multiple,
			RequestID:     requestID,
			Timeout:       timeout,
		}); err != nil {
			a.questionMapMu.Lock()
			delete(a.questionCh, requestID)
			a.questionMapMu.Unlock()
			return nil, err
		}

		resp, err := a.awaitQuestionResponse(ctx, ch, timeout)
		a.questionMapMu.Lock()
		delete(a.questionCh, requestID)
		a.questionMapMu.Unlock()
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

func (a *MainAgent) awaitQuestionResponse(ctx context.Context, ch <-chan QuestionResponse, timeout time.Duration) (QuestionResponse, error) {
	if timeout <= 0 {
		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			return QuestionResponse{}, ctx.Err()
		case <-a.stoppingCh:
			return QuestionResponse{}, ErrAgentShutdown
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-ch:
		return resp, nil
	case <-timer.C:
		return QuestionResponse{}, fmt.Errorf("question timed out after %s", timeout)
	case <-ctx.Done():
		return QuestionResponse{}, ctx.Err()
	case <-a.stoppingCh:
		return QuestionResponse{}, ErrAgentShutdown
	}
}

func makeRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		log.Warnf("request ID generation failed, using fallback err=%v", err)
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buf[:])
}
