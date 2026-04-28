package agent

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

type toolPermissionDecision struct {
	Action              permission.Action
	MatchArgument       string
	NeedsApprovalPaths  []string
	AlreadyAllowedPaths []string
}

type permissionAggregateItem struct {
	Argument  string
	Action    permission.Action
	AskList   []string
	AllowList []string
}

func evaluateToolPermission(ruleset permission.Ruleset, toolName string, args json.RawMessage) toolPermissionDecision {
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}
	if strings.TrimSpace(toolName) == "" {
		return decision
	}
	if toolName == "Cancel" && ruleset.IsDisabled("Delegate") {
		return decision
	}

	unwrapped := llm.UnwrapToolArgs(args)
	switch toolName {
	case "Delete":
		return evaluateDeleteToolPermission(ruleset, unwrapped)
	case "Bash":
		return evaluateBashToolPermission(ruleset, unwrapped)
	default:
		arg := extractToolArgument(toolName, unwrapped)
		decision.Action = ruleset.Evaluate(toolName, arg)
		decision.MatchArgument = arg
		return decision
	}
}

func evaluateDeleteToolPermission(ruleset permission.Ruleset, args json.RawMessage) toolPermissionDecision {
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}
	req, err := tools.DecodeDeleteRequest(args)
	if err != nil {
		arg := extractToolArgument("Delete", args)
		decision.Action = ruleset.Evaluate("Delete", arg)
		decision.MatchArgument = arg
		return decision
	}

	items := make([]permissionAggregateItem, 0, len(req.Paths))
	for _, path := range req.Paths {
		action := ruleset.Evaluate("Delete", path)
		item := permissionAggregateItem{
			Argument: path,
			Action:   action,
		}
		switch action {
		case permission.ActionAsk:
			item.AskList = []string{path}
		case permission.ActionAllow:
			item.AllowList = []string{path}
		}
		items = append(items, item)
	}
	return aggregatePermissionItems(items, permission.ActionAllow, "*")
}

func aggregatePermissionItems(items []permissionAggregateItem, initial permission.Action, fallbackMatch string) toolPermissionDecision {
	decision := toolPermissionDecision{
		Action:        initial,
		MatchArgument: fallbackMatch,
	}
	for _, item := range items {
		switch item.Action {
		case permission.ActionDeny:
			decision.Action = permission.ActionDeny
			decision.MatchArgument = item.Argument
			decision.NeedsApprovalPaths = nil
			decision.AlreadyAllowedPaths = nil
			return decision
		case permission.ActionAsk:
			decision.Action = permission.ActionAsk
			decision.MatchArgument = item.Argument
			decision.NeedsApprovalPaths = append(decision.NeedsApprovalPaths, item.AskList...)
		case permission.ActionAllow:
			decision.AlreadyAllowedPaths = append(decision.AlreadyAllowedPaths, item.AllowList...)
		}
	}
	if len(items) > 0 && decision.MatchArgument == fallbackMatch {
		decision.MatchArgument = items[0].Argument
	}
	return decision
}
