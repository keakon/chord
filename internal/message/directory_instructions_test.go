package message

import (
	"encoding/json"
	"testing"
)

func TestRenderDirectoryInstructionFactsOmitsFileFraming(t *testing.T) {
	facts := []DirectoryInstructionFact{
		{CheckoutRoot: "/repo", Scope: ".", SourcePath: "/repo/AGENTS.md", Operation: DirectoryInstructionUpsert, Body: "Use tabs."},
		{CheckoutRoot: "/repo", Scope: "pkg", SourcePath: "/repo/pkg/AGENTS.md", Operation: DirectoryInstructionUpsert, Body: "Keep identifiers unchanged."},
	}
	want := `# Workspace Instructions

Deeper directory rules override parent rules only within their subtree; sibling rules do not override each other. System instructions, explicit user requests, and role permissions remain higher priority.

Scope: . (checkout: /repo)
<INSTRUCTIONS>
Use tabs.
</INSTRUCTIONS>

Scope: pkg (checkout: /repo)
<INSTRUCTIONS>
Keep identifiers unchanged.
</INSTRUCTIONS>
`
	if got := RenderDirectoryInstructionFacts(facts); got != want {
		t.Fatalf("rendered instructions = %q, want %q", got, want)
	}
}

func TestDirectoryInstructionFactsAreTypedAndIsolated(t *testing.T) {
	f := DirectoryInstructionFact{CheckoutRoot: "/repo", Scope: "pkg", SourcePath: "/repo/pkg/AGENTS.md", Operation: DirectoryInstructionUpsert, Body: "Use scoped conventions."}
	f.BodyHash = DirectoryInstructionBodyHash(f.Body)
	m := Message{Role: RoleUser, Kind: KindDirectoryInstructions, DirectoryInstructions: &DirectoryInstructions{Facts: []DirectoryInstructionFact{f}}}
	if err := ValidateDirectoryInstructions(m); err != nil {
		t.Fatal(err)
	}
	if IsUserAuthored(m) || IsDirectoryInstructions(Message{Role: RoleUser, Content: RenderDirectoryInstructionFacts([]DirectoryInstructionFact{f})}) {
		t.Fatal("typed facts confused with user input")
	}
	copy := m.Clone()
	copy.DirectoryInstructions.Facts[0].Body = "changed"
	if m.DirectoryInstructions.Facts[0].Body != f.Body {
		t.Fatal("clone aliases facts")
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Message
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err = ValidateDirectoryInstructions(decoded); err != nil {
		t.Fatal(err)
	}
	if LastConversationMessageIndex([]Message{{Role: RoleUser, Content: "task"}, m}) != 0 {
		t.Fatal("fact changed continuation anchor")
	}
	for _, scope := range []string{"../pkg", "/pkg", "pkg/../x", "pkg\\x"} {
		bad := f
		bad.Scope = scope
		if ValidateDirectoryInstructionFact(bad) == nil {
			t.Fatalf("accepted invalid scope %q", scope)
		}
	}
}
func TestDirectoryInstructionAccountingDropsStaleAndBlockedBodies(t *testing.T) {
	f := DirectoryInstructionFact{CheckoutRoot: "/repo", Scope: ".", SourcePath: "/repo/AGENTS.md", Operation: DirectoryInstructionUpsert, Body: "old body"}
	f.BodyHash = DirectoryInstructionBodyHash(f.Body)
	fact := func(f DirectoryInstructionFact) Message {
		return Message{Role: RoleUser, Kind: KindDirectoryInstructions, DirectoryInstructions: &DirectoryInstructions{Facts: []DirectoryInstructionFact{f}}}
	}
	old := fact(f)
	f.Operation = DirectoryInstructionBlocked
	f.Reason = DirectoryInstructionReasonInputBudget
	f.Body = "new candidate"
	f.BodyHash = DirectoryInstructionBodyHash(f.Body)
	out := DirectoryInstructionAccountingMessages([]Message{old, fact(f)})
	// The blocked fact supersedes the earlier body, and a blocked scope never
	// becomes a request message, so accounting charges neither.
	if len(out) != 0 {
		t.Fatalf("stale or blocked body counted: %#v", out)
	}
}
func TestSkillDiscoveryCloneCarriesIndependentReferences(t *testing.T) {
	m := Message{Role: RoleUser, Kind: KindSkillDiscovery, SkillDiscovery: &SkillDiscoveryRecord{Skills: []SkillActivationRef{{Identity: "id", Revision: "v1"}}}, CompactionSkillActivation: &SkillActivationCarry{Facts: []SkillActivationFact{{Skills: []SkillActivationRef{{Name: "skill"}}}}}}
	c := m.Clone()
	c.SkillDiscovery.Skills[0].Identity = "changed"
	c.CompactionSkillActivation.Facts[0].Skills[0].Name = "changed"
	if m.SkillDiscovery.Skills[0].Identity != "id" || m.CompactionSkillActivation.Facts[0].Skills[0].Name != "skill" || IsUserAuthored(m) {
		t.Fatal("skill discovery ownership/authorship")
	}
}
