package skill

import (
	"slices"

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
	return filterVisibleSkills(loaded, func(meta *Meta) bool {
		return VisibleUnderRuleset(meta, ruleset)
	})
}

// ModelInvocable reports whether the model may load this skill on its own. A
// skill whose frontmatter declares `disable-model-invocation: true` may only be
// loaded through the user's explicit request.
func ModelInvocable(meta *Meta) bool {
	return meta != nil && !meta.DisableModelInvocation
}

// ModelCallableUnderRuleset is the single model loading permission predicate.
// Path activation controls recommendations and never grants loading access.
func ModelCallableUnderRuleset(meta *Meta, ruleset permission.Ruleset) bool {
	return ModelInvocable(meta) && VisibleUnderRuleset(meta, ruleset)
}

// ModelCallableForRuleset returns every model-loadable skill, including skills
// that have not matched a path and are absent from the startup listing.
func ModelCallableForRuleset(loaded []*Meta, ruleset permission.Ruleset) []*Meta {
	return filterVisibleSkills(loaded, func(meta *Meta) bool {
		return ModelCallableUnderRuleset(meta, ruleset)
	})
}

func filterVisibleSkills(loaded []*Meta, allow func(*Meta) bool) []*Meta {
	out := make([]*Meta, 0, len(loaded))
	for _, meta := range loaded {
		if !allow(meta) {
			continue
		}
		copyMeta := *meta
		copyMeta.Paths = slices.Clone(meta.Paths)
		copyMeta.AllowedTools = slices.Clone(meta.AllowedTools)
		copyMeta.Resources = slices.Clone(meta.Resources)
		copyMeta.Discovered = true
		out = append(out, &copyMeta)
	}
	return out
}

// Reason values carried by InvocationState.Reason. They are machine-readable:
// the TUI selector renders them as user-facing text and the doctor/log paths
// can key off the same vocabulary.
const (
	// ReasonNotFound marks a name absent from the catalog.
	ReasonNotFound = "not_found"
	// ReasonDeniedByRuleset marks an entry the role's ruleset hides from the
	// model and refuses the user's explicit load.
	ReasonDeniedByRuleset = "denied_by_ruleset"
	// ReasonManualOnly marks an entry the frontmatter keeps out of the model
	// catalog while the user may still load it.
	ReasonManualOnly = "manual_only"
	// ReasonUnavailable marks an entry the agent cannot load at all, for
	// example a skill file that disappeared after discovery.
	ReasonUnavailable = "unavailable"
)

// InvocationState separates invocation permission, discovery and body availability.
type InvocationState struct {
	Meta            *Meta
	ModelCallable   bool
	UserLoadable    bool
	BodyPresent     bool
	LoadedEarlier   bool
	PathConditional bool
	PathActivated   bool
	NoticeRecorded  bool
	Reason          string
}

// InvocationStateFor builds one entry's state. loaded is the set of skill
// names whose instructions are currently in the agent's context (nil when
// nothing is loaded).
func InvocationStateFor(meta *Meta, ruleset permission.Ruleset, loaded map[string]struct{}) InvocationState {
	if meta == nil {
		return InvocationState{Reason: ReasonUnavailable}
	}
	state := InvocationState{
		Meta:            meta,
		PathConditional: BuildPathDescriptor(meta).Conditional,
		UserLoadable:    VisibleUnderRuleset(meta, ruleset),
		ModelCallable:   ModelCallableUnderRuleset(meta, ruleset),
	}
	switch {
	case !state.UserLoadable:
		state.Reason = ReasonDeniedByRuleset
	case !state.ModelCallable:
		state.Reason = ReasonManualOnly
	}
	if _, ok := loaded[meta.Name]; ok {
		state.BodyPresent = true
	}
	return state
}

// InvocationStates builds the states of a whole catalog, preserving order.
// The catalog is the complete discovered set: entries the ruleset denies are
// reported with ReasonDeniedByRuleset rather than dropped, because the user
// selector explains them while the sidebar chooses what to render.
func InvocationStates(catalog []*Meta, ruleset permission.Ruleset, loaded map[string]struct{}) []InvocationState {
	out := make([]InvocationState, 0, len(catalog))
	for _, meta := range catalog {
		out = append(out, InvocationStateFor(meta, ruleset, loaded))
	}
	return out
}
