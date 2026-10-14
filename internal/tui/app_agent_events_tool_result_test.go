package tui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionToolResultEventPreservesStructuredReceipt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    string
		payload string
		want    string
	}{
		{name: "accepted", args: `{"questions":[{"header":"Destination","question":"Where should files go?"}],"wait":false}`, payload: `{"status":"accepted","batch_id":"batch-1","question_ids":["question-1"]}`, want: "Questions sent; awaiting answers."},
		{name: "resolved", args: `{"questions":[{"header":"Destination","question":"Where should files go?","options":[{"id":"local","label":"Save locally"}]}]}`, payload: `{"status":"resolved","answers":[{"question_id":"question-1","header":"Destination","outcome":"answered","selected_ids":["local"],"selected":["Save locally"]}]}`, want: "✓ 1. Save locally"},
		{name: "interrupted", args: `{"wait_for":["question-1"]}`, payload: `{"status":"wait_interrupted","pending_ids":["question-1"]}`, want: "1 question still awaiting an answer."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
			block, _ := m.ensureToolCallBlock("question-call", tools.NameQuestion, tc.args, "", agent.ToolCallExecutionStateRunning, false)
			notes := []string{"Sample diagnostic"}
			result := tc.payload + "\n" + notes[0]
			event := agent.ToolResultEvent{CallID: block.ToolID, Name: tools.NameQuestion, ArgsJSON: tc.args, Result: result, Payload: tc.payload, Notes: notes, Status: message.ToolStatusSuccess}
			m.handleToolResultEvent(event)
			m.handleToolResultEvent(event)
			if block.ResultPayload != tc.payload || !slices.Equal(block.ResultNotes, notes) {
				t.Fatalf("receipt lost: payload=%q notes=%v", block.ResultPayload, block.ResultNotes)
			}
			view := stripANSI(strings.Join(block.Render(100, ""), "\n"))
			if !strings.Contains(view, tc.want) || strings.Contains(view, `"status"`) || strings.Count(view, notes[0]) != 1 {
				t.Fatalf("unexpected live result:\n%s", view)
			}

			nextID := block.ID
			restored := messagesToBlocks([]message.Message{
				{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: block.ToolID, Name: tools.NameQuestion, Args: json.RawMessage(tc.args)}}},
				{Role: message.RoleTool, ToolCallID: block.ToolID, ToolStatus: string(event.Status), Content: result, ToolPayload: tc.payload, ToolNotes: notes},
			}, &nextID)
			if len(restored) != 1 {
				t.Fatalf("restored block count=%d", len(restored))
			}
			if restoredView := stripANSI(strings.Join(restored[0].Render(100, ""), "\n")); restoredView != view {
				t.Fatalf("restored and live views differ:\nlive:\n%s\nrestored:\n%s", view, restoredView)
			}
			notes[0] = "Changed source note"
			if block.ResultNotes[0] != "Sample diagnostic" {
				t.Fatal("card retained the event's mutable notes slice")
			}
		})
	}
}

func TestToolResultEventUpdatesIndependentFieldsWithUnchangedContent(t *testing.T) {
	m := NewModelWithSize(&sessionControlAgent{}, 100, 30)
	block, _ := m.ensureToolCallBlock("question-call", tools.NameQuestion, `{"wait_for":["question-1"]}`, "", agent.ToolCallExecutionStateRunning, false)
	event := agent.ToolResultEvent{CallID: block.ToolID, Name: tools.NameQuestion, Result: "Sample result", Payload: `{"status":"resolved"}`, Status: message.ToolStatusSuccess}
	m.handleToolResultEvent(event)
	event.Payload = `{"status":"wait_interrupted","pending_ids":["question-1"]}`
	m.handleToolResultEvent(event)
	if block.ResultPayload != event.Payload {
		t.Fatal("unchanged combined content suppressed a payload update")
	}
	event.Notes = []string{"Sample diagnostic"}
	m.handleToolResultEvent(event)
	if !slices.Equal(block.ResultNotes, event.Notes) {
		t.Fatal("unchanged combined content suppressed a notes update")
	}
	view := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(view, "1 question still awaiting an answer.") || strings.Count(view, event.Notes[0]) != 1 {
		t.Fatalf("updated receipt was not rendered:\n%s", view)
	}
}
