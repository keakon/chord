package ctxmgr

import (
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func TestToolDiscoveryNamesTrackCanonicalHistory(t *testing.T) {
	m := NewManager(32000, 0.8)
	history := []message.Message{{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "search", Name: toolname.ToolSearch}}}, {Role: message.RoleTool, ToolCallID: "search", Content: `{"tools":[{"name":"sample","status":"loaded","definition":{"name":"sample","input_schema":{"type":"object"}}}]}`, ToolStatus: message.ToolStatusSuccess}}
	for _, msg := range history {
		m.Append(msg)
	}
	names := m.ToolDiscoveryNames()
	if len(names) != 1 || names[0] != "sample" {
		t.Fatalf("names = %v", names)
	}
	names[0] = "changed"
	if m.ToolDiscoveryNames()[0] != "sample" {
		t.Fatal("discovery name view shares mutable state")
	}
	m.RestoreMessages(history)
	if len(m.ToolDiscoveryNames()) != 1 {
		t.Fatal("restoration lost canonical discovery facts")
	}
	if err := m.ReplacePrefixAtomic(len(history), []message.Message{{Role: message.RoleUser, Content: "Continue"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.ToolDiscoveryNames()) != 0 {
		t.Fatal("compacted discovery facts remained loaded")
	}
}
