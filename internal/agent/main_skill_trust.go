package agent

import (
	"github.com/keakon/chord/internal/skill"
)

// ProjectSkillLoader returns a skill parsed from the declaration bytes that
// were checked against the authorized project input set.
type ProjectSkillLoader func(meta *skill.Meta) (*skill.Skill, error)

// SetProjectSkillLoader installs the loader before the event loop starts.
func (a *MainAgent) SetProjectSkillLoader(loader ProjectSkillLoader) {
	a.projectSkillLoader = loader
}

func (a *MainAgent) loadProjectSkill(meta *skill.Meta) (*skill.Skill, error) {
	if a == nil || a.projectSkillLoader == nil {
		return skill.LoadSkill(meta.Location)
	}
	return a.projectSkillLoader(meta)
}
