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
	Permission string // tool name pattern (e.g., "Bash", "Skill", "*")
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
//   - Mapping with sub-map:  "Bash: { rm *: deny }" → {Permission: "Bash", Pattern: "rm *", Action: deny}
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
		toolName := node.Content[i].Value
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
			// Bash: { "rm *": deny, "git *": allow }
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
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if globMatch(permission, r.Permission) && globMatch(pattern, r.Pattern) {
			return r.Action
		}
	}
	return ActionDeny // default deny if no rule matches
}

// IsDisabled returns true if the tool is completely unavailable (deny with wildcard pattern).
// Used to exclude tools from the LLM's tool list entirely.
// Scans in reverse to find the last rule matching the tool name.
func (rs Ruleset) IsDisabled(toolName string) bool {
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if globMatch(toolName, r.Permission) {
			return r.Pattern == "*" && r.Action == ActionDeny
		}
	}
	return false
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
