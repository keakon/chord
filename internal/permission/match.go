package permission

import "strings"

// MatchResult describes the last matching rule for a permission lookup.
type MatchResult struct {
	Rule  Rule
	Found bool
}

// LastMatch returns the last rule whose permission and pattern both match.
func (rs Ruleset) LastMatch(permission, pattern string) MatchResult {
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if globMatch(permission, r.Permission) && globMatch(pattern, r.Pattern) {
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

// LastExactPatternMatch returns the last rule whose permission matches and whose
// pattern is an exact literal equal to the provided pattern.
func (rs Ruleset) LastExactPatternMatch(permission, pattern string) MatchResult {
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if rulePatternHasWildcards(r.Pattern) {
			continue
		}
		if globMatch(permission, r.Permission) && r.Pattern == pattern {
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

func rulePatternHasWildcards(pattern string) bool {
	return strings.ContainsAny(pattern, "*?")
}
