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
	NeedsApprovalRules  []string
	AlreadyAllowedPaths []string
	AlreadyAllowedRules []string
}

type permissionAggregateItem struct {
	Argument      string
	Action        permission.Action
	AskList       []string
	AskRuleList   []string
	AllowList     []string
	AllowRuleList []string
}

func normalizeToolPermissionAction(toolName string, action permission.Action) permission.Action {
	if toolName == tools.NameQuestion && action == permission.ActionAsk {
		return permission.ActionAllow
	}
	return action
}

func evaluateToolPermission(ruleset permission.Ruleset, toolName string, args json.RawMessage) toolPermissionDecision {
	toolName = tools.NormalizeName(toolName)
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}
	if strings.TrimSpace(toolName) == "" {
		return decision
	}
	if toolName == tools.NameCancel && ruleset.IsDisabled(tools.NameDelegate) {
		return decision
	}

	unwrapped := llm.UnwrapToolArgs(args)
	switch toolName {
	case tools.NameDelete:
		return evaluateDeleteToolPermission(ruleset, unwrapped)
	case tools.NameGlob:
		return evaluateGlobToolPermission(ruleset, unwrapped)
	case tools.NameShell:
		return evaluateShellToolPermission(ruleset, unwrapped)
	default:
		arg := extractToolArgument(toolName, unwrapped)
		decision.Action = normalizeToolPermissionAction(toolName, ruleset.Evaluate(toolName, arg))
		decision.MatchArgument = arg
		return decision
	}
}

func evaluateDeleteToolPermission(ruleset permission.Ruleset, args json.RawMessage) toolPermissionDecision {
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}
	req, err := tools.DecodeDeleteRequest(args)
	if err != nil {
		arg := extractToolArgument(tools.NameDelete, args)
		decision.Action = ruleset.Evaluate(tools.NameDelete, arg)
		decision.MatchArgument = arg
		return decision
	}

	items := make([]permissionAggregateItem, 0, len(req.Paths))
	for _, path := range req.Paths {
		action := ruleset.Evaluate(tools.NameDelete, path)
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

// evaluateGlobToolPermission aggregates permission decisions across every
// pattern in glob.patterns so a deny/ask rule on any later pattern cannot be
// bypassed by an earlier allowed pattern.
func evaluateGlobToolPermission(ruleset permission.Ruleset, args json.RawMessage) toolPermissionDecision {
	var parsed struct {
		Patterns json.RawMessage `json:"patterns"`
	}
	var patterns []string
	err := json.Unmarshal(args, &parsed)
	if err == nil {
		// patterns may be a JSON array or a single bare string; mirror the
		// executor's scalar->array coercion so permission rules are evaluated
		// against the real pattern instead of a wildcard fallback.
		patterns, _, err = tools.DecodeStringOrList(parsed.Patterns)
	}
	if err != nil || len(patterns) == 0 {
		arg := extractToolArgument(tools.NameGlob, args)
		return toolPermissionDecision{
			Action:        normalizeToolPermissionAction(tools.NameGlob, ruleset.Evaluate(tools.NameGlob, arg)),
			MatchArgument: arg,
		}
	}
	items := make([]permissionAggregateItem, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		action := normalizeToolPermissionAction(tools.NameGlob, ruleset.Evaluate(tools.NameGlob, pattern))
		item := permissionAggregateItem{Argument: pattern, Action: action}
		if action == permission.ActionAsk {
			item.AskList = []string{pattern}
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		arg := extractToolArgument(tools.NameGlob, args)
		return toolPermissionDecision{
			Action:        normalizeToolPermissionAction(tools.NameGlob, ruleset.Evaluate(tools.NameGlob, arg)),
			MatchArgument: arg,
		}
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
			decision.NeedsApprovalRules = nil
			decision.AlreadyAllowedPaths = nil
			decision.AlreadyAllowedRules = nil
			return decision
		case permission.ActionAsk:
			decision.Action = permission.ActionAsk
			decision.MatchArgument = item.Argument
			decision.NeedsApprovalPaths = append(decision.NeedsApprovalPaths, item.AskList...)
			decision.NeedsApprovalRules = append(decision.NeedsApprovalRules, item.AskRuleList...)
		case permission.ActionAllow:
			decision.AlreadyAllowedPaths = append(decision.AlreadyAllowedPaths, item.AllowList...)
			decision.AlreadyAllowedRules = append(decision.AlreadyAllowedRules, item.AllowRuleList...)
		}
	}
	if len(items) > 0 && decision.MatchArgument == fallbackMatch {
		decision.MatchArgument = items[0].Argument
	}
	decision.NeedsApprovalRules = dedupeStrings(decision.NeedsApprovalRules)
	decision.AlreadyAllowedRules = dedupeStrings(decision.AlreadyAllowedRules)
	return decision
}

func dedupeStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
