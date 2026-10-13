package agent

import "github.com/keakon/chord/internal/message"

func buildSkillActivationCarry(history []message.Message, view skillActivationView, nextEpoch uint64) *message.SkillActivationCarry {
	facts := collectSkillActivationFacts(history, view)
	latest := make(map[string]int)
	for i, f := range facts {
		for _, ref := range f.Skills {
			latest[skillRefKey(ref)] = i
		}
	}
	carry := &message.SkillActivationCarry{TaskID: view.TaskID, CheckoutRoot: view.CheckoutRoot, ContextEpoch: nextEpoch}
	for i, f := range facts {
		refs := make([]message.SkillActivationRef, 0, len(f.Skills))
		for _, ref := range f.Skills {
			if latest[skillRefKey(ref)] == i {
				refs = append(refs, ref)
			}
		}
		if len(refs) > 0 {
			f.Skills = refs
			carry.Facts = append(carry.Facts, f)
		}
	}
	return carry
}
