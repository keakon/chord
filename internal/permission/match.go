package permission

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/toolname"
)

// MatchResult describes the last matching rule for a permission lookup.
type MatchResult struct {
	Rule  Rule
	Found bool
}

// LastMatch returns the last rule whose permission and pattern both match.
func (rs Ruleset) LastMatch(permission, pattern string) MatchResult {
	return rs.lastMatch(permission, pattern, false)
}

// LastEvaluatedMatch returns the rule selected by Evaluate, preserving its
// safety checks while exposing the matched rule for UI/reporting purposes.
func (rs Ruleset) LastEvaluatedMatch(permission, pattern string) MatchResult {
	return rs.lastMatch(permission, pattern, true)
}

func (rs Ruleset) lastMatch(permission, pattern string, skipCompoundShellAllow bool) MatchResult {
	permission = toolname.Normalize(permission)
	for _, r := range slices.Backward(rs) {

		if globMatch(permission, toolname.Normalize(r.Permission)) && globMatch(pattern, r.Pattern) {
			if skipCompoundShellAllow && r.Action == ActionAllow && shellCompoundCommandNeedsReview(permission, pattern, r.Pattern) {
				continue
			}
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

// LastSpecificToolMatch returns the last rule that names toolName specifically
// (wildcard-only "*" tool rules are skipped) and whose argument pattern matches.
// It answers "did the user write a rule for this particular tool?" so callers
// can layer tool-specific constraints without inheriting wildcard defaults.
// Only the literal "*" is treated as non-specific: a glob tool name such as
// "del*" still counts as specific, since the user deliberately targeted a
// narrower tool set than the wildcard default.
func (rs Ruleset) LastSpecificToolMatch(permission, pattern string) MatchResult {
	permission = toolname.Normalize(permission)
	for _, r := range slices.Backward(rs) {
		normRulePerm := toolname.Normalize(r.Permission)
		if normRulePerm == "*" {
			continue
		}
		if globMatch(permission, normRulePerm) && globMatch(pattern, r.Pattern) {
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

// StricterAction returns the more restrictive of two actions (deny > ask > allow).
func StricterAction(a, b Action) Action {
	if a == ActionDeny || b == ActionDeny {
		return ActionDeny
	}
	if a == ActionAsk || b == ActionAsk {
		return ActionAsk
	}
	return ActionAllow
}

// LastExactPatternMatch returns the last rule whose permission matches and whose
// pattern is an exact literal equal to the provided pattern.
func (rs Ruleset) LastExactPatternMatch(permission, pattern string) MatchResult {
	permission = toolname.Normalize(permission)
	for _, r := range slices.Backward(rs) {

		if rulePatternHasWildcards(r.Pattern) {
			continue
		}
		if globMatch(permission, toolname.Normalize(r.Permission)) && r.Pattern == pattern {
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

func rulePatternHasWildcards(pattern string) bool {
	return strings.ContainsAny(pattern, "*?")
}
