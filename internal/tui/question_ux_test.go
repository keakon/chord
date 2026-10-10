package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/tools"
)

func TestQuestionDraftSurvivesOptionToggle(t *testing.T) {
	backend := &questionResolverAgent{accepted: true}
	m := NewModelWithSize(backend, 80, 24)
	m.handleAgentEvent(agentEventMsg{event: questionEventForTest("question-1", "Choice", "Choose a format", []string{"Plain", "Rich"}, nil, false, time.Time{}, "")})
	key := func(code rune) { runQuestionCmd(&m, m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: code}))) }
	key(tea.KeyTab)
	m.question.input.SetValue("custom draft")
	key(tea.KeyTab)
	key(tea.KeyTab)
	if got := m.question.input.Value(); got != "custom draft" {
		t.Fatalf("draft after tab = %q", got)
	}
	key(tea.KeyEnter)
	if len(backend.calls) != 1 || backend.calls[0].requestID != "question-1" || strings.Join(backend.calls[0].answers, "|") != "custom draft" {
		t.Fatalf("unexpected submission: %+v", backend.calls)
	}
	m.handleAgentEvent(agentEventMsg{event: questionEventForTest("question-2", "Details", "Add details", nil, nil, false, time.Time{}, "")})
	if m.question.requestID != "question-2" || m.question.input.Value() != "" || !m.question.input.Focused() {
		t.Fatal("next request must start with a fresh, focused editor")
	}
}

func TestQuestionMultiSelectUsesDisplayOrder(t *testing.T) {
	backend := &questionResolverAgent{accepted: true}
	m := NewModelWithSize(backend, 80, 24)
	q := tools.QuestionItem{Header: "Choices", Question: "Choose", Multiple: true, Options: []tools.QuestionOption{{ID: "First", Label: "First"}, {ID: "Second", Label: "Second"}, {ID: "Third", Label: "Third"}}}
	m.presentQuestionRequest(questionDialog{request: QuestionRequest{Item: q}, requestID: "multi"}, ModeInsert)
	m.question.selected = map[int]bool{2: true, 0: true, 1: false}
	runQuestionCmd(&m, m.submitCurrentQuestion(q))
	if got := strings.Join(backend.calls[0].answers, "|"); got != "First|Third" {
		t.Fatalf("answers = %q", got)
	}
}

func TestQuestionLayoutKeepsSelectionAndEditorVisible(t *testing.T) {
	for _, size := range [][2]int{{32, 6}, {32, 8}, {32, 12}, {40, 16}, {80, 24}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := NewModelWithSize(nil, size[0], size[1])
			options := make([]tools.QuestionOption, 12)
			for i := range options {
				options[i] = tools.QuestionOption{Label: fmt.Sprintf("Option %02d", i), Description: strings.Repeat("Useful detail. ", 12)}
			}
			m.question = questionState{request: &QuestionRequest{Item: tools.QuestionItem{Header: "Choice", Question: strings.Repeat("Read this before choosing. ", 20), Options: options}}, selected: map[int]bool{}, input: newQuestionTextarea(size[0])}
			m.mode = ModeQuestion
			if rendered := stripANSI(m.renderQuestionDialog()); !strings.Contains(rendered, "Read this") || m.question.scrollOffset != 0 {
				t.Fatalf("first view hides the question: %s", rendered)
			}
			for range 11 {
				m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
			}
			rendered := m.renderQuestionDialog()
			if lipgloss.Width(rendered) >= size[0] || lipgloss.Height(rendered) > size[1] {
				t.Fatalf("dialog size %dx%d for %v", lipgloss.Width(rendered), lipgloss.Height(rendered), size)
			}
			if !strings.Contains(stripANSI(rendered), "Option 11") {
				t.Fatalf("focused option hidden:\n%s", rendered)
			}
			m.scrollQuestion(-10000)
			if !strings.Contains(stripANSI(m.renderQuestionDialog()), "Read this") {
				t.Fatal("cannot read the beginning")
			}
			m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
			if !strings.Contains(stripANSI(m.renderQuestionDialog()), "Read this") {
				t.Fatal("an unrelated key should not move the reading position")
			}
			m.question.custom = true
			m.question.input.SetValue("My answer")
			rendered = m.renderQuestionDialog()
			if !strings.Contains(stripANSI(rendered), "My answer") || lipgloss.Height(rendered) > size[1] {
				t.Fatalf("editor hidden or overflow:\n%s", rendered)
			}
			m.width = 40
			m.height = 14
			m.question.custom = false
			m.question.followCursor = false
			rendered = m.renderQuestionDialog()
			if lipgloss.Width(rendered) >= 40 || lipgloss.Height(rendered) > 14 {
				t.Fatal("resized dialog uses stale layout")
			}
		})
	}
}

func TestQuestionPastePreservesMultilineAnswer(t *testing.T) {
	backend := &questionResolverAgent{accepted: true}
	m := NewModelWithSize(backend, 80, 24)
	m.handleAgentEvent(agentEventMsg{event: questionEventForTest("text-answer", "Details", "Add details", nil, nil, false, time.Time{}, "")})
	m.Update(tea.PasteMsg{Content: "first line\nsecond line"})
	runQuestionCmd(&m, m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	if len(backend.calls) != 1 || strings.Join(backend.calls[0].answers, "|") != "first line\nsecond line" {
		t.Fatalf("unexpected pasted answer: %+v", backend.calls)
	}
}

func TestQuestionDeadlineAndEditorFitSixRows(t *testing.T) {
	m := NewModelWithSize(nil, 32, 6)
	m.question = questionState{
		request: &QuestionRequest{Item: tools.QuestionItem{Header: "Details", Question: "Add details"}},
		input:   newQuestionTextarea(32), deadline: time.Now().Add(time.Minute),
	}
	m.question.input.SetValue("My answer")
	rendered := m.renderQuestionDialog()
	plain := stripANSI(rendered)
	if lipgloss.Height(rendered) > 6 || !strings.Contains(plain, "My answer") || !strings.Contains(plain, "Esc") || !strings.Contains(plain, "s · Details") {
		t.Fatalf("editor, controls or countdown do not fit: %s", plain)
	}
}

func TestQuestionUnicodeAndDeadlineFitShortTerminal(t *testing.T) {
	m := NewModelWithSize(nil, 32, 12)
	m.question = questionState{
		request:  &QuestionRequest{Item: tools.QuestionItem{Header: "选择格式", Question: strings.Repeat("请选择输出格式。", 12), Options: []tools.QuestionOption{{ID: "纯文本", Label: "纯文本", Description: strings.Repeat("便于阅读。", 20)}}}},
		selected: map[int]bool{}, input: newQuestionTextarea(32), custom: true, deadline: time.Now().Add(time.Minute),
	}
	m.question.input.SetValue("自定义回答\n第二行\n第三行\n第四行")
	rendered := m.renderQuestionDialog()
	if lipgloss.Width(rendered) >= 32 || lipgloss.Height(rendered) > 12 {
		t.Fatalf("dialog size %dx%d", lipgloss.Width(rendered), lipgloss.Height(rendered))
	}
	if !strings.Contains(stripANSI(rendered), "Closes in") || !strings.Contains(stripANSI(rendered), "Esc") {
		t.Fatalf("deadline or escape hint hidden:\n%s", rendered)
	}
}
