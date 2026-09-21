package skill

import (
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/toolname"
)

// VisibleUnderRuleset reports whether one skill survives the ruleset filter.
// It is the single source of truth for the visibility predicate: the MainAgent
// runtime filters the model-facing list through it and the doctor diagnostics
// report the same verdict per skill, so a rule change can never leave the two
// sides disagreeing about what the model may see.
func VisibleUnderRuleset(meta *Meta, ruleset permission.Ruleset) bool {
	if meta == nil {
		return false
	}
	return !(len(ruleset) > 0 && ruleset.Evaluate(toolname.Skill, meta.Name) == permission.ActionDeny)
}

// VisibleForRuleset returns the subset of loaded skills visible under a
// target ruleset. It is a stateless pure function shared by the MainAgent
// runtime and the doctor diagnostics so both sides filter identically:
// a skill denied by the ruleset stays loadable in isolation but is hidden
// from the model-facing list.
func VisibleForRuleset(loaded []*Meta, ruleset permission.Ruleset) []*Meta {
	out := make([]*Meta, 0, len(loaded))
	for _, meta := range loaded {
		if !VisibleUnderRuleset(meta, ruleset) {
			continue
		}
		copyMeta := *meta
		copyMeta.Discovered = true
		out = append(out, &copyMeta)
	}
	return out
}
