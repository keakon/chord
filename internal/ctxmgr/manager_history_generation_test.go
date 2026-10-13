package ctxmgr

import (
	"errors"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestHistoryGenerationTracksRewritesOnly(t *testing.T) {
	m := NewManager(10000, 0)
	m.Append(message.Message{Role: message.RoleUser, Content: "sample"})
	msgs, g := m.SnapshotWithGeneration()
	if g != 0 || len(msgs) != 1 {
		t.Fatal("append changed generation")
	}
	msgs[0].Content = "mutated"
	if m.Snapshot()[0].Content != "sample" {
		t.Fatal("snapshot alias")
	}
	m.DropLastMessages(0)
	if m.HistoryGeneration() != 0 {
		t.Fatal("no-op changed generation")
	}
	m.RestoreMessages(msgs)
	if m.HistoryGeneration() != 1 {
		t.Fatal("restore")
	}
	if err := m.ReplacePrefixAtomic(1, nil, func([]message.Message) ([]message.Message, error) { return nil, errors.New("sample") }); err == nil || m.HistoryGeneration() != 1 {
		t.Fatal("failed replacement changed generation")
	}
	if err := m.ReplacePrefixAtomic(1, nil, nil); err != nil || m.HistoryGeneration() != 2 {
		t.Fatal("replace")
	}
	m.DropLastMessages(1)
	if m.HistoryGeneration() != 2 {
		t.Fatal("empty drop changed generation")
	}
	m.Append(message.Message{Role: message.RoleTool, ToolCallID: "missing", Content: "sample"})
	if m.RepairOrphanToolMessagesInPlace() != 1 || m.HistoryGeneration() != 3 {
		t.Fatal("repair")
	}
	if m.RepairOrphanToolMessagesInPlace() != 0 || m.HistoryGeneration() != 3 {
		t.Fatal("no-op repair")
	}
	m.Append(message.Message{Role: message.RoleUser, Content: "sample"})
	m.DropLastMessages(1)
	if m.HistoryGeneration() != 4 {
		t.Fatal("drop")
	}
}
