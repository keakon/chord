package agent

import (
	"errors"
	"fmt"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

const askPolicyDenyReason = "this is not user feedback. Continue with an approach that does not require approval, or defer this action and report it in your final summary."

var errToolAskPolicyDenied = errors.New("auto-denied by the unattended ask policy")

func wrapToolAskPolicyDenied(toolName string) error {
	return fmt.Errorf("tool %q %w (confirm_ask_policy=%s): %s", toolName, errToolAskPolicyDenied, config.ConfirmAskPolicyDeny, askPolicyDenyReason)
}

// Completion and compaction keep their own confirmation semantics under both
// YOLO and unattended ask handling.
func toolAskPolicyApplies(toolName string) bool {
	switch tools.NormalizeName(toolName) {
	case tools.NameDone, tools.NameCompactContext:
		return false
	default:
		return true
	}
}

func (a *MainAgent) denyToolAsks() bool {
	if a.projectConfig != nil && a.projectConfig.ConfirmAskPolicy != nil {
		return a.projectConfig.EffectiveConfirmAskPolicy() == config.ConfirmAskPolicyDeny
	}
	return a.globalConfig.EffectiveConfirmAskPolicy() == config.ConfirmAskPolicyDeny
}

// ToolAskPolicyDeniedEvent reports a completed permission decision. It never
// opens a confirmation and carries no request ID that a client could answer.
type ToolAskPolicyDeniedEvent struct {
	CallID   string `json:"call_id"`
	ToolName string `json:"tool_name"`
	AgentID  string `json:"agent_id"`
	Policy   string `json:"policy"`
	Reason   string `json:"reason"`
}

func (ToolAskPolicyDeniedEvent) agentEvent() {}
