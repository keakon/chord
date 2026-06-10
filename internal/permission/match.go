package permission

import (
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
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if globMatch(permission, toolname.Normalize(r.Permission)) && globMatch(pattern, r.Pattern) {
			if skipCompoundShellAllow && r.Action == ActionAllow && shellCompoundCommandNeedsReview(permission, pattern, r.Pattern) {
				continue
			}
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

// LastExactPatternMatch returns the last rule whose permission matches and whose
// pattern is an exact literal equal to the provided pattern.
func (rs Ruleset) LastExactPatternMatch(permission, pattern string) MatchResult {
	permission = toolname.Normalize(permission)
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
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
