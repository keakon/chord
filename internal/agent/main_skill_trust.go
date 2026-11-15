package agent

import (
	"github.com/keakon/chord/internal/skill"
)

// ProjectSkillTrustVerifier validates one skill's declaration files against
// the authorized project input set before its body is loaded from disk. It
// returns an error when a project-controlled declaration changed after the
// user authorized the project configuration; nil when the skill is not
// project-controlled or still matches.
type ProjectSkillTrustVerifier func(meta *skill.Meta) error

// SetProjectSkillTrustVerifier installs the verifier built at startup. It
// must be set before the event loop starts; a nil verifier (no authorized
// project inputs) disables the check.
func (a *MainAgent) SetProjectSkillTrustVerifier(verifier ProjectSkillTrustVerifier) {
	a.projectSkillTrustVerifier = verifier
}

// verifyProjectSkillTrust runs the installed verifier. It is a no-op when
// no verifier was installed or the skill has no metadata. SubAgents resolve
// it through their parent so the startup-authorized set stays the single
// source of truth.
func (a *MainAgent) verifyProjectSkillTrust(meta *skill.Meta) error {
	if a == nil || a.projectSkillTrustVerifier == nil || meta == nil {
		return nil
	}
	return a.projectSkillTrustVerifier(meta)
}
