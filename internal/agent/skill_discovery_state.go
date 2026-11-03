package agent

import (
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/skill"
)

// skillInvocationStatesFromHistory reports bodies from successful invocation
// names. Parked task snapshots strip instruction bodies by design and use the
// invocation record as their load-state evidence.
func skillInvocationStatesFromHistory(history []message.Message, view skillActivationView) []skill.InvocationState {
	bodies := make(map[string]struct{})
	for _, name := range invokedSkillNamesFromMessages(history) {
		bodies[name] = struct{}{}
	}
	return skillInvocationStatesWithBodies(history, view, bodies)
}

// skillInvocationStatesFromVisibleBodies reports a body as present only while
// a complete encoded body still survives the history window. A successful
// name-only envelope such as "already loaded" records the invocation but not
// the instruction text, so it must not paint the skill as loaded.
func skillInvocationStatesFromVisibleBodies(history []message.Message, view skillActivationView, currentBatch uint64, staleAge int) []skill.InvocationState {
	return skillInvocationStatesWithBodies(history, view, skillBodyNamesVisibleInMessages(history, currentBatch, staleAge))
}

func skillInvocationStatesWithBodies(history []message.Message, view skillActivationView, bodies map[string]struct{}) []skill.InvocationState {
	states := skill.InvocationStates(view.Catalog, view.Ruleset, bodies)
	earlier, _ := collectCheckpointSkillNames(history)
	loadedEarlier := make(map[string]bool)
	for _, name := range earlier {
		loadedEarlier[name] = true
	}
	activated := make(map[string]bool)
	for _, fact := range collectSkillActivationFacts(history, view) {
		for _, ref := range fact.Skills {
			activated[ref.Name] = true
		}
	}
	notices := make(map[string]bool)
	for _, msg := range projectSkillDiscoveryMessages(history, view) {
		if msg.SkillDiscovery != nil {
			for _, ref := range msg.SkillDiscovery.Skills {
				notices[ref.Name] = true
			}
		}
	}
	for i := range states {
		if states[i].Meta == nil {
			continue
		}
		name := states[i].Meta.Name
		states[i].LoadedEarlier = loadedEarlier[name]
		states[i].PathActivated = activated[name]
		states[i].NoticeRecorded = notices[name]
	}
	return states
}
