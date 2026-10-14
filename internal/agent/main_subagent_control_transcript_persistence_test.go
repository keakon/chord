package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSubAgentPersistencePumpPreservesMessageOrder(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-persist-order")
	for _, content := range []string{"first", "second", "third"} {
		sub.persistMessageAsync(message.Message{Role: "user", Content: content}, "ordered test message", nil)
	}
	a.flushPersist()

	msgs, err := a.recoveryManager().LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 3 || msgs[0].Content != "first" || msgs[1].Content != "second" || msgs[2].Content != "third" {
		t.Fatalf("persisted messages = %#v, want enqueue order", msgs)
	}
}

func TestSubAgentControlToolPersistenceKeepsResultAfterRestore(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-control-persist")
	callID := "call-escalate-order"
	sub.persistMessageAsync(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameEscalate,
			Args: []byte(`{"reason":"need owner input"}`),
		}},
	}, "assistant control call", nil)
	sub.persistMessageAsync(message.Message{
		Role:       "tool",
		ToolCallID: callID,
		Content:    "Escalation sent: need owner input",
	}, "control tool result", nil)
	a.flushPersist()

	msgs, err := a.recoveryManager().LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != message.RoleAssistant || msgs[1].Role != message.RoleTool {
		t.Fatalf("persisted control messages = %#v, want assistant then tool", msgs)
	}

	restored := newControllableTestSubAgent(t, a, "adhoc-control-restore")
	restored.RestoreMessages(msgs)
	got := restored.GetMessages()
	if len(got) != 2 || got[1].ToolCallID != callID {
		t.Fatalf("restored control messages = %#v, want settled tool result retained", got)
	}
}

func TestPersistencePumpCloseDrainsAcceptedEntriesAndRejectsLaterEnqueues(t *testing.T) {
	pump := newPersistencePump(1)
	var got []string
	pump.start(func(entry persistEntry) { got = append(got, entry.msg.Content) })
	if !pump.enqueue(persistEntry{msg: message.Message{Content: "accepted"}}, make(chan struct{})) {
		t.Fatal("initial enqueue rejected")
	}
	pump.closeUntil(nil)
	<-pump.done
	if len(got) != 1 || got[0] != "accepted" {
		t.Fatalf("drained entries = %#v, want accepted entry", got)
	}
	if pump.enqueue(persistEntry{msg: message.Message{Content: "late"}}, make(chan struct{})) {
		t.Fatal("enqueue succeeded after close")
	}
}
