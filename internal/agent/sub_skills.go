package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) visibleSkillsSnapshot() []*skill.Meta {
	if s == nil {
		return nil
	}
	catalog := s.loadedSkills
	var ruleset permission.Ruleset
	if s.parent != nil {
		catalog = s.parent.loadedSkillsSnapshot()
		s.parent.stateMu.RLock()
		cfg := s.parent.agentConfigs[s.agentDefName]
		s.parent.stateMu.RUnlock()
		if cfg == nil {
			return nil
		}
		ruleset = s.parent.buildSubAgentRuleset(cfg)
	} else {
		ruleset = s.ruleset
	}
	if len(catalog) == 0 {
		return nil
	}
	out := make([]*skill.Meta, 0, len(catalog))
	for _, meta := range catalog {
		if meta == nil || strings.TrimSpace(meta.Name) == "" {
			continue
		}
		copyMeta := *meta
		copyMeta.Discovered = true
		if len(ruleset) > 0 && ruleset.Evaluate(tools.NameSkill, copyMeta.Name) == permission.ActionDeny {
			copyMeta.Discovered = false
		}
		if copyMeta.Discovered {
			out = append(out, &copyMeta)
		}
	}
	return out
}

func (s *SubAgent) availableSkillsPromptBlock() string {
	skills := s.visibleSkillsSnapshot()
	if len(skills) == 0 {
		return ""
	}
	entries := make([]tools.SkillListingEntry, 0, len(skills))
	for _, sk := range skills {
		if sk == nil {
			continue
		}
		entries = append(entries, tools.SkillListingEntry{Name: sk.Name, Desc: sk.Description})
	}
	if len(entries) == 0 {
		return ""
	}
	header := "## Available Skills\nThe `skill` tool can load additional skill instructions on demand. When a task clearly matches one of these skills, call `skill` before proceeding.\n\n"
	return tools.BuildSkillListing(entries, header)
}

func (s *SubAgent) ListSkills() []*skill.Meta {
	return s.visibleSkillsSnapshot()
}

func (s *SubAgent) InvokedSkills() []*skill.Meta {
	if s == nil {
		return nil
	}
	visible := make(map[string]*skill.Meta)
	for _, meta := range s.visibleSkillsSnapshot() {
		if meta != nil {
			visible[meta.Name] = meta
		}
	}
	s.skillsMu.RLock()
	out := make([]*skill.Meta, 0, len(s.invokedSkills))
	for name := range s.invokedSkills {
		if meta, ok := visible[name]; ok {
			copyMeta := *meta
			copyMeta.Invoked = true
			out = append(out, &copyMeta)
		}
	}
	s.skillsMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *SubAgent) MarkSkillInvoked(meta *skill.Meta) {
	if s == nil || meta == nil || strings.TrimSpace(meta.Name) == "" {
		return
	}
	copyMeta := *meta
	copyMeta.Invoked = true
	s.skillsMu.Lock()
	if s.invokedSkills == nil {
		s.invokedSkills = make(map[string]*skill.Meta)
	}
	s.invokedSkills[copyMeta.Name] = &copyMeta
	s.skillsMu.Unlock()
}

func (s *SubAgent) invokedSkillNamesSnapshot() []string {
	if s == nil {
		return nil
	}
	s.skillsMu.RLock()
	names := make([]string, 0, len(s.invokedSkills))
	for name := range s.invokedSkills {
		names = append(names, name)
	}
	s.skillsMu.RUnlock()
	sort.Strings(names)
	return names
}

func (s *SubAgent) restoreInvokedSkills(msgs []message.Message) {
	invoked := rebuildInvokedSkillsFromMessages(msgs, s.visibleSkillsSnapshot())
	s.skillsMu.Lock()
	s.invokedSkills = make(map[string]*skill.Meta, len(invoked))
	for _, meta := range invoked {
		if meta != nil {
			s.invokedSkills[meta.Name] = meta
		}
	}
	s.skillsMu.Unlock()
}

func (s *SubAgent) LoadSkill(name string) (*skill.Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("skill name is required")
	}
	for _, meta := range s.visibleSkillsSnapshot() {
		if meta == nil || meta.Name != name {
			continue
		}
		return skill.LoadSkill(meta.Location)
	}
	return nil, fmt.Errorf("skill %q not found", name)
}
