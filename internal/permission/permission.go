// Package permission implements a rule-based permission system for tool access control.
//
// Permissions are defined as an ordered list of rules. Each rule maps a (tool pattern, argument pattern)
// pair to an action (allow, ask, deny). Rules are evaluated in reverse order (last match wins),
// allowing later declarations to override earlier ones.
package permission

import (
	"regexp"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/toolname"

	"gopkg.in/yaml.v3"
)

// Action determines how a tool invocation is handled.
type Action string

const (
	ActionAllow Action = "allow" // auto-execute without confirmation
	ActionAsk   Action = "ask"   // requires user confirmation before execution
	ActionDeny  Action = "deny"  // tool is unavailable (not injected to LLM)
)

// Rule is a single permission entry: (tool pattern, argument pattern) → action.
type Rule struct {
	Permission string // tool name pattern (e.g., "Shell", "Skill", "*")
	Pattern    string // sub-command/argument pattern (e.g., "rm *", "go-expert", "*")
	Action     Action
}

// Ruleset is an ordered list of permission rules.
type Ruleset []Rule

// ParsePermission parses a YAML permission node preserving key order.
// yaml.Node.Content for MappingNode is [key1, val1, key2, val2, ...] in document order.
//
// Supported formats:
//   - Scalar: "deny"         → [{Permission: "*", Pattern: "*", Action: deny}]
//   - Mapping with scalar:   "Read: allow" → {Permission: "Read", Pattern: "*", Action: allow}
//   - Mapping with sub-map:  "Shell: { rm *: deny }" → {Permission: "Shell", Pattern: "rm *", Action: deny}
func ParsePermission(node *yaml.Node) Ruleset {
	if node == nil {
		return nil
	}

	// Scalar: "deny" → [{Permission: "*", Pattern: "*", Action: deny}]
	if node.Kind == yaml.ScalarNode {
		return Ruleset{{Permission: "*", Pattern: "*", Action: Action(node.Value)}}
	}

	if node.Kind != yaml.MappingNode {
		return nil
	}

	var rules Ruleset
	for i := 0; i < len(node.Content); i += 2 {
		toolName := toolname.Normalize(node.Content[i].Value)
		valNode := node.Content[i+1]

		switch valNode.Kind {
		case yaml.ScalarNode:
			// "Read: allow" → {Permission: "Read", Pattern: "*", Action: allow}
			rules = append(rules, Rule{
				Permission: toolName,
				Pattern:    "*",
				Action:     Action(valNode.Value),
			})
		case yaml.MappingNode:
			// Shell: { "rm *": deny, "git *": allow }
			for j := 0; j < len(valNode.Content); j += 2 {
				rules = append(rules, Rule{
					Permission: toolName,
					Pattern:    valNode.Content[j].Value,
					Action:     Action(valNode.Content[j+1].Value),
				})
			}
		}
	}
	return rules
}

// Evaluate resolves the action for a tool call by scanning rules in reverse order.
// Both permission (tool name) and pattern (argument) support glob wildcards (* and ?).
// Returns ActionDeny if no rule matches.
func (rs Ruleset) Evaluate(permission, pattern string) Action {
	return rs.evaluateWithEditPatchFallback(permission, pattern)
}

// getEditPatchCounterpart returns the counterpart tool name for edit/patch,
// or empty string if the tool is not edit/patch.
func getEditPatchCounterpart(toolName string) string {
	switch toolName {
	case toolname.Edit:
		return toolname.Patch
	case toolname.Patch:
		return toolname.Edit
	default:
		return ""
	}
}

// evaluateWithEditPatchFallback checks the requested permission and falls back to
// its edit/patch counterpart. An explicit same-tool rule wins; otherwise the
// counterpart's edit-family rule is inherited before wildcard rules are used.
func (rs Ruleset) evaluateWithEditPatchFallback(permission, pattern string) Action {
	normPerm := toolname.Normalize(permission)
	counterpart := getEditPatchCounterpart(normPerm)

	if counterpart != "" {
		if match, ok := rs.lastSpecificEditPatchToolMatch(normPerm, counterpart, pattern); ok {
			return match.Action
		}
	}

	if match := rs.LastEvaluatedMatch(permission, pattern); match.Found {
		return match.Rule.Action
	}

	return ActionDeny
}

// IsDisabled returns true if the tool is completely unavailable (deny with wildcard pattern).
// Used to exclude tools from the LLM's tool list entirely.
// Scans in reverse to find the last rule matching the tool name.
func (rs Ruleset) IsDisabled(toolName string) bool {
	toolName = toolname.Normalize(toolName)
	counterpart := getEditPatchCounterpart(toolName)

	if counterpart != "" {
		if match, ok := rs.lastSpecificEditPatchToolRule(toolName, counterpart); ok {
			return match.Pattern == "*" && match.Action == ActionDeny
		}
	}

	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if globMatch(toolName, toolname.Normalize(r.Permission)) {
			return r.Pattern == "*" && r.Action == ActionDeny
		}
	}
	return false
}

// DeniesAllWithPrefix reports whether the effective rules prove that every
// tool name under prefix is disabled. A later narrower allow/ask prevents that
// conclusion, which keeps undiscovered lazy tool namespaces reachable.
// Excluded prefixes represent nested namespaces owned by a more specific
// integration and are ignored when deciding whether this prefix has any
// reachable names of its own.
func (rs Ruleset) DeniesAllWithPrefix(prefix string, excludedPrefixes ...string) bool {
	prefix = toolname.Normalize(prefix)
	if prefix == "" {
		return false
	}
	excluded := normalizePermissionPrefixes(excludedPrefixes)
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		permissionPattern := toolname.Normalize(r.Permission)
		if permissionPatternCanMatchOwnedPrefix(permissionPattern, prefix, excluded) && r.Action != ActionDeny {
			return false
		}
		if r.Action == ActionDeny && r.Pattern == "*" && permissionPatternCoversPrefix(permissionPattern, prefix) {
			return true
		}
	}
	return false
}

func normalizePermissionPrefixes(prefixes []string) []string {
	normalized := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix = toolname.Normalize(prefix); prefix != "" {
			normalized = append(normalized, prefix)
		}
	}
	return normalized
}

func permissionPatternCanMatchOwnedPrefix(pattern, prefix string, excluded []string) bool {
	if !permissionPatternCanMatchPrefix(pattern, prefix) {
		return false
	}
	wildcard := strings.IndexAny(pattern, "*?")
	ownedPrefix := pattern
	if wildcard >= 0 {
		ownedPrefix = pattern[:wildcard]
	}
	for _, excludedPrefix := range excluded {
		if strings.HasPrefix(ownedPrefix, excludedPrefix) {
			return false
		}
	}
	return true
}

func permissionPatternCanMatchPrefix(pattern, prefix string) bool {
	if permissionPatternCoversPrefix(pattern, prefix) || (!strings.ContainsAny(pattern, "*?") && strings.HasPrefix(pattern, prefix)) {
		return true
	}
	wildcard := strings.IndexAny(pattern, "*?")
	if wildcard < 0 {
		return false
	}
	fixedPrefix := pattern[:wildcard]
	return strings.HasPrefix(prefix, fixedPrefix) || strings.HasPrefix(fixedPrefix, prefix)
}

func permissionPatternCoversPrefix(pattern, prefix string) bool {
	if strings.Contains(pattern, "?") {
		return false
	}
	fixed := strings.TrimRight(pattern, "*")
	if fixed == pattern || strings.Contains(fixed, "*") {
		return false
	}
	return strings.HasPrefix(prefix, fixed)
}

func (rs Ruleset) lastSpecificEditPatchToolRule(toolName, counterpart string) (Rule, bool) {
	var counterpartMatch Rule
	counterpartFound := false

	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		normRulePerm := toolname.Normalize(r.Permission)
		if normRulePerm == "*" {
			continue
		}
		if normRulePerm == toolName || globMatch(toolName, normRulePerm) {
			return r, true
		}
		if !counterpartFound && (normRulePerm == counterpart || globMatch(counterpart, normRulePerm)) {
			counterpartMatch = r
			counterpartFound = true
		}
	}
	return counterpartMatch, counterpartFound
}

func (rs Ruleset) lastSpecificEditPatchToolMatch(toolName, counterpart, pattern string) (Rule, bool) {
	var counterpartMatch Rule
	counterpartFound := false

	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		normRulePerm := toolname.Normalize(r.Permission)
		if normRulePerm == "*" {
			continue
		}
		if (normRulePerm == toolName || globMatch(toolName, normRulePerm)) && globMatch(pattern, r.Pattern) {
			return r, true
		}
		if !counterpartFound && (normRulePerm == counterpart || globMatch(counterpart, normRulePerm)) && globMatch(pattern, r.Pattern) {
			counterpartMatch = r
			counterpartFound = true
		}
	}
	return counterpartMatch, counterpartFound
}

// Merge concatenates rulesets. Later rulesets override earlier ones (findLast semantics).
func Merge(rulesets ...Ruleset) Ruleset {
	var merged Ruleset
	for _, rs := range rulesets {
		merged = append(merged, rs...)
	}
	return merged
}

// globCache caches compiled regex patterns for glob matching.
// This is a hot path (called on every tool invocation), so caching is important.
var globCache sync.Map // pattern string → *regexp.Regexp

func shellCompoundCommandNeedsReview(permission, command, rulePattern string) bool {
	if toolname.Normalize(permission) != toolname.Shell {
		return false
	}
	if strings.TrimSpace(rulePattern) == "*" {
		return false
	}
	return shellCommandContainsSeparator(command)
}

func shellCommandContainsSeparator(command string) bool {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(command); i++ {
		c := command[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && !inSingle {
			escaped = true
			continue
		}
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '\n', ';', '|', '&':
			if inSingle || inDouble {
				continue
			}
			return true
		}
	}
	return false
}

// globMatch matches a string against a glob pattern.
// Supports: * (any character sequence), ? (single character).
// Special case: "ls *" also matches "ls" — a trailing " *" is treated as optional.
// Uses compiled regex cache for performance.
func globMatch(str, pattern string) bool {
	re, ok := globCache.Load(pattern)
	if !ok {
		escaped := regexp.QuoteMeta(pattern)
		escaped = strings.ReplaceAll(escaped, `\*`, ".*")
		escaped = strings.ReplaceAll(escaped, `\?`, ".")
		// "ls .*" (from "ls *") should also match "ls" without args
		if strings.HasSuffix(escaped, " .*") {
			escaped = escaped[:len(escaped)-3] + "( .*)?"
		}
		compiled := regexp.MustCompile("(?s)^" + escaped + "$")
		globCache.Store(pattern, compiled)
		re = compiled
	}
	return re.(*regexp.Regexp).MatchString(str)
}
