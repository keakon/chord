package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

const (
	hostedPauseTurn        = "pause_turn"
	maxHostedContinuations = 4
)

// mergeHostedObservations joins observations of one provider turn by call id.
// Completed results replace the pending invocation emitted before pause_turn.
func mergeHostedObservations(dst, src *message.HostedObservation) *message.HostedObservation {
	if src == nil {
		return dst
	}
	if dst == nil {
		return src
	}
	dst.Calls = message.MergeHostedCalls(dst.Calls, src.Calls)
	if src.Summary != "" {
		dst.Summary = src.Summary
	}
	dst.Items = append(dst.Items, src.Items...)
	if src.Container != "" {
		dst.Container = src.Container
	}
	dst.RequiresApproval = dst.RequiresApproval || src.RequiresApproval
	dst.Usage = src.Usage
	return dst
}

type hostedApprovalRequiredError struct{ tool string }

func (e *hostedApprovalRequiredError) Error() string {
	return fmt.Sprintf("%s requires provider-side approval; use the local MCP integration for interactive approval", e.tool)
}

// A paused provider turn grants no new authorization. Recheck the caller's
// current permission and target capability before sending another declaration.
func (b *hostedBackend) checkContinuation(ctx context.Context, target llm.FallbackModel, spec tools.HostedToolSpec, args map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !hostedTargetCapable(target, spec) {
		return fmt.Errorf("%s continuation stopped: target capability disabled", spec.Name)
	}
	var ruleset permission.Ruleset
	var scope permission.PathScope
	id := tools.AgentIDFromContext(ctx)
	if id != "" && id != identity.MainAgentID && id != b.agent.instanceID {
		sub := b.agent.subAgentByID(id)
		if sub == nil {
			return fmt.Errorf("%s continuation stopped: caller unavailable", spec.Name)
		}
		ruleset, scope = sub.currentRuleset(), sub.effectivePathScope()
	} else {
		ruleset, scope = b.agent.effectiveRuleset(), b.agent.effectivePathScope()
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("marshal continuation permission arguments: %w", err)
	}
	if evaluateToolPermissionInDir(ruleset, spec.Name, raw, scope).Action == permission.ActionDeny {
		return fmt.Errorf("%s continuation stopped: permission revoked", spec.Name)
	}
	return nil
}
