package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
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

// permissionRuleTargetsTool reports whether one rule names toolName through a
// non-global tool pattern: an exact spelling, a narrow glob such as
// `compact_*`, or any alias that normalizes to the same tool. The literal `*`
// tool pattern is never a match — it is the wildcard default, not a rule about
// this particular tool.
//
// The rule's own argument pattern is passed through as the lookup argument, so
// the answer is "does this rule name the tool?" independently of what the
// argument pattern happens to be. Tools whose permission decision has no
// meaningful matching argument (compact_context, and the YOLO-protected
// control tools) need exactly that question answered; matching them against a
// literal "*" argument would silently drop every parameterized rule, because
// globMatch("*", "some-argument") is false.
//
// The repo's canonical glob + tool-name normalization live in the permission
// package (LastSpecificToolMatch); wrapping a single rule in a ruleset reuses
// them instead of re-implementing a matcher here.
func permissionRuleTargetsTool(rule permission.Rule, toolName string) bool {
	return permission.Ruleset{rule}.LastSpecificToolMatch(toolName, rule.Pattern).Found
}

// compactContextPermissionAction resolves the effective permission action for
// the compact_context tool. The tool is registered only while the
// context.compaction.model_driven feature is enabled, so registration itself
// is the user's authorization: wildcard-only rules — such as an allowlist's
// `"*": deny` — must neither hide the tool nor block its calls, or a user who
// enables model_driven would silently lose the feature unless they also knew
// to allow the internal tool name. Only a non-global rule whose tool pattern
// matches compact_context (an explicit deny / ask / allow) overrides that
// default; an explicit deny keeps the tool hidden and its calls rejected.
// Narrow globs such as compact_* are specific rules too, and so are rules
// written with an argument pattern (`compact_context: {"anything": deny}`):
// the tool takes no permission-matching argument, so any rule naming it
// applies, and the last such rule wins.
func compactContextPermissionAction(ruleset permission.Ruleset) permission.Action {
	for _, rule := range slices.Backward(ruleset) {
		if permissionRuleTargetsTool(rule, tools.NameCompactContext) {
			return rule.Action
		}
	}
	return permission.ActionAllow
}

func evaluateToolPermission(ruleset permission.Ruleset, toolName string, args json.RawMessage) toolPermissionDecision {
	return evaluateToolPermissionInDir(ruleset, toolName, args, "")
}

// evaluateToolPermissionInDir is the cwd-aware entry point. cwd is the
// session working directory the tool would execute in (its base dir); when it
// is non-empty, path-taking tools are matched with EvaluatePath so relative
// and absolute spellings of the same file converge on one rule. An empty cwd
// degrades to the plain lexical matching of evaluateToolPermission.
func evaluateToolPermissionInDir(ruleset permission.Ruleset, toolName string, args json.RawMessage, cwd string) toolPermissionDecision {
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
	case tools.NameApplyPatch:
		return evaluateApplyPatchPermissionInDir(ruleset, unwrapped, cwd)
	case tools.NameDelete:
		return evaluateDeleteToolPermissionInDir(ruleset, unwrapped, cwd)
	case tools.NameGlob:
		return evaluateGlobToolPermission(ruleset, unwrapped)
	case tools.NameShell:
		return evaluateShellToolPermission(ruleset, unwrapped)
	case tools.NameWebFetch:
		return evaluateWebFetchToolPermission(ruleset, unwrapped)
	case tools.NameCompactContext:
		// The tool is registered only while the model-driven compaction
		// feature is enabled, so registration is the user's authorization:
		// wildcard-only rules (an allowlist's `"*": deny`) do not block its
		// calls. Non-global rules whose tool pattern matches compact_context
		// still apply — deny rejects, ask confirms, allow passes — see
		// compactContextPermissionAction.
		return toolPermissionDecision{Action: compactContextPermissionAction(ruleset), MatchArgument: "*"}
	default:
		arg := extractToolArgument(toolName, unwrapped)
		if isPathToolPermission(toolName) && strings.TrimSpace(cwd) != "" {
			decision.Action = normalizeToolPermissionAction(toolName, ruleset.EvaluatePath(toolName, arg, cwd))
		} else {
			decision.Action = normalizeToolPermissionAction(toolName, ruleset.Evaluate(toolName, arg))
		}
		decision.MatchArgument = arg
		return decision
	}
}

// isPathToolPermission reports whether a tool takes a filesystem path as its
// matching argument, so its rules participate in cwd-relative matching.
func isPathToolPermission(toolName string) bool {
	switch tools.NormalizeName(toolName) {
	case tools.NameRead, tools.NameWrite, tools.NameEdit, tools.NameViewImage:
		return true
	default:
		return false
	}
}

func evaluateApplyPatchPermissionInDir(ruleset permission.Ruleset, args json.RawMessage, cwd string) toolPermissionDecision {
	targets, err := tools.ApplyPatchDisplayTargets(args)
	if err != nil {
		arg := extractToolArgument(tools.NameApplyPatch, args)
		if strings.TrimSpace(cwd) != "" {
			return toolPermissionDecision{Action: ruleset.EvaluatePath(tools.NameApplyPatch, arg, cwd), MatchArgument: arg}
		}
		return toolPermissionDecision{Action: ruleset.Evaluate(tools.NameApplyPatch, arg), MatchArgument: arg}
	}
	paths := make([]string, 0, len(targets)*2)
	deletePaths := make(map[string]bool)
	writePaths := make(map[string]bool)
	for _, target := range targets {
		// Match rules against lexically cleaned paths: the model-facing patch
		// spells paths free-form, and "./secret/x" must not slip past a
		// "secret/*" deny rule. Resolution stays as-written (relative) by
		// design; cleaning only removes redundant ./ and inner ".." hops.
		source := filepath.ToSlash(filepath.Clean(target.SourcePath))
		paths = append(paths, source)
		switch target.Kind {
		case tools.MutationAdd:
			writePaths[source] = true
		case tools.MutationDelete:
			deletePaths[source] = true
		case tools.MutationUpdate, tools.MutationMove:
			if target.TargetPath != "" {
				// Move removes the source even though its protocol header is Update.
				deletePaths[source] = true
			}
		}
		if target.TargetPath != "" {
			if targetPath := filepath.ToSlash(filepath.Clean(target.TargetPath)); targetPath != source {
				paths = append(paths, targetPath)
				writePaths[targetPath] = true
			}
		}
	}
	items := make([]permissionAggregateItem, 0, len(paths))
	for _, path := range dedupeStrings(paths) {
		var action permission.Action
		if strings.TrimSpace(cwd) != "" {
			action = ruleset.EvaluatePath(tools.NameApplyPatch, path, cwd)
		} else {
			action = ruleset.Evaluate(tools.NameApplyPatch, path)
		}
		// apply_patch subsumes write/delete for patch-native models. Preserve any
		// explicit operation-specific threshold when those standalone tools are
		// hidden; wildcard defaults remain represented by the patch evaluation.
		for _, toolName := range []string{tools.NameWrite, tools.NameDelete} {
			applies := toolName == tools.NameWrite && writePaths[path] || toolName == tools.NameDelete && deletePaths[path]
			if applies {
				var match permission.MatchResult
				if strings.TrimSpace(cwd) != "" {
					match = ruleset.LastSpecificToolMatchPath(toolName, path, cwd)
				} else {
					match = ruleset.LastSpecificToolMatch(toolName, path)
				}
				if match.Found {
					action = permission.StricterAction(action, match.Rule.Action)
				}
			}
		}
		item := permissionAggregateItem{Argument: path, Action: action}
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

func evaluateDeleteToolPermissionInDir(ruleset permission.Ruleset, args json.RawMessage, cwd string) toolPermissionDecision {
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}
	req, err := tools.DecodeDeleteRequest(args)
	if err != nil {
		arg := extractToolArgument(tools.NameDelete, args)
		if strings.TrimSpace(cwd) != "" {
			decision.Action = ruleset.EvaluatePath(tools.NameDelete, arg, cwd)
		} else {
			decision.Action = ruleset.Evaluate(tools.NameDelete, arg)
		}
		decision.MatchArgument = arg
		return decision
	}

	items := make([]permissionAggregateItem, 0, len(req.Paths))
	for _, path := range req.Paths {
		var action permission.Action
		if strings.TrimSpace(cwd) != "" {
			action = ruleset.EvaluatePath(tools.NameDelete, path, cwd)
		} else {
			action = ruleset.Evaluate(tools.NameDelete, path)
		}
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

// evaluateWebFetchToolPermission matches the requested URL against WebFetch
// host/IP/CIDR/port rules, falling back to generic wildcard evaluation when
// there is no URL to match on.
func evaluateWebFetchToolPermission(ruleset permission.Ruleset, args json.RawMessage) toolPermissionDecision {
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}
	arg := extractToolArgument(tools.NameWebFetch, args)
	if strings.TrimSpace(arg) == "" || arg == "*" {
		decision.Action = ruleset.Evaluate(tools.NameWebFetch, arg)
		decision.MatchArgument = arg
		return decision
	}
	if match := ruleset.EvaluateWebFetch(arg); match.Found {
		decision.Action = match.Rule.Action
	}
	decision.MatchArgument = arg
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
