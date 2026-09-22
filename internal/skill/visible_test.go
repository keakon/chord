package skill

import (
	"testing"

	"github.com/keakon/chord/internal/permission"
)

func allowAllRuleset() permission.Ruleset {
	return permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
}

func TestModelInvocableRequiresMetaAndFrontmatterOptIn(t *testing.T) {
	if ModelInvocable(nil) {
		t.Fatal("nil meta must not be model-invocable")
	}
	if !ModelInvocable(&Meta{Name: "plain"}) {
		t.Fatal("a skill without the flag must stay model-invocable")
	}
	if ModelInvocable(&Meta{Name: "manual", DisableModelInvocation: true}) {
		t.Fatal("disable-model-invocation must keep the skill out of the model catalog")
	}
}

func TestModelVisibleForRulesetDropsManualOnlySkills(t *testing.T) {
	catalog := []*Meta{
		{Name: "visible"},
		{Name: "manual", DisableModelInvocation: true},
		{Name: "denied"},
		nil,
	}
	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "skill", Pattern: "denied", Action: permission.ActionDeny},
	}

	model := ModelVisibleForRuleset(catalog, ruleset)
	if len(model) != 1 || model[0].Name != "visible" {
		t.Fatalf("ModelVisibleForRuleset = %+v, want only visible", model)
	}
	if !model[0].Discovered {
		t.Fatal("a returned entry must be marked discovered")
	}

	// The user-facing filter keeps the manual-only skill and drops only the
	// ruleset-denied one, so the selector can offer an explicit load.
	user := VisibleForRuleset(catalog, ruleset)
	if len(user) != 2 || user[0].Name != "visible" || user[1].Name != "manual" {
		t.Fatalf("VisibleForRuleset = %+v, want visible and manual", user)
	}
}

func TestInvocationStatesReportReasonAndLoad(t *testing.T) {
	catalog := []*Meta{
		{Name: "model-skill"},
		{Name: "manual-skill", DisableModelInvocation: true},
		{Name: "denied-skill"},
	}
	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "skill", Pattern: "denied-skill", Action: permission.ActionDeny},
	}
	states := InvocationStates(catalog, ruleset, map[string]struct{}{"manual-skill": {}})

	cases := []struct {
		name             string
		wantModelVisible bool
		wantUserLoadable bool
		wantLoaded       bool
		wantReason       string
	}{
		{name: "model-skill", wantModelVisible: true, wantUserLoadable: true},
		{name: "manual-skill", wantUserLoadable: true, wantLoaded: true, wantReason: ReasonManualOnly},
		{name: "denied-skill", wantReason: ReasonDeniedByRuleset},
	}
	if len(states) != len(cases) {
		t.Fatalf("len(states) = %d, want %d", len(states), len(cases))
	}
	for i, tc := range cases {
		st := states[i]
		if st.Meta == nil || st.Meta.Name != tc.name {
			t.Fatalf("state %d meta = %+v, want %q", i, st.Meta, tc.name)
		}
		if st.ModelVisible != tc.wantModelVisible || st.UserLoadable != tc.wantUserLoadable || st.Loaded != tc.wantLoaded || st.Reason != tc.wantReason {
			t.Fatalf("state %q = %+v, want model=%v user=%v loaded=%v reason=%q",
				tc.name, st, tc.wantModelVisible, tc.wantUserLoadable, tc.wantLoaded, tc.wantReason)
		}
	}
}

func TestInvocationStateUnavailableForNilMeta(t *testing.T) {
	st := InvocationStateFor(nil, allowAllRuleset(), nil)
	if st.Meta != nil || st.UserLoadable || st.ModelVisible || st.Reason != ReasonUnavailable {
		t.Fatalf("nil meta state = %+v, want unavailable", st)
	}
}
