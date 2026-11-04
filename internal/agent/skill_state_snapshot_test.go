package agent

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/tools"
)

func TestSkillStateSnapshotMatchesFullHistory(t *testing.T) {
	v := skillActivationFixture(t, 1)
	v.TaskID = identity.MainAgentID
	meta := v.Catalog[0]
	full := tools.FormatSkillInvocationResult(&skill.Skill{Meta: *meta, Content: "Inspect the sample code."}, "")
	base := []message.Message{
		{Role: message.RoleUser, Content: "Inspect the sample project"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{
			{ID: "skill-1", Name: tools.NameSkill, Args: fmt.Appendf(nil, `{"name":%q}`, meta.Name)},
			{ID: "other-1", Name: tools.NameShell, Args: []byte(`{"command":"pwd"}`)},
		}},
		{Role: message.RoleTool, ToolCallID: "other-1", Content: "sample", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleTool, ToolCallID: "skill-1", Content: full, ToolStatus: message.ToolStatusSuccess},
	}
	read := activationReadHistory(v)
	notice := *buildSkillDiscoveryMessage(read, v)
	activated := append(append([]message.Message(nil), base...), read...)
	activated = append(activated, notice)
	checkpoint := message.Message{
		Role: message.RoleUser, IsCompactionSummary: true,
		Content:                   "[Context Summary]\n\n" + checkpointSkillsHeading + "\n- " + meta.Name,
		CompactionSkillActivation: buildSkillActivationCarry(activated, v, 1),
	}
	type testCase struct {
		name                             string
		history                          []message.Message
		body, earlier, activated, notice bool
	}
	tests := []testCase{
		{name: "full body", history: base, body: true, earlier: true},
		{name: "path activation and notice", history: activated, body: true, earlier: true, activated: true, notice: true},
		{name: "new user turn", history: append(append([]message.Message(nil), activated...), message.Message{Role: message.RoleUser, Content: "Next task"}), earlier: true, activated: true, notice: true},
		{name: "synthetic continuation", history: append(append([]message.Message(nil), base...), message.Message{Role: message.RoleUser, Kind: message.KindStreamContinue, Content: "Continue"}), body: true, earlier: true},
		{name: "question decision", history: append(append([]message.Message(nil), base...), message.Message{Role: message.RoleUser, Question: []byte(`{"result":{}}`)}), body: true, earlier: true},
		{name: "checkpoint carry", history: []message.Message{checkpoint}, earlier: true, activated: true},
		{name: "checkpoint after body", history: append(append([]message.Message(nil), activated...), checkpoint), earlier: true, activated: true, notice: true},
		{name: "notice without archived read", history: []message.Message{notice}, activated: true, notice: true},
		{name: "unrelated batch ages body", history: append(append([]message.Message(nil), base...), message.Message{Role: message.RoleAssistant, RequestBatch: 100, Content: "Working"}), earlier: true},
	}
	for _, status := range []string{message.ToolStatusError, message.ToolStatusCancelled} {
		history := append([]message.Message(nil), base...)
		history[len(history)-1].ToolStatus = status
		tests = append(tests, testCase{name: status, history: history})
	}
	envelope := append([]message.Message(nil), base...)
	envelope[len(envelope)-1].Content = tools.FormatSkillAlreadyLoadedResult(&skill.Skill{Meta: *meta}, "")
	tests = append(tests, testCase{name: "name-only envelope", history: envelope, earlier: true})
	failedRead := []message.Message{read[0].Clone(), read[1].Clone()}
	failedRead[1].ToolStatus = message.ToolStatusError
	tests = append(tests, testCase{name: "failed activation read", history: failedRead})
	redeclaredRead := []message.Message{
		read[0].Clone(),
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: read[1].ToolCallID, Name: tools.NameShell}}},
		read[1].Clone(),
	}
	tests = append(tests, testCase{name: "activation source redeclared", history: redeclaredRead})
	invalidNotice := notice.Clone()
	invalidNotice.SkillDiscovery.ID = "invalid"
	tests = append(tests, testCase{name: "invalid notice", history: []message.Message{invalidNotice}})
	conflictingNotice := notice.Clone()
	conflictingNotice.Content += " conflicting text"
	tests = append(tests, testCase{name: "conflicting notices", history: []message.Message{notice, conflictingNotice}, activated: true})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &MainAgent{ctxMgr: ctxmgr.NewManager(128000, 0), loadedSkills: v.Catalog}
			a.directoryInstructions.Store(&directoryInstructionRuntime{binding: directoryInstructionBinding{CheckoutRoot: v.CheckoutRoot}})
			for _, msg := range tt.history {
				a.ctxMgr.Append(msg)
				fullHistory := a.ctxMgr.Snapshot()
				want := skillInvocationStatesFromVisibleBodies(fullHistory, a.skillActivationView(), a.currentRequestBatch(fullHistory), a.contextReductionPolicy().StaleAgeTurns)
				if got := a.FocusedSkillInvocationStates(); !reflect.DeepEqual(got, want) {
					t.Fatalf("append: got %+v, want %+v", got, want)
				}
			}
			for range 2 {
				got := a.FocusedSkillInvocationStates()
				if len(got) != 1 || got[0].BodyPresent != tt.body || got[0].LoadedEarlier != tt.earlier || got[0].PathActivated != tt.activated || got[0].NoticeRecorded != tt.notice {
					t.Fatalf("state = %+v", got)
				}
				// Restore must expose the same facts without requiring a cache reset.
				a.ctxMgr.RestoreMessages(tt.history)
			}
		})
	}
}

func TestSkillStateSnapshotUsesCurrentViewAndBatch(t *testing.T) {
	v := skillActivationFixture(t, 1)
	v.TaskID = identity.MainAgentID
	a := &MainAgent{ctxMgr: ctxmgr.NewManager(128000, 0), loadedSkills: v.Catalog}
	a.directoryInstructions.Store(&directoryInstructionRuntime{binding: directoryInstructionBinding{CheckoutRoot: v.CheckoutRoot}})
	history := activationReadHistory(v)
	history = append(history, *buildSkillDiscoveryMessage(history, v))
	a.ctxMgr.RestoreMessages(history)
	if state := a.FocusedSkillInvocationStates()[0]; !state.PathActivated || !state.NoticeRecorded {
		t.Fatalf("initial state = %+v", state)
	}
	changed := *v.Catalog[0]
	changed.Description = "A different revision"
	a.loadedSkills = skill.SnapshotCatalog([]*skill.Meta{&changed})
	if state := a.FocusedSkillInvocationStates()[0]; state.PathActivated || state.NoticeRecorded {
		t.Fatalf("stale metadata remains activated: %+v", state)
	}
	a.loadedSkills = v.Catalog
	a.ruleset = permission.Ruleset{{Permission: tools.NameSkill, Pattern: "*", Action: permission.ActionDeny}}
	if state := a.FocusedSkillInvocationStates()[0]; state.ModelCallable || state.PathActivated || state.NoticeRecorded {
		t.Fatalf("denied skill remains callable or activated: %+v", state)
	}
	a.ruleset = nil
	a.directoryInstructions.Store(&directoryInstructionRuntime{binding: directoryInstructionBinding{CheckoutRoot: t.TempDir()}})
	if state := a.FocusedSkillInvocationStates()[0]; state.PathActivated || state.NoticeRecorded {
		t.Fatalf("different checkout inherits activation: %+v", state)
	}
	a.loadedSkills = nil
	if got := a.FocusedSkillInvocationStates(); len(got) != 0 {
		t.Fatalf("removed catalog still visible: %+v", got)
	}

	a = skillStateBenchAgent(1)
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, RequestBatch: 100})
	if a.FocusedSkillInvocationStates()[0].BodyPresent {
		t.Fatal("full-history maximum batch was lost")
	}
	a.requestBatches.reserve(a.sessionEpoch, 1)
	if !a.FocusedSkillInvocationStates()[0].BodyPresent {
		t.Fatal("live request batch must take precedence over restored batches")
	}
	a.ctxMgr.DropLastMessages(2)
	if a.FocusedSkillInvocationStates()[0].BodyPresent {
		t.Fatal("removed result still exposes its body")
	}
	a.ctxMgr.RestoreMessages(nil)
	if state := a.FocusedSkillInvocationStates()[0]; state.BodyPresent || state.LoadedEarlier {
		t.Fatalf("cleared history retains invocation: %+v", state)
	}
}

func TestSubAgentSkillStateSnapshotKeepsBodyWithoutBatchAging(t *testing.T) {
	a := skillStateBenchAgent(1)
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, RequestBatch: 100})
	s := &SubAgent{ctxMgr: a.ctxMgr, loadedSkills: a.loadedSkills}
	if state := s.skillInvocationStates()[0]; !state.BodyPresent || !state.LoadedEarlier {
		t.Fatalf("worker body incorrectly aged: %+v", state)
	}
	s.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "Next task"})
	if state := s.skillInvocationStates()[0]; state.BodyPresent || !state.LoadedEarlier {
		t.Fatalf("worker turn boundary lost: %+v", state)
	}
}

func skillStateBenchAgent(rounds int) *MainAgent {
	meta := &skill.Meta{Name: "sample-skill", Description: "A sample workflow"}
	a := &MainAgent{
		ctxMgr:       ctxmgr.NewManager(128000, 0),
		loadedSkills: []*skill.Meta{meta},
	}
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "Inspect the sample project"})
	args := []byte(`{"patch":"` + strings.Repeat("sample change ", 200) + `"}`)
	for i := range rounds {
		id := fmt.Sprintf("call-%d", i)
		a.ctxMgr.Append(message.Message{
			Role: message.RoleAssistant, RequestBatch: uint64(i + 1),
			ToolCalls: []message.ToolCall{{ID: id, Name: tools.NameApplyPatch, Args: args}},
		})
		a.ctxMgr.Append(message.Message{
			Role: message.RoleTool, ToolCallID: id, ToolStatus: message.ToolStatusSuccess,
			Content: strings.Repeat("sample output\n", 100),
			FileState: &message.ToolFileState{
				Writes:  []message.TrackedFileState{{Path: "sample.go", Exists: true}},
				Changes: []message.ToolFileChange{{Path: "sample.go", Added: 2, Removed: 1}},
			},
			ToolChangedPaths: []string{"sample.go"},
		})
	}
	a.ctxMgr.Append(message.Message{
		Role: message.RoleAssistant, RequestBatch: uint64(rounds + 1),
		ToolCalls: []message.ToolCall{{ID: "skill-1", Name: tools.NameSkill, Args: []byte(`{"name":"sample-skill"}`)}},
	})
	a.ctxMgr.Append(message.Message{
		Role: message.RoleTool, ToolCallID: "skill-1", ToolStatus: message.ToolStatusSuccess,
		Content: tools.FormatSkillInvocationResult(&skill.Skill{Meta: *meta, Content: "Inspect the diff carefully."}, ""),
	})
	return a
}

func BenchmarkFocusedSkillInvocationStates(b *testing.B) {
	for _, rounds := range []int{250, 1000} {
		b.Run(fmt.Sprintf("messages-%d", rounds*2), func(b *testing.B) {
			a := skillStateBenchAgent(rounds)
			b.ReportAllocs()
			for b.Loop() {
				states := a.FocusedSkillInvocationStates()
				if len(states) != 1 || !states[0].BodyPresent || !states[0].LoadedEarlier {
					b.Fatalf("unexpected states: %+v", states)
				}
			}
		})
	}
}
