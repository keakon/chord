package agent

import (
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestNativeSessionRestorePersistsUnknownMainReceipt(t *testing.T) {
	root := t.TempDir()
	dir := testProjectSessionDir(t, root, "native")
	rm := recovery.NewRecoveryManager(dir)
	if err := rm.PersistMessageDurable(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "Search the sample documentation"}); err != nil {
		t.Fatal(err)
	}
	rm.Close()
	j := recovery.NativeRequestJournal{SessionDir: dir, AgentID: identity.MainAgentID}
	id, err := j.Begin(llm.NativeRequestRecord{Target: "sample/test-model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgentForRestore(t, root, dir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatal(err)
	}
	history := a.GetMessages()
	if len(history) != 2 || history[1].NativeTools == nil || !history[1].NativeTools.OutcomeUnknown || history[1].NativeTools.RequestIDs[0] != id {
		t.Fatalf("history=%+v", history)
	}
	if displays := tools.NativeToolDisplays(history[1].NativeTools); len(displays) != 1 || displays[0].Status != message.ToolStatusError {
		t.Fatalf("displays=%+v", displays)
	}
	durable, err := a.recoveryManager().LoadMessages(identity.MainAgentID)
	if err != nil || len(durable) != len(history) || durable[1].NativeTools == nil {
		t.Fatalf("durable=%+v err=%v", durable, err)
	}
	if _, err := j.Check(); err == nil {
		t.Fatal("restored unknown released barrier")
	}
}

func TestNativeRestoredTaskReceiptUsesDurableIdentity(t *testing.T) {
	dir := t.TempDir()
	rm := recovery.NewRecoveryManager(dir)
	t.Cleanup(rm.Close)
	j := recovery.NativeRequestJournal{SessionDir: dir, AgentID: "task-1"}
	if _, err := j.Begin(llm.NativeRequestRecord{Target: "sample/test-model"}, nil); err != nil {
		t.Fatal(err)
	}
	loaded := &loadedSessionState{SessionPath: dir, SubAgentStates: []loadedSubAgentState{{TaskID: "task-1", InstanceID: "worker-current"}, {TaskID: "task-2", InstanceID: "worker-other"}}}
	if err := restoreNativeSessionReceipts(loaded, rm); err != nil {
		t.Fatal(err)
	}
	if len(loaded.SubAgentStates[0].Messages) != 1 || len(loaded.SubAgentStates[1].Messages) != 0 {
		t.Fatalf("states=%+v", loaded.SubAgentStates)
	}
	durable, err := rm.LoadMessages("worker-current")
	if err != nil || len(durable) != 1 || !durable[0].NativeTools.OutcomeUnknown {
		t.Fatalf("durable=%+v err=%v", durable, err)
	}
}

func TestNativeFailureReceiptsAreNotDuplicated(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	receipt := &message.NativeToolHistory{Target: "sample/test-model", OutcomeUnknown: true, RequestIDs: []string{"request-1"}}
	a.persistNativeFailure(receipt)
	a.persistNativeFailure(receipt)
	if history := a.GetMessages(); len(history) != 1 {
		t.Fatalf("history=%+v", history)
	}
}
