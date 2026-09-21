package agent

import (
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
)

func TestVisibleSkillsForRulesetMatchesSharedHelper(t *testing.T) {
	loaded := []*skill.Meta{{Name: "open-skill"}, {Name: "closed-skill"}, nil}
	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "skill", Pattern: "closed-skill", Action: permission.ActionDeny},
	}
	got := visibleSkillsForRuleset(loaded, ruleset)
	want := skill.VisibleForRuleset(loaded, ruleset)
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Name != want[i].Name || got[i].Discovered != want[i].Discovered {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
