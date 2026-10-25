package permission

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
	"github.com/keakon/chord/internal/toolname"
)

// pathRuleKind classifies how a rule pattern resolves onto the filesystem.
type pathRuleKind int

const (
	// pathRuleRelative matches paths inside the working directory, which are
	// normalized to cwd-relative form before matching.
	pathRuleRelative pathRuleKind = iota
	// pathRuleAbsolute matches absolute paths outside the working directory.
	pathRuleAbsolute
	// pathRuleAny is the literal "*" pattern, which matches every path spelling.
	pathRuleAny
)

// EvaluatePath resolves the action for a path-taking tool call. The input path
// is normalized against scope the same way the tool resolves it: paths inside
// a scope root collapse to root-relative form, paths outside every root stay
// absolute. Rules are classified the same way, so a relative rule can never
// match an outside-root path and an absolute rule never matches an
// inside-root path; only the literal "*" rule matches both spellings. With an
// empty scope it degrades to the plain lexical Evaluate.
func (rs Ruleset) EvaluatePath(permission, pattern string, scope PathScope) Action {
	if scope.degenerate() {
		return rs.Evaluate(permission, pattern)
	}
	normPerm := toolname.Normalize(permission)
	counterpart := getEditPatchCounterpart(normPerm)
	if counterpart != "" {
		if match, ok := rs.lastSpecificEditApplyPatchToolMatchPath(normPerm, counterpart, pattern, scope); ok {
			return match.Action
		}
	}
	if match := rs.lastMatchPath(permission, pattern, scope); match.Found {
		return match.Rule.Action
	}
	return ActionDeny
}

// LastSpecificToolMatchPath is the scope-aware counterpart of
// LastSpecificToolMatch: rule patterns are classified and matched against the
// normalized path instead of the raw string. With an empty scope it degrades
// to LastSpecificToolMatch.
func (rs Ruleset) LastSpecificToolMatchPath(permission, pattern string, scope PathScope) MatchResult {
	if scope.degenerate() {
		return rs.LastSpecificToolMatch(permission, pattern)
	}
	permission = toolname.Normalize(permission)
	normalized := normalizePathInput(pattern, scope)
	for _, r := range slices.Backward(rs) {
		normRulePerm := toolname.Normalize(r.Permission)
		if normRulePerm == "*" {
			continue
		}
		if globMatch(permission, normRulePerm) && pathRuleMatches(r.Pattern, normalized) {
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

// LastSpecificMatchPath returns the specific (non-wildcard-tool) rule that
// EvaluatePath would select for a path call, mirroring its same-tool /
// counterpart precedence. Found is false when the action comes from a
// wildcard default or no rule matched, so callers can tell a named rule apart
// from a blanket default.
func (rs Ruleset) LastSpecificMatchPath(permission, pattern string, scope PathScope) MatchResult {
	if scope.degenerate() {
		return rs.LastSpecificToolMatch(permission, pattern)
	}
	normPerm := toolname.Normalize(permission)
	if counterpart := getEditPatchCounterpart(normPerm); counterpart != "" {
		if match, ok := rs.lastSpecificEditApplyPatchToolMatchPath(normPerm, counterpart, pattern, scope); ok {
			return MatchResult{Rule: match, Found: true}
		}
	}
	return rs.LastSpecificToolMatchPath(permission, pattern, scope)
}

// CoversPath reports whether the rule names toolName and matches path under
// scope, using the same tool glob, path classification, and normalization as
// path evaluation. It is a reporting helper for rule suggestions: counterpart
// inheritance is intentionally not applied, so the answer is "would this exact
// rule fire for this call"; a wildcard tool rule therefore still covers the
// call because it would fire.
func (r Rule) CoversPath(toolName, path string, scope PathScope) bool {
	if !globMatch(toolname.Normalize(toolName), toolname.Normalize(r.Permission)) {
		return false
	}
	if scope.degenerate() {
		return globMatch(path, r.Pattern)
	}
	return pathRuleMatches(r.Pattern, normalizePathInput(path, scope))
}

// LastNamedPathMatch returns the last rule that names permission (a "*" tool
// pattern is skipped, as in LastSpecificToolMatchPath) and matches the path
// with a pattern that names a path rather than every path. Path gates use it
// to ask whether the user explicitly listed this file, so a blanket
// `present: allow` — tool-specific but path-universal — does not lift a
// path-level default denial.
func (rs Ruleset) LastNamedPathMatch(permission, pattern string, scope PathScope) MatchResult {
	if scope.degenerate() {
		return rs.lastNamedPathMatch(permission, pattern)
	}
	permission = toolname.Normalize(permission)
	normalized := normalizePathInput(pattern, scope)
	for _, r := range slices.Backward(rs) {
		normRulePerm := toolname.Normalize(r.Permission)
		if normRulePerm == "*" || !pathRuleNamesPath(r.Pattern) {
			continue
		}
		if globMatch(permission, normRulePerm) && pathRuleMatches(r.Pattern, normalized) {
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

func (rs Ruleset) lastNamedPathMatch(permission, pattern string) MatchResult {
	permission = toolname.Normalize(permission)
	for _, r := range slices.Backward(rs) {
		normRulePerm := toolname.Normalize(r.Permission)
		if normRulePerm == "*" || !pathRuleNamesPath(r.Pattern) {
			continue
		}
		if globMatch(permission, normRulePerm) && globMatch(pattern, r.Pattern) {
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

// pathRuleNamesPath reports whether a rule pattern names paths instead of
// covering only wildcard-and-separator spellings. The literal "*" is
// universal by kind; beyond that, a pattern with no literal name at all —
// a bare run of "*", its "./*" spelling, or "**/*" — matches paths at every
// directory level because the glob crosses separators, so it cannot stand
// for a file the user named.
func pathRuleNamesPath(pattern string) bool {
	kind, normalized := classifyPathRule(pattern)
	if kind == pathRuleAny {
		return false
	}
	return strings.Trim(normalized, "*/") != ""
}

func (rs Ruleset) lastMatchPath(permission, pattern string, scope PathScope) MatchResult {
	permission = toolname.Normalize(permission)
	normalized := normalizePathInput(pattern, scope)
	for _, r := range slices.Backward(rs) {
		if globMatch(permission, toolname.Normalize(r.Permission)) && pathRuleMatches(r.Pattern, normalized) {
			return MatchResult{Rule: r, Found: true}
		}
	}
	return MatchResult{}
}

func (rs Ruleset) lastSpecificEditApplyPatchToolMatchPath(toolName, counterpart, pattern string, scope PathScope) (Rule, bool) {
	normalized := normalizePathInput(pattern, scope)
	var counterpartMatch Rule
	counterpartFound := false
	for _, r := range slices.Backward(rs) {
		normRulePerm := toolname.Normalize(r.Permission)
		if normRulePerm == "*" {
			continue
		}
		if (normRulePerm == toolName || globMatch(toolName, normRulePerm)) && pathRuleMatches(r.Pattern, normalized) {
			return r, true
		}
		if !counterpartFound && (normRulePerm == counterpart || globMatch(counterpart, normRulePerm)) && pathRuleMatches(r.Pattern, normalized) {
			counterpartMatch = r
			counterpartFound = true
		}
	}
	return counterpartMatch, counterpartFound
}

// pathRuleMatches reports whether a rule pattern matches a normalized input
// path. The pattern is classified once per evaluation (patterns are short and
// rule lookups dominate the cost); the glob itself reuses the cached matcher.
func pathRuleMatches(pattern, input string) bool {
	kind, normalizedPattern := classifyPathRule(pattern)
	switch kind {
	case pathRuleAny:
		return true
	case pathRuleAbsolute:
		if !pathIsAbsoluteForm(input) {
			return false
		}
	case pathRuleRelative:
		if pathIsAbsoluteForm(input) {
			return false
		}
	}
	return globMatch(input, normalizedPattern)
}

func pathIsAbsoluteForm(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/")
}

// classifyPathRule normalizes a rule pattern into a kind plus the spelling
// used for matching:
//   - "*" matches every path spelling (any path).
//   - "~/..." (or "~\..." on Windows) and "/..." (or any absolute form) match
//     absolute paths outside the working directory.
//   - "./..." loses the redundant prefix and behaves like a relative rule.
//   - anything else is relative to the working directory.
func classifyPathRule(pattern string) (pathRuleKind, string) {
	return classifyPathRuleForOS(pattern, runtime.GOOS == "windows")
}

// classifyPathRuleForOS is the OS-parameterized form of classifyPathRule so
// the Windows home-pattern spelling ("~\") is testable on any platform. It
// mirrors pathutil.expandTilde, which expands "~\" only on Windows.
func classifyPathRuleForOS(pattern string, isWindows bool) (pathRuleKind, string) {
	p := strings.TrimSpace(pattern)
	switch {
	case p == "*":
		return pathRuleAny, "*"
	case strings.HasPrefix(p, "~/"):
		return pathRuleAbsolute, expandHomePattern(p[2:])
	case strings.HasPrefix(p, `~\`) && isWindows:
		return pathRuleAbsolute, expandHomePattern(p[2:])
	case p == "~":
		return pathRuleAbsolute, expandHomePattern("")
	case strings.HasPrefix(p, "./"):
		return pathRuleRelative, filepath.ToSlash(filepath.Clean(p[2:]))
	case filepath.IsAbs(p):
		return pathRuleAbsolute, filepath.ToSlash(filepath.Clean(p))
	default:
		return pathRuleRelative, filepath.ToSlash(filepath.Clean(p))
	}
}

func expandHomePattern(rel string) string {
	home, err := pathutil.ExpandTilde("~")
	if err != nil {
		// Keep the raw pattern so it can still match literally when no home
		// directory is resolvable.
		return filepath.ToSlash(filepath.Clean("~/" + rel))
	}
	return filepath.ToSlash(filepath.Clean(filepath.Join(home, rel)))
}
