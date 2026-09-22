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

// specificToolRuleAction resolves a control tool's action from the rules that
// name it directly, ignoring the wildcard default entirely, and falls back to
// fallback when no rule names it. Narrow globs such as compact_* are specific
// rules too, and so are rules written with an argument pattern
// (`compact_context: {"anything": deny}`): the tools resolved here take no
// permission-matching argument, so any rule naming one applies, and the last
// such rule wins.
func specificToolRuleAction(ruleset permission.Ruleset, toolName string, fallback permission.Action) permission.Action {
	for _, rule := range slices.Backward(ruleset) {
		if permissionRuleTargetsTool(rule, toolName) {
			return rule.Action
		}
	}
	return fallback
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
func compactContextPermissionAction(ruleset permission.Ruleset) permission.Action {
	return specificToolRuleAction(ruleset, tools.NameCompactContext, permission.ActionAllow)
}

// donePermissionAction resolves the effective permission action for the done
// tool while loop mode is active. Loop mode is entered explicitly by the user
// (`/loop`), and its completion contract designates done as the required exit
// signal, so that opt-in is the authorization — the same reasoning that makes
// registering compact_context authorization for compact_context. Without this,
// an allowlist role (`"*": deny` plus a few tools) would enter a loop it cannot
// finish: done is hidden and its calls rejected, leaving only the
// `<blocked>` escape hatch or exhausting the interception budget.
//
// done carries no external side effect to protect here — it is read-only and
// its executor only echoes the report back, while the runtime intercepts the
// result and independently decides whether exit is granted. A rule naming done
// still wins, so `done: deny` keeps a loop under human-only termination.
//
// Callers must gate this on loop mode being active (toolPermissionContext.
// LoopExitAuthorized). Outside a loop, done keeps plain wildcard semantics:
// nothing requires it there, and both its tool description and the Response
// Closure prompt block actively tell the model not to call it.
func donePermissionAction(ruleset permission.Ruleset) permission.Action {
	return specificToolRuleAction(ruleset, tools.NameDone, permission.ActionAllow)
}

// toolPermissionContext carries the agent-state gates that a few control tools
// need on top of the ruleset itself. The zero value is the conservative
// default: no runtime mode is active, so every gated tool falls back to plain
// wildcard semantics.
// permRulesetIdentity captures the identity of the ruleset a permission
// decision was evaluated against. Rulesets are replaced wholesale on every
// change (overlay merge, role refresh, session-rule intent), so the backing
// array header plus the YOLO flag distinguish every state the evaluation
// semantics depend on; no in-place rule mutation happens.
type permRulesetIdentity struct {
	ruleset *permission.Rule
	length  int
	yolo    bool
}

func rulesetSliceID(rs permission.Ruleset) *permission.Rule {
	if len(rs) > 0 {
		return &rs[0]
	}
	return nil
}

// permApprovalRecord is one cached allow decision: the exact evaluation inputs
// are re-checked before reuse, so any drift (hook-modified args, ruleset or
// YOLO change, different cwd, loop-mode pctx) falls back to a fresh evaluation.
type permApprovalRecord struct {
	// name is part of the identity, not redundant with callID: a provider is
	// free to reuse a call id, and two different tools can carry byte-identical
	// args ({"path":"x"} for read and for delete), so without it an allow could
	// be replayed for a tool the ruleset denies.
	name      string
	args      string
	rulesetID permRulesetIdentity
	cwd       string
}

// recordPermissionApproval stores an allow decision for this turn's call.
func (t *Turn) recordPermissionApproval(callID, name, args, cwd string, rulesetID permRulesetIdentity) {
	if t == nil || callID == "" {
		return
	}
	t.permissionApprovalsMu.Lock()
	defer t.permissionApprovalsMu.Unlock()
	if t.permissionApprovals == nil {
		t.permissionApprovals = make(map[string]permApprovalRecord)
	}
	t.permissionApprovals[callID] = permApprovalRecord{name: name, args: args, rulesetID: rulesetID, cwd: cwd}
}

// permissionApprovalMatches reports whether a recorded allow decision still
// applies to the exact evaluation inputs the finalize path would use. pctx
// must be the zero value: recorded decisions were taken without loop context.
func (t *Turn) permissionApprovalMatches(callID, name, args, cwd string, current permRulesetIdentity, pctx toolPermissionContext) bool {
	if t == nil || callID == "" || pctx != (toolPermissionContext{}) {
		return false
	}
	t.permissionApprovalsMu.Lock()
	defer t.permissionApprovalsMu.Unlock()
	record, ok := t.permissionApprovals[callID]
	if !ok {
		return false
	}
	return record.name == name &&
		record.args == args &&
		record.cwd == cwd &&
		record.rulesetID == current
}

type toolPermissionContext struct {
	// LoopExitAuthorized reports that loop mode is currently active, which
	// authorizes done against wildcard-only rules. See donePermissionAction.
	LoopExitAuthorized bool
}

func evaluateToolPermission(ruleset permission.Ruleset, toolName string, args json.RawMessage) toolPermissionDecision {
	return evaluateToolPermissionInDir(ruleset, toolName, args, permission.PathScope{})
}

// evaluateToolPermissionInDir is the scope-aware entry point. scope carries the
// tool base dir plus the repository's checkout roots, so relative and absolute
// spellings of one repository file converge on one rule regardless of which
// checkout the agent stands in. A zero scope degrades to the plain lexical
// matching of evaluateToolPermission.
//
// It evaluates with a zero toolPermissionContext, so the loop-gated done
// exemption is off. Callers that own the loop state must use
// evaluateToolPermissionInDirWithContext instead.
func evaluateToolPermissionInDir(ruleset permission.Ruleset, toolName string, args json.RawMessage, scope permission.PathScope) toolPermissionDecision {
	return evaluateToolPermissionInDirWithContext(ruleset, toolName, args, scope, toolPermissionContext{})
}

func evaluateToolPermissionInDirWithContext(ruleset permission.Ruleset, toolName string, args json.RawMessage, scope permission.PathScope, pctx toolPermissionContext) toolPermissionDecision {
	toolName = tools.NormalizeName(toolName)
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}
	if strings.TrimSpace(toolName) == "" {
		return decision
	}
	if toolName == tools.NameCancel && ruleset.IsDisabled(tools.NameDelegate) {
		return decision
	}
	if toolName == tools.NameDone && pctx.LoopExitAuthorized {
		// Loop mode is the user's authorization for the loop's own exit
		// signal, so wildcard-only rules do not reject it. A rule naming done
		// still wins — see donePermissionAction.
		return toolPermissionDecision{Action: donePermissionAction(ruleset), MatchArgument: "*"}
	}

	unwrapped := llm.UnwrapToolArgs(args)
	switch toolName {
	case tools.NameApplyPatch:
		return evaluateApplyPatchPermissionInDir(ruleset, unwrapped, scope)
	case tools.NameDelete:
		return evaluateDeleteToolPermissionInDir(ruleset, unwrapped, scope)
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
		// EvaluatePath degrades to the plain lexical Evaluate for an empty
		// scope; non-path tools keep matching their argument lexically.
		if isPathToolPermission(toolName) {
			decision.Action = normalizeToolPermissionAction(toolName, ruleset.EvaluatePath(toolName, arg, scope))
		} else {
			decision.Action = normalizeToolPermissionAction(toolName, ruleset.Evaluate(toolName, arg))
		}
		decision.MatchArgument = arg
		return decision
	}
}

// isPathToolPermission reports whether a tool takes a filesystem path as its
// matching argument, so its rules participate in path-scope matching.
func isPathToolPermission(toolName string) bool {
	switch tools.NormalizeName(toolName) {
	case tools.NameRead, tools.NameWrite, tools.NameEdit, tools.NameViewImage:
		return true
	default:
		return false
	}
}

func evaluateApplyPatchPermissionInDir(ruleset permission.Ruleset, args json.RawMessage, scope permission.PathScope) toolPermissionDecision {
	targets, err := tools.ApplyPatchDisplayTargets(args)
	if err != nil {
		arg := extractToolArgument(tools.NameApplyPatch, args)
		return toolPermissionDecision{Action: ruleset.EvaluatePath(tools.NameApplyPatch, arg, scope), MatchArgument: arg}
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
		action := ruleset.EvaluatePath(tools.NameApplyPatch, path, scope)
		// apply_patch subsumes write/delete for patch-native models. Preserve any
		// explicit operation-specific threshold when those standalone tools are
		// hidden; wildcard defaults remain represented by the patch evaluation.
		for _, toolName := range []string{tools.NameWrite, tools.NameDelete} {
			applies := toolName == tools.NameWrite && writePaths[path] || toolName == tools.NameDelete && deletePaths[path]
			if applies {
				if match := ruleset.LastSpecificToolMatchPath(toolName, path, scope); match.Found {
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

func evaluateDeleteToolPermissionInDir(ruleset permission.Ruleset, args json.RawMessage, scope permission.PathScope) toolPermissionDecision {
	decision := toolPermissionDecision{Action: permission.ActionDeny, MatchArgument: "*"}
	req, err := tools.DecodeDeleteRequest(args)
	if err != nil {
		arg := extractToolArgument(tools.NameDelete, args)
		decision.Action = ruleset.EvaluatePath(tools.NameDelete, arg, scope)
		decision.MatchArgument = arg
		return decision
	}

	items := make([]permissionAggregateItem, 0, len(req.Paths))
	for _, path := range req.Paths {
		action := ruleset.EvaluatePath(tools.NameDelete, path, scope)
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
