package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) visibleSkillsSnapshot() []*skill.Meta {
	if s == nil || len(s.loadedSkills) == 0 {
		return nil
	}
	out := make([]*skill.Meta, 0, len(s.loadedSkills))
	for _, meta := range s.loadedSkills {
		if meta == nil || strings.TrimSpace(meta.Name) == "" {
			continue
		}
		copyMeta := *meta
		copyMeta.Discovered = true
		if len(s.ruleset) > 0 && s.ruleset.Evaluate("Skill", copyMeta.Name) == permission.ActionDeny {
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
	header := "## Available Skills\nThe `Skill` tool can load additional skill instructions on demand. When a task clearly matches one of these skills, call `Skill` before proceeding.\n\n"
	return tools.BuildSkillListing(entries, header)
}

func (s *SubAgent) ListSkills() []*skill.Meta {
	return s.visibleSkillsSnapshot()
}

func (s *SubAgent) InvokedSkills() []*skill.Meta {
	if s == nil || s.parent == nil {
		return nil
	}
	return s.parent.InvokedSkills()
}

func (s *SubAgent) MarkSkillInvoked(meta *skill.Meta) {
	if s == nil || s.parent == nil || meta == nil {
		return
	}
	s.parent.MarkSkillInvoked(meta)
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
