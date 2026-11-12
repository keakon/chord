package permission

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/toolname"
)

// RelaxCalls returns an execution-only copy. Asks pass without confirmation;
// scoped denies can relax only for tools whose tool-wide rule grants access.
// The decision is delayed until matching identifies the concrete tool: one
// tool glob can cover both granted and denied capabilities.
func (rs Ruleset) RelaxCalls() Ruleset {
	if len(rs) == 0 {
		return nil
	}
	relaxed := slices.Clone(rs)
	for i := range relaxed {
		switch relaxed[i].Action {
		case ActionAsk:
			relaxed[i].Action = ActionAllow
		case ActionDeny:
			relaxed[i].relaxScopedDeny = strings.TrimSpace(relaxed[i].Pattern) != "*"
		}
	}
	return relaxed
}

func (rs Ruleset) executionRule(tool string, rule Rule) Rule {
	if !rule.relaxScopedDeny {
		return rule
	}
	rule.relaxScopedDeny = false
	switch toolname.Normalize(tool) {
	case toolname.Delegate, toolname.Skill, toolname.Done, toolname.CompactContext:
		// These denies select catalog members or disable a control tool,
		// even when their argument pattern is narrower than "*".
		return rule
	}
	if action := rs.toolWideAction(tool); action == ActionAllow || action == ActionAsk {
		rule.Action = ActionAllow
	}
	return rule
}

func (rs Ruleset) toolWideAction(tool string) Action {
	// Filtering to universal argument rules preserves the engine's actual
	// same-tool / edit-family / wildcard precedence without treating a narrow
	// grant elsewhere as authorization for this call.
	wide := make(Ruleset, 0, len(rs))
	for _, rule := range rs {
		if rule.Pattern == "*" {
			wide = append(wide, rule)
		}
	}
	return wide.Evaluate(tool, "*")
}
