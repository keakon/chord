package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

func TestImageToolFailureRecordsPanelOnceWithoutErrorCard(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	event := agent.ToolResultEvent{CallID: "image-call", Name: tools.NameGenerateImage, AgentID: "", Status: agent.ToolResultStatusError, Result: "image request outcome_unknown", Diagnostic: &agent.ToolErrorDiagnostic{Provider: "sample", Model: "gpt-image-1.5", Err: &llm.APIError{StatusCode: 502, Message: "image request outcome_unknown; category=provider_error"}}}
	m.ensureToolCallBlock(event.CallID, event.Name, "", event.AgentID, agent.ToolCallExecutionStateRunning, false)
	m.handleToolResultEvent(event)
	m.handleToolResultEvent(event)
	records := m.snapshotAgentErrors()
	if len(records) != 1 {
		t.Fatalf("records=%+v", records)
	}
	record := records[0]
	if record.StatusCode != 502 || record.Provider != "sample" || record.Model != "gpt-image-1.5" || record.AgentID != "" || record.Retry {
		t.Fatalf("record=%+v", record)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) != 1 || blocks[0].Type == BlockError || blocks[0].ResultStatus != agent.ToolResultStatusError {
		t.Fatalf("blocks=%+v", blocks)
	}
}

func TestToolErrorPanelIgnoresSuccessAndCancellation(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	for _, status := range []agent.ToolResultStatus{agent.ToolResultStatusSuccess, agent.ToolResultStatusCancelled} {
		m.recordToolErrorDiagnostic(agent.ToolResultEvent{Status: status, Diagnostic: &agent.ToolErrorDiagnostic{Err: &llm.APIError{StatusCode: 502, Message: "sample failure"}}})
	}
	m.recordToolErrorDiagnostic(agent.ToolResultEvent{Status: agent.ToolResultStatusError})
	if len(m.snapshotAgentErrors()) != 0 {
		t.Fatal("non-errors recorded")
	}
}

func TestToolDiagnosticPreservesSubAgentIdentity(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	m.recordToolErrorDiagnostic(agent.ToolResultEvent{AgentID: "sub-1", Status: agent.ToolResultStatusError, Diagnostic: &agent.ToolErrorDiagnostic{Provider: "sample", Model: "gpt-image-1.5", Err: &llm.APIError{StatusCode: 429, Code: "insufficient_quota", Message: "quota rejected"}}})
	records := m.snapshotAgentErrors()
	if len(records) != 1 || records[0].AgentID != "sub-1" || records[0].ErrorCode != "insufficient_quota" {
		t.Fatalf("records=%+v", records)
	}
}
