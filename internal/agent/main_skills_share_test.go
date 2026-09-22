package agent

import (
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

// TestSkillVisibilityWrappersMatchSharedHelpers pins the two MainAgent
// wrappers to the shared skill package filters: the user-facing snapshot keeps
// manual-only skills, while the model-facing snapshot drops them. A divergence
// would let the sidebar and the Available Skills block disagree about what a
// skill can be loaded by.
func TestSkillVisibilityWrappersMatchSharedHelpers(t *testing.T) {
	loaded := []*skill.Meta{
		{Name: "open-skill"},
		{Name: "closed-skill"},
		{Name: "manual-skill", DisableModelInvocation: true},
		nil,
	}
	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "skill", Pattern: "closed-skill", Action: permission.ActionDeny},
	}

	assertMatches := func(t *testing.T, label string, got, want []*skill.Meta) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s len = %d, want %d", label, len(got), len(want))
		}
		for i := range got {
			if got[i].Name != want[i].Name || got[i].Discovered != want[i].Discovered {
				t.Fatalf("%s row %d = %+v, want %+v", label, i, got[i], want[i])
			}
		}
	}

	assertMatches(t, "userVisibleSkillsForRuleset", userVisibleSkillsForRuleset(loaded, ruleset), skill.VisibleForRuleset(loaded, ruleset))
	assertMatches(t, "modelVisibleSkillsForRuleset", modelVisibleSkillsForRuleset(loaded, ruleset), skill.ModelVisibleForRuleset(loaded, ruleset))
}
