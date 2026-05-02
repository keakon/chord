package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission"
)

// ConfirmAction represents the user's decision on a tool permission check.
type ConfirmAction int

const (
	ConfirmAllow ConfirmAction = iota // approve this invocation
	ConfirmDeny                       // deny this invocation
)

// ConfirmRuleIntent captures the user's intent to add a permission rule
// alongside the approval decision.
type ConfirmRuleIntent struct {
	Pattern string
	Scope   permission.RuleScope
}

// ConfirmRequest is sent from the agent to the TUI when a tool has "ask"
// permission and needs explicit user approval before execution.
// RequestID is set when the request comes from a remote transport
// (ConfirmRequestEvent); the TUI then calls ResolveConfirm with this ID when
// the user responds.
type ConfirmRequest struct {
	ToolName       string
	ArgsJSON       string
	RequestID      string
	Timeout        time.Duration
	NeedsApproval  []string
	AlreadyAllowed []string
}

// ConfirmResult is the user's response to a ConfirmRequest.
type ConfirmResult struct {
	Action        ConfirmAction
	FinalArgsJSON string
	EditSummary   string
	DenyReason    string
	RuleIntent    *ConfirmRuleIntent // nil = no new rule
}

// confirmActionToStr maps ConfirmAction to the protocol string for ResolveConfirm.
func confirmActionToStr(a ConfirmAction) string {
	switch a {
	case ConfirmAllow:
		return "allow"
	case ConfirmDeny:
		return "deny"
	default:
		return "deny"
	}
}

// confirmRequestMsg wraps a ConfirmRequest for the Bubble Tea message loop.
type confirmRequestMsg struct {
	request ConfirmRequest
}

// confirmTimeoutTickMsg is emitted every second while a confirmation dialog is
// active and a timeout is configured.
type confirmTimeoutTickMsg struct{}

// confirmState holds the transient state for the active confirmation dialog.
type confirmState struct {
	request   *ConfirmRequest // pending request (nil when inactive)
	requestID string          // non-empty when from remote (ResolveConfirm)
	editing   bool            // true while the user is editing args
	editInput textarea.Model  // textarea used in edit sub-mode
	editError string          // inline validation error shown in edit sub-mode
	prevMode  Mode            // mode to restore when the dialog closes
	deadline  time.Time       // zero value = no timeout

	// Rule picker state
	pickingRule bool               // true while in rule picker sub-mode
	candidates  []PatternCandidate // suggested patterns
	patternIdx  int                // selected pattern index
	scopeIdx    int                // selected scope index (0=session, 1=project, 2=user-global)
	scopes      []permission.RuleScope

	// Deny with reason state
	denyingWithReason bool           // true while in deny-reason sub-mode
	denyReasonInput   textarea.Model // textarea used in deny-reason sub-mode

	renderCacheWidth  int
	renderCacheHeight int
	renderCacheTheme  string
	renderCacheReq    *ConfirmRequest
	renderCacheText   string
}

// waitForConfirmRequest returns a tea.Cmd that blocks until a ConfirmRequest
// arrives on ch, then delivers it as a confirmRequestMsg.
func waitForConfirmRequest(ch <-chan ConfirmRequest) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return confirmRequestMsg{request: req}
	}
}

func confirmTimeoutTick() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg {
		return confirmTimeoutTickMsg{}
	})
}

// MakeConfirmFunc creates a callback suitable for use as the agent's tool
// permission confirmation handler. It sends a ConfirmRequest to reqCh and
// blocks until a ConfirmResult is available on resCh or the provided context
// is cancelled.
//
// Context cancellation (e.g. turn cancelled by a new user message) returns
// ConfirmDeny immediately, unblocking the tool goroutine without waiting for
// user input.
//
// If timeout > 0 the call returns ConfirmDeny automatically after that
// duration.

func MakeConfirmFunc(reqCh chan<- ConfirmRequest, resCh <-chan ConfirmResult, timeout time.Duration) func(ctx context.Context, toolName, argsJSON string, needsApproval, alreadyAllowed []string) (agent.ConfirmResponse, error) {
	return func(ctx context.Context, toolName, argsJSON string, needsApproval, alreadyAllowed []string) (agent.ConfirmResponse, error) {
		request := ConfirmRequest{
			ToolName:       toolName,
			ArgsJSON:       argsJSON,
			NeedsApproval:  append([]string(nil), needsApproval...),
			AlreadyAllowed: append([]string(nil), alreadyAllowed...),
		}
		// Send the request, but bail out if the context is already cancelled.
		select {
		case reqCh <- request:
		case <-ctx.Done():
			return agent.ConfirmResponse{Approved: false}, nil
		}

		// Wait for the user's response, with optional timeout and context
		// cancellation.
		if timeout <= 0 {
			select {
			case result := <-resCh:
				return confirmResponseFromResult(result, argsJSON), nil
			case <-ctx.Done():
				return agent.ConfirmResponse{Approved: false}, nil
			}
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case result := <-resCh:
			return confirmResponseFromResult(result, argsJSON), nil
		case <-timer.C:
			return agent.ConfirmResponse{Approved: false}, nil
		case <-ctx.Done():
			return agent.ConfirmResponse{Approved: false}, nil
		}
	}
}

func confirmResponseFromResult(result ConfirmResult, fallbackArgsJSON string) agent.ConfirmResponse {
	finalArgs := result.FinalArgsJSON
	if strings.TrimSpace(finalArgs) == "" {
		finalArgs = fallbackArgsJSON
	}
	var ruleIntent *agent.ConfirmRuleIntent
	if result.RuleIntent != nil {
		ruleIntent = &agent.ConfirmRuleIntent{
			Pattern: result.RuleIntent.Pattern,
			Scope:   int(result.RuleIntent.Scope),
		}
	}
	return agent.ConfirmResponse{
		Approved:      result.Action == ConfirmAllow,
		FinalArgsJSON: finalArgs,
		EditSummary:   result.EditSummary,
		DenyReason:    result.DenyReason,
		RuleIntent:    ruleIntent,
	}
}
