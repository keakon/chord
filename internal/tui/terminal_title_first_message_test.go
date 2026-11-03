package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func TestStageDraftSetsTerminalTitleFromFirstUserMessage(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.stageDraft(queuedDraft{ID: "request-1", Content: "first direct request"})
	if got := m.terminalTitleBase; got != "first direct request" {
		t.Fatalf("terminalTitleBase = %q, want first user message", got)
	}

	// Later sends must not overwrite the first-request title.
	m.stageDraft(queuedDraft{ID: "request-2", Content: "second direct request"})
	if got := m.terminalTitleBase; got != "first direct request" {
		t.Fatalf("terminalTitleBase = %q, want unchanged after later message", got)
	}
}

func TestPendingDraftConsumedEventSetsTerminalTitleFromFirstUserMessage(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.input.SetValue("first queued request")

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	draftID := backend.queuedDraftIDs[0]
	_ = m.handleAgentEvent(agentEventMsg{event: agent.PendingDraftConsumedEvent{
		DraftID: draftID,
		Parts:   []message.ContentPart{{Type: message.ContentPartText, Text: "first queued request"}},
	}})
	if got := m.terminalTitleBase; got != "first queued request" {
		t.Fatalf("terminalTitleBase = %q, want first queued request", got)
	}

	// A later consumed message must not overwrite the first-request title.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.PendingDraftConsumedEvent{
		DraftID: "draft-2",
		Parts:   []message.ContentPart{{Type: message.ContentPartText, Text: "second queued request"}},
	}})
	if got := m.terminalTitleBase; got != "first queued request" {
		t.Fatalf("terminalTitleBase = %q, want unchanged after later message", got)
	}
}

func TestFirstUserMessageTerminalTitleUsesAllPromptText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []message.ContentPart
		want  string
	}{
		{
			name: "text before paste",
			parts: []message.ContentPart{
				{Type: message.ContentPartText, Text: "Inspect "},
				{Type: message.ContentPartText, Text: "sample output", DisplayText: "[pasted text]"},
			},
			want: "Inspect sample output",
		},
		{
			name: "whitespace before paste",
			parts: []message.ContentPart{
				{Type: message.ContentPartText, Text: " \n"},
				{Type: message.ContentPartText, Text: "Inspect sample output", DisplayText: "[pasted text]"},
			},
			want: "Inspect sample output",
		},
		{
			name: "image before text",
			parts: []message.ContentPart{
				{Type: message.ContentPartImage, FileName: "sample.png", MimeType: "image/png"},
				{Type: message.ContentPartText, Text: "Inspect sample output"},
			},
			want: "Inspect sample output",
		},
		{
			name: "file reference before text",
			parts: []message.ContentPart{
				{Type: message.ContentPartText, Text: `<file path="sample.go">package sample</file>`},
				{Type: message.ContentPartText, Text: "Inspect sample output"},
			},
			want: "Inspect sample output",
		},
	} {
		for _, delivery := range []string{"direct", "queued"} {
			t.Run(tc.name+"/"+delivery, func(t *testing.T) {
				m := NewModelWithSize(nil, 80, 24)
				if delivery == "direct" {
					m.stageDraft(queuedDraft{ID: "request-1", Content: "preview", Parts: tc.parts})
				} else {
					handled, _ := m.handleTurnAgentEvent(agent.PendingDraftConsumedEvent{DraftID: "request-1", Parts: tc.parts})
					if !handled {
						t.Fatal("input consumption event was not handled")
					}
				}
				if got := m.terminalTitleBase; got != tc.want {
					t.Fatalf("terminalTitleBase = %q, want %q", got, tc.want)
				}
			})
		}
	}
}

func TestFirstUserMessageTerminalTitleFallsBackToContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		parts   []message.ContentPart
		want    string
	}{
		{name: "content without parts", content: " fallback request ", want: "fallback request"},
		{
			name:    "attachment without text",
			content: "attachment preview",
			parts:   []message.ContentPart{{Type: message.ContentPartImage, FileName: "sample.png", MimeType: "image/png"}},
			want:    "attachment preview",
		},
		{
			name:    "blank text parts",
			content: "fallback request",
			parts:   []message.ContentPart{{Type: message.ContentPartText, Text: " \n"}},
			want:    "fallback request",
		},
		{name: "empty message", want: "chord"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			m.maybeSetTerminalTitleFromFirstUserMessage(tc.content, tc.parts)
			if got := m.terminalTitleBase; got != tc.want {
				t.Fatalf("terminalTitleBase = %q, want %q", got, tc.want)
			}
			m.maybeSetTerminalTitleFromFirstUserMessage("later request", nil)
			if got := m.terminalTitleBase; got != tc.want {
				t.Fatalf("later message changed terminalTitleBase to %q, want %q", got, tc.want)
			}
		})
	}
}

func TestManagedComposerPasteSetsTerminalTitleFromWholeMessage(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModel(backend)
	m.mode = ModeInsert
	m.input.InsertString("Inspect ")
	if !m.input.InsertLargePaste("sample output\n" + strings.Repeat("sample line\n", 12)) {
		t.Fatal("paste was not inserted")
	}
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(backend.submittedInputs) != 1 {
		t.Fatalf("submitted inputs = %d, want 1", len(backend.submittedInputs))
	}
	submitted := backend.submittedInputs[0]
	if len(submitted.Parts) < 2 || submitted.Parts[0].Text != "Inspect " {
		t.Fatalf("expected split composer text: %+v", submitted.Parts)
	}
	m.handleTurnAgentEvent(agent.PendingDraftConsumedEvent{
		Target: backend.InputDeliveryTarget(), InputID: "input-1",
		DraftID: submitted.DraftID, Revision: 1, Parts: submitted.Parts,
	})
	want := deriveTerminalTitle(message.UserPromptPlainText(message.Message{Parts: submitted.Parts}))
	if got := m.terminalTitleBase; got != want {
		t.Fatalf("terminalTitleBase = %q, want %q", got, want)
	}
}
