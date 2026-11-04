package ctxmgr

import (
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func TestSkillHistorySnapshotOwnsEvidenceAndOmitsUnrelatedPayloads(t *testing.T) {
	m := NewManager(128000, 0)
	ref := message.SkillActivationRef{Name: "sample", Identity: "identity", Revision: "revision"}
	fact := &message.SkillActivationFact{TaskID: "task", SourceCallID: "read-1", Skills: []message.SkillActivationRef{ref}}
	history := []message.Message{
		{Role: message.RoleUser, Content: "Inspect the image", Parts: []message.ContentPart{{Type: message.ContentPartImage, Data: make([]byte, 1024)}}},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{
			{ID: "skill-1", Name: toolname.Skill, Args: []byte(`{"name":"sample"}`)},
			{ID: "read-1", Name: toolname.Read, Args: []byte(`{"path":"sample.go"}`)},
			{ID: "shell-1", Name: toolname.Shell, Args: []byte(`{"command":"pwd"}`)},
		}},
		{Role: message.RoleTool, ToolCallID: "shell-1", Content: "unrelated output", FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: "sample.go"}}}},
		{Role: message.RoleTool, ToolCallID: "read-1", SkillActivation: fact, ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleTool, ToolCallID: "skill-1", Content: "skill body", ToolStatus: message.ToolStatusSuccess},
		{Role: message.RoleUser, Kind: message.KindSkillDiscovery, Content: "notice", SkillDiscovery: &message.SkillDiscoveryRecord{Skills: []message.SkillActivationRef{ref}}},
		{Role: message.RoleUser, IsCompactionSummary: true, Content: "checkpoint", CompactionSkillActivation: &message.SkillActivationCarry{Facts: []message.SkillActivationFact{*fact.Clone()}}},
		{Role: message.RoleUser, Question: []byte(`{"source_id":"input","result":{}}`)},
		{Role: message.RoleAssistant, RequestBatch: 42, Content: "unrelated reply"},
	}
	m.RestoreMessages(history)
	before := m.Snapshot()
	got, batch := m.SkillHistorySnapshot()
	if batch != 42 || len(got) != 7 {
		t.Fatalf("snapshot length/batch = %d/%d", len(got), batch)
	}
	if got[0].Content != "" || len(got[0].Parts) != 0 || !message.IsUserAuthored(got[0]) {
		t.Fatalf("user boundary includes payload or changed meaning: %+v", got[0])
	}
	if len(got[1].ToolCalls) != 2 || len(got[1].ToolCalls[1].Args) != 0 {
		t.Fatalf("unexpected calls: %+v", got[1].ToolCalls)
	}
	if got[3].Content != "skill body" || got[4].Content != "notice" || got[5].Content != "checkpoint" {
		t.Fatal("required content lost")
	}
	got[1].ToolCalls[0].Args[0] = '!'
	got[1].ToolCalls[1].Name = "changed"
	got[2].SkillActivation.Skills[0].Name = "changed"
	got[4].SkillDiscovery.Skills[0].Name = "changed"
	got[5].CompactionSkillActivation.Facts[0].Skills[0].Name = "changed"
	got[6].Question[0] = '!'
	if after := m.Snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot mutation changed canonical history")
	}
	m.RestoreMessages(nil)
	if got[3].Content != "skill body" {
		t.Fatal("history rewrite changed an earlier snapshot")
	}
}

func TestSkillHistorySnapshotKeepsDeclarationsAndBoundaries(t *testing.T) {
	m := NewManager(128000, 0)
	m.RestoreMessages([]message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "shared", Name: toolname.Skill}}},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "shared", Name: toolname.Shell}}},
		{Role: message.RoleTool, ToolCallID: "shared", Content: "result"},
		{Role: message.RoleUser, Kind: message.KindStreamContinue, Content: "Continue"},
		{Role: message.RoleUser, Question: []byte(`{"result":{}}`)},
		{Role: message.RoleUser, Content: "Next task"},
	})
	got, _ := m.SkillHistorySnapshot()
	if len(got) != 4 || got[1].ToolCalls[0].Name != toolname.Shell || !message.IsUserAuthored(got[3]) {
		t.Fatalf("declaration or boundary lost: %+v", got)
	}
}

func TestSkillHistorySnapshotConcurrentHistoryChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(128000, 0)
		history := []message.Message{
			{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "skill-1", Name: toolname.Skill, Args: []byte(`{"name":"sample"}`)}}},
			{Role: message.RoleTool, ToolCallID: "skill-1", Content: "body"},
		}
		go func() {
			for range 100 {
				m.RestoreMessages(history)
				m.Append(message.Message{Role: message.RoleUser, RequestBatch: 2, Content: "Next task"})
				m.DropLastMessages(1)
			}
		}()
		for range 100 {
			got, batch := m.SkillHistorySnapshot()
			if len(got) == 0 {
				continue
			}
			if len(got) != 2 && len(got) != 3 {
				t.Fatalf("partial snapshot: %+v", got)
			}
			if batch != uint64(len(got)-1) {
				t.Fatalf("batch %d does not match snapshot length %d", batch, len(got))
			}
			got[0].ToolCalls[0].Args[0] = '!'
		}
		synctest.Wait()
	})
}
