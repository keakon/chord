package permission

import (
	"strings"
)

// NormalizeRulePath returns the spelling a path takes when rules are matched
// against it under scope: paths inside a scope root collapse to the
// root-relative form, paths outside every root keep the absolute spelling.
// Rule authors see this spelling in the confirmation rule picker, so it must
// be the same normalization EvaluatePath applies to the call being approved.
func NormalizeRulePath(path string, scope PathScope) string {
	return normalizePathInput(path, scope)
}

// RulePathInScope reports whether scope places path on the relative side of
// the rule boundary: the normalized spelling is relative, so relative rules —
// including the recursive "**" — can match it. Absolute spellings, a path that
// escapes the scope, and the scope root itself (which normalizes to ".") all
// report false. With a degenerate scope the check is lexical, mirroring the
// plain Evaluate path.
func RulePathInScope(path string, scope PathScope) bool {
	normalized := NormalizeRulePath(path, scope)
	switch {
	case normalized == "", normalized == ".", normalized == "..":
		return false
	case pathIsAbsoluteForm(normalized):
		return false
	case strings.HasPrefix(normalized, "../"):
		return false
	default:
		return true
	}
}
