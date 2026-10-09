package message

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/toolname"
)

func TestFailedDiscoveryCannotActivateSchemas(t *testing.T) {
	raw := `{"tools":[{"name":"sample","status":"loaded","definition":{"name":"sample","input_schema":{"type":"object"}}}]}`
	history := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "search", Name: toolname.ToolSearch}}}, {Role: RoleTool, ToolCallID: "search", Content: raw, ToolStatus: ToolStatusError}}
	if len(ToolDiscoveryHistory(history)) != 0 {
		t.Fatal("failed discovery activated a definition")
	}
	history[1].ToolStatus = ToolStatusSuccess
	if names := ToolDiscoveryHistory(history); len(names) != 1 || names[0] != "sample" {
		t.Fatalf("load=%v", names)
	}
}

func TestDiscoveryProjectionIsIdempotentAndCanonicalHistoryIsImmutable(t *testing.T) {
	raw := `{"tools":[{"name":"sample","status":"loaded","definition":{"name":"sample","description":"Sample tool","input_schema":{"type":"object","properties":{"record":{"type":"string"}}}}}]}`
	history := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "search", Name: toolname.ToolSearch}}}, {Role: RoleTool, ToolCallID: "search", Content: raw, ToolStatus: ToolStatusSuccess}}
	projected := ProjectToolDiscoveryHistory(history)
	if strings.Contains(projected[1].Content, "definition") || history[1].Content != raw || len(ToolDiscoveryHistory(history)) != 1 {
		t.Fatal("projection leaked schemas or changed canonical discovery facts")
	}
	again := ProjectToolDiscoveryHistory(projected)
	if &again[0] != &projected[0] {
		t.Fatal("already projected history was copied again")
	}
}
