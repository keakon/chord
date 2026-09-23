package tui

import (
	"time"

	"github.com/keakon/bubbles/v2/textarea"
	tea "github.com/keakon/bubbletea/v2"

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
	Patterns []string
	Scope    permission.RuleScope
}

// ConfirmRequest is sent from the agent to the TUI when a tool has "ask"
// permission and needs explicit user approval before execution.
// RequestID is set when the request comes from a remote transport
// (ConfirmRequestEvent); the TUI then calls ResolveConfirm with this ID when
// the user responds.
type ConfirmRequest struct {
	ToolName            string
	ArgsJSON            string
	DoneReport          string
	RequestID           string
	Timeout             time.Duration
	NeedsApproval       []string
	AlreadyAllowed      []string
	NeedsApprovalRules  []string
	AlreadyAllowedRules []string
	ForceDenyReason     bool
	// AgentID is the asking agent's instance id ("main" for the main agent),
	// carried through from ConfirmRequestEvent so the TUI can switch focus to
	// the agent whose tool call needs a decision.
	AgentID string
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
	pickingRule        bool               // true while in rule picker sub-mode
	candidates         []PatternCandidate // suggested patterns
	patternIdx         int                // selected pattern index
	selectedPatterns   map[int]struct{}
	scopeIdx           int // selected scope index (0=session, 1=project, 2=user-global)
	scopes             []permission.RuleScope
	editingRulePattern bool
	rulePatternInput   textarea.Model

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
	return tickCmd(time.Second, func(_ time.Time) tea.Msg {
		return confirmTimeoutTickMsg{}
	})
}
