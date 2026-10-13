package session

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestExportPreservesInstructionAndSkillFactsWithoutUserTurns(t *testing.T) {
	fact := message.DirectoryInstructionFact{CheckoutRoot: "/repo", Scope: ".", Operation: message.DirectoryInstructionUpsert, SourcePath: "/repo/AGENTS.md", Body: "rules", BodyHash: message.DirectoryInstructionBodyHash("rules")}
	history := []message.Message{{Role: message.RoleUser, Content: "task"}, {Role: message.RoleUser, Kind: message.KindDirectoryInstructions, DirectoryInstructions: &message.DirectoryInstructions{Facts: []message.DirectoryInstructionFact{fact}}}, {Role: message.RoleUser, Kind: message.KindSkillDiscovery, Content: "discovered skill", SkillDiscovery: &message.SkillDiscoveryRecord{ID: "notice", Skills: []message.SkillActivationRef{{Identity: "skill", Revision: "v1"}}}}, {Role: message.RoleTool, ToolCallID: "call", ToolStatus: message.ToolStatusCancelled, ToolControlReason: message.ToolControlReasonDirectoryInstructionsRequired}}
	exported, err := Export(history, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	restored := exported.ToMessages()
	if len(restored) != len(history) || restored[1].DirectoryInstructions.Facts[0] != fact || restored[2].SkillDiscovery.ID != "notice" || restored[3].ToolControlReason != history[3].ToolControlReason {
		t.Fatalf("facts lost: %+v", restored)
	}
	restored[1].DirectoryInstructions.Facts[0].Body = "mutated"
	if history[1].DirectoryInstructions.Facts[0].Body != "rules" {
		t.Fatal("export aliases fact storage")
	}
	turns := buildProjectedTurns(exported, DefaultProjectionLimits())
	if len(turns) != 1 || turns[0].UserText.Text != "task" {
		t.Fatalf("synthetic turns: %+v", turns)
	}
}
