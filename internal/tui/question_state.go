package tui

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

// QuestionRequest is sent from the Question tool to the TUI via a channel.
type QuestionRequest struct {
	Questions  []tools.QuestionItem
	Timeout    time.Duration       // if > 0, auto-cancel after this duration
	ResponseCh chan QuestionResult // in-process only; request-scoped reply channel
}

// QuestionResult is the TUI's response sent back to the blocking tool caller.
type QuestionResult struct {
	Answers []tools.QuestionAnswer
	Err     error
}

// questionRequestMsg wraps a QuestionRequest for the Bubble Tea message loop.
// RequestID is set when the request comes from a remote transport; the TUI
// then calls ResolveQuestion with this ID when the user responds.
type questionRequestMsg struct {
	request   QuestionRequest
	requestID string
}

// questionTimeoutTickMsg is emitted every second while a question dialog is
// active and a timeout is configured. It drives the countdown display.
type questionTimeoutTickMsg struct{}

// questionState holds the transient state for the active question dialog.
type questionState struct {
	request    *QuestionRequest       // full request (nil when inactive)
	requestID  string                 // non-empty when from remote (ResolveQuestion)
	responseCh chan QuestionResult    // in-process only; request-scoped reply channel
	currentQ   int                    // index of the question being answered
	cursor     int                    // highlighted option (0-based)
	selected   map[int]bool           // toggled option indices (multi-select)
	answers    []tools.QuestionAnswer // accumulated answers from previous questions
	custom     bool                   // true when custom text input is focused
	input      textarea.Model         // free-text input for custom answers / text-only Qs
	prevMode   Mode                   // mode to restore on close

	// Timeout state (driven by config confirm_timeout)
	deadline time.Time // zero value = no timeout

	renderCacheWidth    int
	renderCacheTheme    string
	renderCacheReq      *QuestionRequest
	renderCacheCurrentQ int
	renderCacheCursor   int
	renderCacheSelected string
	renderCacheText     string
}

// waitForQuestionRequest returns a tea.Cmd that blocks until a
// QuestionRequest arrives on ch, then delivers it as a questionRequestMsg.
func waitForQuestionRequest(ch <-chan QuestionRequest) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return questionRequestMsg{request: req}
	}
}

// questionTimeoutTick returns a tea.Cmd that sleeps for 1 second then
// delivers a questionTimeoutTickMsg (for countdown display).
func questionTimeoutTick() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg {
		return questionTimeoutTickMsg{}
	})
}

// QuestionCh returns the send-only channel for submitting question requests
// to the TUI. The Question tool writes to this channel.
func (m Model) QuestionCh() chan<- QuestionRequest { return m.questionCh }

// MakeQuestionFunc creates a blocking callback suitable for use as the
// QuestionTool's QuestionFunc. It sends a QuestionRequest to reqCh and blocks
// on a request-scoped response channel until the matching QuestionResult
// arrives.
//
// If timeout > 0 the call returns an error automatically after that duration,
// matching confirm_timeout behaviour.
func MakeQuestionFunc(reqCh chan<- QuestionRequest, timeout time.Duration) tools.QuestionFunc {
	return func(ctx context.Context, questions []tools.QuestionItem) ([]tools.QuestionAnswer, error) {
		responseCh := make(chan QuestionResult, 1)
		// Send the request, but bail out if the context is already cancelled.
		select {
		case reqCh <- QuestionRequest{Questions: questions, Timeout: timeout, ResponseCh: responseCh}:
		case <-ctx.Done():
			return nil, fmt.Errorf("question cancelled: %w", ctx.Err())
		}

		// Wait for the user's response, with optional timeout and context
		// cancellation. Each request uses its own response channel so timed-out
		// or cancelled results cannot pollute later Question calls.
		if timeout <= 0 {
			select {
			case result := <-responseCh:
				return result.Answers, result.Err
			case <-ctx.Done():
				return nil, fmt.Errorf("question cancelled: %w", ctx.Err())
			}
		}

		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case result := <-responseCh:
			return result.Answers, result.Err
		case <-timer.C:
			return nil, fmt.Errorf("question timed out after %s", timeout)
		case <-ctx.Done():
			return nil, fmt.Errorf("question cancelled: %w", ctx.Err())
		}
	}
}
