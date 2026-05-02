package agent

import (
	"fmt"
	"github.com/keakon/golog/log"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

func (a *MainAgent) SetSkills(skills []*skill.Meta) {
	a.skillsMu.Lock()
	a.loadedSkills = append([]*skill.Meta(nil), skills...)
	a.skillsMu.Unlock()
	a.MarkSkillsReady()

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
	loaded := a.loadedSkillsSnapshot()
	if len(loaded) == 0 {
		return nil
	}
	ruleset := a.effectiveRuleset()
	out := make([]*skill.Meta, 0, len(loaded))
	for _, meta := range loaded {
		if meta == nil {
			continue
		}
		copyMeta := *meta
		copyMeta.Discovered = true
		if len(ruleset) > 0 && ruleset.Evaluate("Skill", meta.Name) == permission.ActionDeny {
			copyMeta.Discovered = false
		}
		if copyMeta.Discovered {
			out = append(out, &copyMeta)
		}
	}
	return out
}

func (a *MainAgent) ListSkills() []*skill.Meta { return a.visibleSkillsSnapshot() }

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

func (a *MainAgent) InvokedSkills() []*skill.Meta {
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
