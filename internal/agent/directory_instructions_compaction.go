package agent

import "github.com/keakon/chord/internal/message"

// retainDirectoryInstructionFacts retains the latest exact durable state for
// every checkout/scope, including tombstones and blocked candidates. Current
// tail facts supersede the snapshot captured before the atomic rewrite lock.
func retainDirectoryInstructionFacts(original, compacted []message.Message) []message.Message {
	type key struct{ root, scope string }
	latest := make(map[key]message.DirectoryInstructionFact)
	invalid := make([]message.Message, 0)
	for _, history := range [][]message.Message{original, compacted} {
		for _, m := range history {
			if m.Kind != message.KindDirectoryInstructions {
				continue
			}
			if !message.IsDirectoryInstructions(m) {
				invalid = append(invalid, m.Clone())
				continue
			}
			for _, f := range m.DirectoryInstructions.Facts {
				latest[key{f.CheckoutRoot, f.Scope}] = f
			}
		}
	}
	retained := make(map[key]bool)
	out := make([]message.Message, 0, len(compacted)+1)
	for _, m := range compacted {
		if m.Kind != message.KindDirectoryInstructions {
			out = append(out, m)
			continue
		}
		if !message.IsDirectoryInstructions(m) {
			continue
		}
		var facts []message.DirectoryInstructionFact
		for _, f := range m.DirectoryInstructions.Facts {
			k := key{f.CheckoutRoot, f.Scope}
			if !retained[k] && latest[k] == f {
				facts = append(facts, f)
				retained[k] = true
			}
		}
		if len(facts) > 0 {
			m = m.Clone()
			m.DirectoryInstructions.Facts = facts
			out = append(out, m)
		}
	}
	var missing []message.DirectoryInstructionFact
	for k, f := range latest {
		if !retained[k] {
			missing = append(missing, f)
		}
	}
	message.SortDirectoryInstructionFacts(missing)
	insert := 0
	for i, m := range out {
		if m.IsCompactionSummary {
			insert = i + 1
			break
		}
		if m.Role == message.RoleSystem {
			insert = i + 1
		}
	}
	extra := invalid
	if len(missing) > 0 {
		extra = append(extra, message.Message{Role: message.RoleUser, Kind: message.KindDirectoryInstructions, DirectoryInstructions: &message.DirectoryInstructions{Facts: missing}})
	}
	if len(extra) == 0 {
		return out
	}
	result := make([]message.Message, 0, len(out)+len(extra))
	result = append(result, out[:insert]...)
	result = append(result, extra...)
	result = append(result, out[insert:]...)
	return result
}
