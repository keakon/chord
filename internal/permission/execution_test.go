package permission

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/keakon/chord/internal/toolname"
)

func TestRelaxCallsResolvesConcreteTool(t *testing.T) {
	raw := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: toolname.Read, Pattern: "*", Action: ActionAsk},
		{Permission: "*", Pattern: "private/*", Action: ActionDeny},
	}
	before := slices.Clone(raw)
	relaxed := raw.RelaxCalls()
	for _, scope := range []PathScope{{}, testScope} {
		if got := relaxed.EvaluatePath(toolname.Read, "private/notes.txt", scope); got != ActionAllow {
			t.Fatalf("read with scope %+v = %s, want allow", scope, got)
		}
		if got := relaxed.EvaluatePath(toolname.Write, "private/notes.txt", scope); got != ActionDeny {
			t.Fatalf("write with scope %+v = %s, want deny", scope, got)
		}
	}
	if !slices.Equal(raw, before) || raw.Evaluate(toolname.Read, "private/notes.txt") != ActionDeny {
		t.Fatal("execution copy changed configured rules")
	}
	for _, tool := range []string{toolname.Delegate, toolname.Skill, toolname.Done, toolname.CompactContext} {
		rules := Ruleset{
			{Permission: "*", Pattern: "*", Action: ActionAllow},
			{Permission: "*", Pattern: "sample", Action: ActionDeny},
		}
		if got := rules.RelaxCalls().Evaluate(tool, "sample"); got != ActionDeny {
			t.Errorf("%s membership/control deny = %s, want deny", tool, got)
		}
	}
}

func TestRelaxCallsEditFamilyPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules Ruleset
		want  Action
	}{
		{
			name: "counterpart grants full scope",
			rules: Ruleset{
				{Permission: toolname.Edit, Pattern: "*", Action: ActionAllow},
				{Permission: toolname.ApplyPatch, Pattern: "private/*", Action: ActionDeny},
			},
			want: ActionAllow,
		},
		{
			name: "same tool deny beats counterpart grant",
			rules: Ruleset{
				{Permission: toolname.ApplyPatch, Pattern: "*", Action: ActionDeny},
				{Permission: toolname.Edit, Pattern: "*", Action: ActionAllow},
				{Permission: toolname.ApplyPatch, Pattern: "private/*", Action: ActionDeny},
			},
			want: ActionDeny,
		},
		{
			name: "counterpart deny beats global grant",
			rules: Ruleset{
				{Permission: toolname.Edit, Pattern: "*", Action: ActionDeny},
				{Permission: "*", Pattern: "*", Action: ActionAllow},
				{Permission: "patch", Pattern: "private/*", Action: ActionDeny},
			},
			want: ActionDeny,
		},
		{
			name: "same tool grant beats counterpart deny",
			rules: Ruleset{
				{Permission: "patch", Pattern: "*", Action: ActionAllow},
				{Permission: toolname.Edit, Pattern: "*", Action: ActionDeny},
				{Permission: toolname.ApplyPatch, Pattern: "private/*", Action: ActionDeny},
			},
			want: ActionAllow,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := tc.rules.RelaxCalls()
			for _, scope := range []PathScope{{}, testScope} {
				if got := rules.EvaluatePath(toolname.ApplyPatch, "private/notes.txt", scope); got != tc.want {
					t.Errorf("scope %+v: action = %s, want %s", scope, got, tc.want)
				}
			}
		})
	}
}

func TestRelaxCallsPreservesPathAndNetworkMatching(t *testing.T) {
	raw := Ruleset{
		{Permission: "*", Pattern: "*", Action: ActionDeny},
		{Permission: "r*", Pattern: "*", Action: ActionAllow},
		{Permission: toolname.Read, Pattern: "private/*", Action: ActionDeny},
		{Permission: toolname.Read, Pattern: "../external/*", Action: ActionAsk},
		{Permission: "web_*", Pattern: "*", Action: ActionAsk},
		{Permission: toolname.WebFetch, Pattern: "192.0.2.0/24:443", Action: ActionDeny},
	}
	rules := raw.RelaxCalls()
	path := filepath.Join(testCWD, "private/notes.txt")
	for _, match := range []MatchResult{
		rules.LastSpecificToolMatchPath(toolname.Read, path, testScope),
		rules.LastSpecificMatchPath(toolname.Read, path, testScope),
		rules.LastNamedPathMatch(toolname.Read, path, testScope),
		rules.LastNamedPathMatch(toolname.Read, "private/notes.txt", PathScope{}),
		rules.EvaluateWebFetch("https://192.0.2.1/"),
	} {
		if !match.Found || match.Rule.Action != ActionAllow {
			t.Errorf("match = %+v, want allow", match)
		}
	}
	if got := rules.EvaluatePath(toolname.Write, "../external/notes.txt", testScope); got != ActionDeny {
		t.Fatalf("ungranted external write = %s, want deny", got)
	}
}

func TestRelaxCallsKeepsShellCompoundReview(t *testing.T) {
	rules := Ruleset{
		{Permission: toolname.Shell, Pattern: "*", Action: ActionDeny},
		{Permission: toolname.Shell, Pattern: "git *", Action: ActionAsk},
	}.RelaxCalls()
	if got := rules.Evaluate(toolname.Shell, "git status"); got != ActionAllow {
		t.Fatalf("simple command = %s, want allow", got)
	}
	if got := rules.Evaluate(toolname.Shell, "git status; rm build.txt"); got != ActionDeny {
		t.Fatalf("compound command = %s, want deny", got)
	}
}
