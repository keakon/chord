package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

func (a *MainAgent) SetSkills(skills []*skill.Meta) {
	a.skillsMu.Lock()
	a.loadedSkills = append([]*skill.Meta(nil), skills...)
	a.skillsMu.Unlock()
	a.MarkSkillsReady()
	// The catalog is part of the LLM surface: the Available Skills block lives
	// in the system prompt, and the skill tool lists the same entries. Marking
	// the surface dirty lets the next request compare and rebuild it, so a
	// refreshed catalog (a worktree switch, a session switch) is visible
	// instead of waiting for the next session-head reset.
	a.markRuntimeSurfaceDirty()

	if len(skills) > 0 {
		names := make([]string, len(skills))
		for i, s := range skills {
			names[i] = s.Name
		}
		log.Debugf("skills discovered count=%v names=%v", len(skills), names)
	}
	a.skillsMu.Lock()
	for name, meta := range a.invokedSkills {
		if meta == nil {
			delete(a.invokedSkills, name)
			continue
		}
		meta.Discovered = false
		for _, discovered := range a.loadedSkills {
			if discovered != nil && discovered.Name == name {
				meta.Location = discovered.Location
				meta.RootDir = discovered.RootDir
				meta.Description = discovered.Description
				meta.Discovered = true
				break
			}
		}
	}
	a.skillsMu.Unlock()
}

func (a *MainAgent) visibleSkillsSnapshot() []*skill.Meta {
	return visibleSkillsForRuleset(a.loadedSkillsSnapshot(), a.effectiveRuleset())
}

func (a *MainAgent) ListSkills() []*skill.Meta { return a.visibleSkillsSnapshot() }

// FocusedSkills returns skills visible to the agent currently shown by the TUI.
// ListSkills remains scoped to MainAgent so the runtime SkillProvider semantics
// do not change when the user switches sidebar focus.
func (a *MainAgent) FocusedSkills() []*skill.Meta {
	target := a.focusedAgentSnapshot()
	if target.sub != nil {
		return target.sub.ListSkills()
	}
	if (target.parked || target.settled) && target.task != nil {
		return a.parkedTaskVisibleSkills(target.task)
	}
	return a.ListSkills()
}

func (a *MainAgent) parkedTaskVisibleSkills(task *DurableTaskRecord) []*skill.Meta {
	if task == nil {
		return nil
	}
	a.stateMu.RLock()
	cfg := a.agentConfigs[task.AgentDefName]
	a.stateMu.RUnlock()
	if cfg == nil {
		return nil
	}
	return visibleSkillsForRuleset(a.loadedSkillsSnapshot(), a.buildSubAgentRuleset(cfg))
}

func visibleSkillsForRuleset(loaded []*skill.Meta, ruleset permission.Ruleset) []*skill.Meta {
	return skill.VisibleForRuleset(loaded, ruleset)
}

func (a *MainAgent) MarkSkillInvoked(meta *skill.Meta) {
	if meta == nil || strings.TrimSpace(meta.Name) == "" {
		return
	}
	a.skillsMu.Lock()
	if a.invokedSkills == nil {
		a.invokedSkills = make(map[string]*skill.Meta)
	}
	copyMeta := *meta
	copyMeta.Invoked = true
	copyMeta.Discovered = true
	a.invokedSkills[copyMeta.Name] = &copyMeta
	a.skillsMu.Unlock()
}

// resetInvokedSkillsFromMessages recomputes the invoked-skill state from the
// messages that are actually in context. A skill's instructions live only in
// its tool result, so a durable compaction that archives the head takes them
// out of the context entirely — the state must stop reporting those skills as
// invoked, exactly as it already does after a restart, where session restore
// rebuilds this map from the same messages. Without it the sidebar keeps
// asserting the model is following a workflow it can no longer see, and the
// same session reports different skills before and after a reload.
func (a *MainAgent) resetInvokedSkillsFromMessages(msgs []message.Message) {
	invoked := rebuildInvokedSkillsFromMessages(msgs, a.visibleSkillsSnapshot())
	a.skillsMu.Lock()
	a.invokedSkills = make(map[string]*skill.Meta, len(invoked))
	for _, meta := range invoked {
		if meta == nil {
			continue
		}
		a.invokedSkills[meta.Name] = meta
	}
	a.skillsMu.Unlock()
}

func (a *MainAgent) InvokedSkills() []*skill.Meta {
	target := a.focusedAgentSnapshot()
	if target.sub != nil {
		return target.sub.InvokedSkills()
	}
	if (target.parked || target.settled) && target.task != nil {
		visible := make(map[string]*skill.Meta)
		for _, meta := range a.parkedTaskVisibleSkills(target.task) {
			if meta != nil {
				visible[meta.Name] = meta
			}
		}
		out := make([]*skill.Meta, 0, len(target.task.InvokedSkillNames))
		for _, name := range target.task.InvokedSkillNames {
			if meta, ok := visible[name]; ok {
				copyMeta := *meta
				copyMeta.Invoked = true
				out = append(out, &copyMeta)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
	return a.invokedSkillsSnapshot()
}

func (a *MainAgent) invokedSkillsSnapshot() []*skill.Meta {
	a.skillsMu.RLock()
	defer a.skillsMu.RUnlock()
	if len(a.invokedSkills) == 0 {
		return nil
	}
	out := make([]*skill.Meta, 0, len(a.invokedSkills))
	for _, meta := range a.invokedSkills {
		if meta == nil {
			continue
		}
		copyMeta := *meta
		copyMeta.Invoked = true
		out = append(out, &copyMeta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (a *MainAgent) LoadSkill(name string) (*skill.Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("skill name is required")
	}
	for _, meta := range a.visibleSkillsSnapshot() {
		if meta.Name != name {
			continue
		}
		return skill.LoadSkill(meta.Location)
	}
	return nil, fmt.Errorf("skill %q not found", name)
}

func (a *MainAgent) MarkSkillInvokedByName(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	for _, meta := range a.visibleSkillsSnapshot() {
		if meta.Name == name {
			a.MarkSkillInvoked(meta)
			return
		}
	}
	a.skillsMu.Lock()
	if a.invokedSkills == nil {
		a.invokedSkills = make(map[string]*skill.Meta)
	}
	if existing, ok := a.invokedSkills[name]; ok && existing != nil {
		existing.Invoked = true
		a.skillsMu.Unlock()
		return
	}
	a.invokedSkills[name] = &skill.Meta{Name: name, Invoked: true}
	a.skillsMu.Unlock()
}

func (a *MainAgent) loadedSkillsSnapshot() []*skill.Meta {
	a.skillsMu.RLock()
	defer a.skillsMu.RUnlock()
	if len(a.loadedSkills) == 0 {
		return nil
	}
	out := make([]*skill.Meta, len(a.loadedSkills))
	copy(out, a.loadedSkills)
	return out
}
