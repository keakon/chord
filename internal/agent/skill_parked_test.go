package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/skill"
)

func TestParkedSkillStatePreservesDiscoveryAcrossRestore(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.llmFactory = func(string, []string, string) *llm.Client { return newTestLLMClient() }
	a.SetAgentConfigs(map[string]*config.AgentConfig{"worker": {Name: "worker", Mode: config.AgentModeSubAgent, Permission: parsePermissionNode(t, "skill:\n  '*': allow\n")}})
	a.SetSkills([]*skill.Meta{{Name: "sample", Description: "Sample workflow", Location: filepath.Join(a.ContentRoot(), "skills/sample/SKILL.md"), Paths: []string{"**/*.go"}}})
	sub := newControllableTestSubAgent(t, a, "task-sample")
	prepareSubRequestDirectoryFixture(t, sub)
	view := sub.skillActivationView()
	history := activationReadHistory(view)
	notice := buildSkillDiscoveryMessage(history, view)
	if notice == nil {
		t.Fatal("expected discovery")
	}
	history = append(history, *notice)
	sub.ctxMgr.RestoreMessages(history)
	if err := sub.recoveryManager().RewriteLog(sub.instanceID, history); err != nil {
		t.Fatal(err)
	}
	assertSuggested := func(states []skill.InvocationState) {
		t.Helper()
		if len(states) != 1 || !states[0].PathActivated || !states[0].NoticeRecorded || states[0].BodyPresent {
			t.Fatalf("incorrect discovery state: %+v", states)
		}
	}
	assertSuggested(sub.skillInvocationStates())
	a.SwitchFocus(sub.instanceID)
	a.handleAgentDone(Event{SourceID: sub.instanceID, Payload: &AgentResult{Summary: "Complete"}})
	if a.subAgentByID(sub.instanceID) != nil {
		t.Fatal("worker did not park")
	}
	assertSuggested(a.FocusedSkillInvocationStates())
	records, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if records[sub.taskID].skillSnapshot != nil {
		t.Fatal("runtime snapshot was serialized")
	}
	loaded := a.loadRestoredSubAgentStates(a.sessionDir, a.recoveryManager(), &recovery.SessionSnapshot{}, nil, records, nil)
	if len(loaded) != 1 || loaded[0].skillSnapshot == nil {
		t.Fatalf("skill evidence not restored: %+v", loaded)
	}
	a.setTaskRecords(records)
	if a.restoreLoadedSubAgents(loaded) != 1 {
		t.Fatal("worker state was not installed")
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	assertSuggested(a.parkedTaskSkillInvocationStates(rec))
	// Current metadata/ruleset must still win over the immutable history cache.
	a.SetSkills([]*skill.Meta{{Name: "sample", Description: "Updated workflow", Location: view.Catalog[0].Location, Paths: []string{"**/*.go"}}})
	states := a.parkedTaskSkillInvocationStates(rec)
	if states[0].PathActivated || states[0].NoticeRecorded {
		t.Fatal("old refs upgraded to new metadata")
	}
}

func TestParkedSkillSnapshotKeepsEvidenceWithoutBodies(t *testing.T) {
	view := skillActivationFixture(t, 1)
	history := activationReadHistory(view)
	history[1].Content = "file body"
	history[1].Parts = []message.ContentPart{{Type: message.ContentPartImage, ImagePath: "sample.png"}}
	notice := buildSkillDiscoveryMessage(history, view)
	history = append(history, *notice,
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "load", Name: "skill", Args: json.RawMessage(`{"name":"sample-000"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "load", ToolStatus: message.ToolStatusSuccess, Content: "skill body"})
	snapshot := newParkedSkillSnapshot(history, view.CheckoutRoot)
	history[1].SkillActivation.Skills[0].Revision = "changed"
	states := skillInvocationStatesFromHistory(snapshot.messages, view)
	if !states[0].PathActivated || !states[0].NoticeRecorded || !states[0].BodyPresent {
		t.Fatalf("evidence changed: %+v", states)
	}
	for _, msg := range snapshot.messages {
		if len(msg.Parts) != 0 || msg.Content == "file body" || msg.Content == "skill body" {
			t.Fatal("parked cache retains large bodies")
		}
	}
	carry := buildSkillActivationCarry(snapshot.messages, view, 1)
	compressed := newParkedSkillSnapshot([]message.Message{{Role: message.RoleUser, IsCompactionSummary: true, CompactionSkillActivation: carry}}, view.CheckoutRoot)
	states = skillInvocationStatesFromHistory(compressed.messages, view)
	if !states[0].PathActivated || states[0].BodyPresent {
		t.Fatal("compaction carry confused activation and body presence")
	}
}

func TestSkillActivationResultSkipsUnneededSnapshots(t *testing.T) {
	view := skillActivationFixture(t, 1)
	candidate := &skillActivationCandidate{SessionEpoch: 2, Generation: 3, TaskID: view.TaskID, CheckoutRoot: view.CheckoutRoot, Refs: matchSkillActivationPaths([]string{filepath.Join(view.CheckoutRoot, "file.go")}, view)}
	snapshots := 0
	history := func() []message.Message { snapshots++; return nil }
	currentView := func() skillActivationView { snapshots++; return view }
	for _, input := range []struct {
		name              string
		candidate         *skillActivationCandidate
		epoch, generation uint64
	}{
		{"write", candidate, 2, 3}, {"read", nil, 2, 3}, {"read", candidate, 1, 3}, {"read", candidate, 2, 2},
	} {
		if fact := skillActivationForResult(history, currentView, input.epoch, input.generation, input.name, "call", nil, input.candidate); fact != nil {
			t.Fatal("unexpected fact")
		}
	}
	if snapshots != 0 {
		t.Fatal("irrelevant result fetched history/catalog")
	}
	if fact := skillActivationForResult(history, currentView, 2, 3, "read", "call", nil, candidate); fact == nil || snapshots != 2 {
		t.Fatal("valid read lost activation")
	}
}
