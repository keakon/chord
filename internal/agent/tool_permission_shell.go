package agent

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func evaluateShellToolPermission(ruleset permission.Ruleset, args json.RawMessage) toolPermissionDecision {
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}

	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil || strings.TrimSpace(parsed.Command) == "" {
		arg := extractToolArgument(tools.NameShell, args)
		decision.Action = ruleset.Evaluate(tools.NameShell, arg)
		decision.MatchArgument = arg
		return decision
	}

	rawCommand := strings.TrimSpace(parsed.Command)
	if exact := ruleset.LastExactPatternMatch(tools.NameShell, rawCommand); exact.Found {
		decision.Action = exact.Rule.Action
		decision.MatchArgument = rawCommand
		switch exact.Rule.Action {
		case permission.ActionAsk:
			decision.NeedsApprovalPaths = []string{rawCommand}
			decision.NeedsApprovalRules = []string{exact.Rule.Pattern}
		case permission.ActionAllow:
			decision.AlreadyAllowedPaths = []string{rawCommand}
			decision.AlreadyAllowedRules = []string{exact.Rule.Pattern}
		}
		return decision
	}

	analysis, err := tools.AnalyzeShellCommand(rawCommand)
	if err != nil || len(analysis.Subcommands) == 0 {
		match := ruleset.LastEvaluatedMatch(tools.NameShell, rawCommand)
		decision.Action = permission.ActionDeny
		if match.Found {
			decision.Action = match.Rule.Action
		}
		decision.MatchArgument = rawCommand
		switch decision.Action {
		case permission.ActionAsk:
			decision.NeedsApprovalPaths = []string{rawCommand}
			if match.Found {
				decision.NeedsApprovalRules = []string{match.Rule.Pattern}
			}
		case permission.ActionAllow:
			decision.AlreadyAllowedPaths = []string{rawCommand}
			if match.Found {
				decision.AlreadyAllowedRules = []string{match.Rule.Pattern}
			}
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
		})
		last := &items[len(items)-1]
		match := ruleset.LastMatch(tools.NameShell, source)
		// The reviewed text can keep a command from matching the rule written
		// for it, so a rule that names the command's own text keeps its force:
		// otherwise `FOO=bar; rm -rf /` would fall through to a broader rule
		// instead of the `rm *` deny, and `FOO=bar rm -rf /` would fall through
		// to a broad `allow` instead of the narrow `rm *` ask. An `ask` only
		// overrides when the broader rule also covers the command text, so an
		// explicit allow written for the assignment-bearing text still wins.
		if commandSource := strings.TrimSpace(sub.CommandSource); commandSource != "" && commandSource != source {
			if commandMatch := ruleset.LastMatch(tools.NameShell, commandSource); commandMatch.Found {
				overrides := commandMatch.Rule.Action == permission.ActionDeny ||
					(commandMatch.Rule.Action == permission.ActionAsk &&
						match.Found && match.Rule.Action == permission.ActionAllow &&
						match.Rule.Matches(tools.NameShell, commandSource))
				if overrides {
					match = commandMatch
					last.Argument = commandSource
				}
			}
		}
		if match.Found {
			last.Action = match.Rule.Action
		} else {
			last.Action = permission.ActionDeny
		}
		switch last.Action {
		case permission.ActionAsk:
			last.AskList = []string{last.Argument}
			if match.Found {
				last.AskRuleList = []string{match.Rule.Pattern}
			}
		case permission.ActionAllow:
			last.AllowList = []string{last.Argument}
			if match.Found {
				last.AllowRuleList = []string{match.Rule.Pattern}
			}
		}
	}
	if len(items) == 0 {
		match := ruleset.LastEvaluatedMatch(tools.NameShell, rawCommand)
		decision.Action = permission.ActionDeny
		if match.Found {
			decision.Action = match.Rule.Action
		}
		decision.MatchArgument = rawCommand
		return decision
	}
	return aggregatePermissionItems(items, permission.ActionAllow, rawCommand)
}
