package agent

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func evaluateBashToolPermission(ruleset permission.Ruleset, args json.RawMessage) toolPermissionDecision {
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}

	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil || strings.TrimSpace(parsed.Command) == "" {
		arg := extractToolArgument("Bash", args)
		decision.Action = ruleset.Evaluate("Bash", arg)
		decision.MatchArgument = arg
		return decision
	}

	rawCommand := strings.TrimSpace(parsed.Command)
	if exact := ruleset.LastExactPatternMatch("Bash", rawCommand); exact.Found {
		decision.Action = exact.Rule.Action
		decision.MatchArgument = rawCommand
		switch exact.Rule.Action {
		case permission.ActionAsk:
			decision.NeedsApprovalPaths = []string{rawCommand}
		case permission.ActionAllow:
			decision.AlreadyAllowedPaths = []string{rawCommand}
		}
		return decision
	}

	analysis, err := tools.AnalyzeBashCommand(rawCommand)
	if err != nil || len(analysis.Subcommands) == 0 {
		decision.Action = ruleset.Evaluate("Bash", rawCommand)
		decision.MatchArgument = rawCommand
		switch decision.Action {
		case permission.ActionAsk:
			decision.NeedsApprovalPaths = []string{rawCommand}
		case permission.ActionAllow:
			decision.AlreadyAllowedPaths = []string{rawCommand}
		}
		return decision
	}

	items := make([]permissionAggregateItem, 0, len(analysis.Subcommands))
	for _, sub := range analysis.Subcommands {
		source := strings.TrimSpace(sub.Source)
		if source == "" {
			continue
		}
		items = append(items, permissionAggregateItem{
			Argument: source,
			Action:   ruleset.Evaluate("Bash", source),
		})
		last := &items[len(items)-1]
		switch last.Action {
		case permission.ActionAsk:
			last.AskList = []string{source}
		case permission.ActionAllow:
			last.AllowList = []string{source}
		}
	}
	if len(items) == 0 {
		decision.Action = ruleset.Evaluate("Bash", rawCommand)
		decision.MatchArgument = rawCommand
		return decision
	}
	return aggregatePermissionItems(items, permission.ActionAllow, rawCommand)
}
