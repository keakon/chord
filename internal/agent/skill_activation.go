package agent

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/toolname"
)

type skillActivationView struct {
	TaskID, CheckoutRoot string
	Catalog              []*skill.Meta
	Ruleset              permission.Ruleset
}

func (v skillActivationView) validRef(ref message.SkillActivationRef) bool {
	for _, meta := range v.Catalog {
		if meta == nil || meta.Name != ref.Name || !skill.ModelCallableUnderRuleset(meta, v.Ruleset) {
			continue
		}
		d := skill.PathActivationDescriptor(meta)
		return d.Conditional && d.Diagnostic == "" && d.Identity == ref.Identity && d.Revision == ref.Revision
	}
	return false
}
func skillRefKey(ref message.SkillActivationRef) string { return ref.Identity + ":" + ref.Revision }

func matchSkillActivationPaths(paths []string, view skillActivationView) []message.SkillActivationRef {
	if view.CheckoutRoot == "" || view.TaskID == "" {
		return nil
	}
	var relative []string
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		rel, err := filepath.Rel(view.CheckoutRoot, path)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		relative = append(relative, filepath.ToSlash(rel))
	}
	var out []message.SkillActivationRef
	for _, meta := range view.Catalog {
		if !skill.ModelCallableUnderRuleset(meta, view.Ruleset) {
			continue
		}
		d := skill.PathActivationDescriptor(meta)
		if slices.ContainsFunc(relative, d.Match) {
			out = append(out, message.SkillActivationRef{Identity: d.Identity, Revision: d.Revision, Name: meta.Name})
		}
	}
	return out
}

func skillActivationEpoch(history []message.Message, taskID string) uint64 {
	var epoch uint64
	for _, msg := range history {
		if c := msg.CompactionSkillActivation; c != nil && c.TaskID == taskID {
			epoch = max(epoch, c.ContextEpoch)
		}
		if f := msg.SkillActivation; f != nil && f.TaskID == taskID {
			epoch = max(epoch, f.ContextEpoch)
		}
		if n := msg.SkillDiscovery; n != nil && n.TaskID == taskID {
			epoch = max(epoch, n.ContextEpoch)
		}
	}
	return epoch
}

// collectSkillActivationFacts only accepts paired successful local read results,
// runtime carries and validated notices. Body text never establishes activation.
func collectSkillActivationFacts(history []message.Message, view skillActivationView) []message.SkillActivationFact {
	calls := make(map[string]string)
	var out []message.SkillActivationFact
	observed := make(map[string]bool)
	add := func(f message.SkillActivationFact) {
		if f.TaskID != view.TaskID || f.CheckoutRoot != view.CheckoutRoot {
			return
		}
		filtered := make([]message.SkillActivationRef, 0, len(f.Skills))
		for _, ref := range f.Skills {
			if view.validRef(ref) {
				filtered = append(filtered, ref)
				observed[skillRefKey(ref)] = true
			}
		}
		if len(filtered) > 0 {
			f.Skills = filtered
			out = append(out, f)
		}
	}
	for _, msg := range history {
		if msg.Role == message.RoleAssistant {
			for _, call := range msg.ToolCalls {
				calls[call.ID] = call.Name
			}
		}
		if c := msg.CompactionSkillActivation; c != nil && c.TaskID == view.TaskID && c.CheckoutRoot == view.CheckoutRoot {
			for _, f := range c.Facts {
				add(f)
			}
		}
		if f := msg.SkillActivation; f != nil && msg.Role == message.RoleTool && msg.ToolStatus == message.ToolStatusSuccess && f.SourceCallID == msg.ToolCallID && calls[msg.ToolCallID] == toolname.Read {
			add(*f)
		}
		if validSkillDiscoveryMessage(msg, view) {
			n := msg.SkillDiscovery
			// A retained notice can restore an archived read, but must not make an
			// older actual read appear newer merely because it was advertised later.
			var refs []message.SkillActivationRef
			for _, ref := range n.Skills {
				if !observed[skillRefKey(ref)] {
					refs = append(refs, ref)
				}
			}
			if len(refs) > 0 {
				add(message.SkillActivationFact{TaskID: n.TaskID, CheckoutRoot: n.CheckoutRoot, ContextEpoch: n.ContextEpoch, Skills: refs})
			}
		}
	}
	return out
}

func buildSkillActivationFact(history []message.Message, view skillActivationView, sourceCallID string, candidates []message.SkillActivationRef) *message.SkillActivationFact {
	epoch := skillActivationEpoch(history, view.TaskID)
	seen := make(map[string]bool)
	for _, fact := range collectSkillActivationFacts(history, view) {
		if fact.ContextEpoch == epoch {
			for _, ref := range fact.Skills {
				seen[skillRefKey(ref)] = true
			}
		}
	}
	var refs []message.SkillActivationRef
	for _, ref := range candidates {
		key := skillRefKey(ref)
		if !seen[key] && view.validRef(ref) {
			refs = append(refs, ref)
			seen[key] = true
		}
	}
	if len(refs) == 0 || sourceCallID == "" {
		return nil
	}
	return &message.SkillActivationFact{TaskID: view.TaskID, CheckoutRoot: view.CheckoutRoot, SourceCallID: sourceCallID, ContextEpoch: epoch, Skills: refs}
}
