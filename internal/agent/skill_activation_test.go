package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/toolname"
)

func skillActivationFixture(t *testing.T, n int) skillActivationView {
	t.Helper()
	root := t.TempDir()
	v := skillActivationView{TaskID: "task", CheckoutRoot: root}
	for i := range n {
		v.Catalog = append(v.Catalog, &skill.Meta{Name: fmt.Sprintf("sample-%03d", i), Location: filepath.Join(root, fmt.Sprintf("skill-%d/SKILL.md", i)), Description: "A sample workflow", Paths: []string{"**/*.go"}})
	}
	return v
}
func activationReadHistory(v skillActivationView) []message.Message {
	refs := matchSkillActivationPaths([]string{filepath.Join(v.CheckoutRoot, "main.go")}, v)
	return []message.Message{{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "read-1", Name: toolname.Read}}}, {Role: message.RoleTool, ToolCallID: "read-1", ToolStatus: message.ToolStatusSuccess, SkillActivation: &message.SkillActivationFact{TaskID: v.TaskID, CheckoutRoot: v.CheckoutRoot, SourceCallID: "read-1", Skills: refs}}}
}
func TestSkillActivationFactsRequirePairAndExactMetadata(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	if len(collectSkillActivationFacts(history, v)) != 1 {
		t.Fatal("valid read lost")
	}
	if len(collectSkillActivationFacts(history[1:], v)) != 0 {
		t.Fatal("unpaired fact accepted")
	}
	history[1].ToolStatus = message.ToolStatusCancelled
	if len(collectSkillActivationFacts(history, v)) != 0 {
		t.Fatal("cancelled fact accepted")
	}
	history[1].ToolStatus = message.ToolStatusSuccess
	v.Catalog[0].Description = "new revision"
	if len(collectSkillActivationFacts(history, v)) != 0 {
		t.Fatal("old fact upgraded to new metadata")
	}
}
func TestSkillDiscoveryBudgetAndNoDuplicate(t *testing.T) {
	v := skillActivationFixture(t, 100)
	history := activationReadHistory(v)
	for range 4 {
		n := buildSkillDiscoveryMessage(history, v)
		if n == nil {
			break
		}
		if len(n.Content) > skillDiscoveryMaxBytes || len(n.SkillDiscovery.Skills) > skillDiscoveryMaxEntries {
			t.Fatal("per-message budget exceeded")
		}
		history = append(history, *n)
	}
	bytes, entries := 0, 0
	for _, m := range history {
		if m.SkillDiscovery != nil {
			bytes += len(m.Content)
			entries += len(m.SkillDiscovery.Skills)
		}
	}
	if bytes > skillDiscoveryEpochMaxBytes || entries > skillDiscoveryEpochMaxEntries {
		t.Fatal("epoch budget exceeded")
	}
	if n := buildSkillDiscoveryMessage(history, v); n != nil {
		t.Fatal("exhausted budget still publishes")
	}
}
func TestSkillActivationCarryAndRestoreBudget(t *testing.T) {
	v := skillActivationFixture(t, 40)
	history := activationReadHistory(v)
	carry := buildSkillActivationCarry(history, v, 1)
	history = []message.Message{{Role: message.RoleUser, CompactionSkillActivation: carry}}
	n := buildSkillDiscoveryMessage(history, v)
	if n == nil || n.SkillDiscovery.Mode != message.SkillDiscoveryModeRestore || len(n.Content) > skillDiscoveryRestoreMaxBytes || len(n.SkillDiscovery.Skills) > 8 {
		t.Fatal("invalid restore notice")
	}
	history = append(history, *n)
	if buildSkillDiscoveryMessage(history, v) != nil {
		t.Fatal("restore paginated")
	}
	c2 := buildSkillActivationCarry(history, v, 2)
	count := 0
	for _, f := range c2.Facts {
		count += len(f.Skills)
	}
	if count != 40 {
		t.Fatalf("continuous compaction lost facts: %d", count)
	}
	v.Catalog[0].Description = "changed"
	if len(projectSkillDiscoveryMessages(history, v)) != 1 {
		t.Fatal("stale notice not omitted atomically")
	}
}
func TestSkillDiscoveryEscapedBudget(t *testing.T) {
	v := skillActivationFixture(t, 1)
	v.Catalog[0].Description = strings.Repeat("\"\n", 9000)
	n := buildSkillDiscoveryMessage(activationReadHistory(v), v)
	if n == nil || len(n.Content) > skillDiscoveryMaxBytes || strings.Contains(n.Content, "\n\n") {
		t.Fatal("escaping bypassed budget")
	}
}

func TestSkillActivationForkCutoffAndCheckout(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	beforeRead := []message.Message{history[0].Clone()}
	if len(collectSkillActivationFacts(beforeRead, v)) != 0 {
		t.Fatal("fork before result inherited activation")
	}
	fork := []message.Message{history[0].Clone(), history[1].Clone()}
	if len(collectSkillActivationFacts(fork, v)) != 1 {
		t.Fatal("main task fork lost activation")
	}
	fork[1].SkillActivation.Skills[0].Name = "changed"
	if history[1].SkillActivation.Skills[0].Name == "changed" {
		t.Fatal("fork shares metadata backing storage")
	}
	other := v
	other.CheckoutRoot = t.TempDir()
	if len(collectSkillActivationFacts(history, other)) != 0 {
		t.Fatal("fork with new checkout reused old activation")
	}
}

func TestSkillDiscoveryWaitsForWholeToolRound(t *testing.T) {
	v := skillActivationFixture(t, 1)
	history := activationReadHistory(v)
	history[0].ToolCalls = append(history[0].ToolCalls, message.ToolCall{ID: "edit-1", Name: toolname.Edit})
	if buildSkillDiscoveryMessage(history, v) != nil {
		t.Fatal("notice inserted before all required results")
	}
	history = append(history, message.Message{Role: message.RoleTool, ToolCallID: "edit-1", ToolStatus: message.ToolStatusSuccess})
	if buildSkillDiscoveryMessage(history, v) == nil {
		t.Fatal("closed tool round lost discovery")
	}
}

func TestCompactionInputFiltersInvalidSkillDiscovery(t *testing.T) {
	v := skillActivationFixture(t, 1)
	notice := buildSkillDiscoveryMessage(activationReadHistory(v), v)
	history := []message.Message{{Role: message.RoleUser, Content: "Inspect the sample project"}, *notice}
	input, err := (&MainAgent{}).buildCompactionInputWithOptions(history, 8192, nil, history, compactionAnchors{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input.Transcript, skillDiscoveryPreamble) || strings.Contains(input.RecentTailAnchor, skillDiscoveryPreamble) {
		t.Fatal("inactive task notice leaked into summary input")
	}
}
