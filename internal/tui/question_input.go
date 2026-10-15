package tui

import (
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// handleQuestionKey processes key events while in ModeQuestion.
func (m *Model) handleQuestionKey(msg tea.KeyMsg) tea.Cmd {
	cmd := m.handleQuestionKeyInput(msg)
	var interact tea.Cmd
	// Selection keys can submit directly. Only send an interaction operation
	// when this key leaves the dialog open, avoiding concurrent state commits.
	if msg.String() != "esc" && !m.question.submitting {
		interact = m.beginQuestionInteraction()
	}
	return tea.Batch(interact, cmd)
}

func (m *Model) handleQuestionKeyInput(msg tea.KeyMsg) tea.Cmd {
	if m.question.request == nil {
		return nil
	}
	if msg.String() == "esc" {
		return m.cancelQuestion()
	}
	if m.question.submitting {
		return nil
	}
	if msg.String() == "pgup" || msg.String() == "pgdown" {
		delta := max(m.question.visibleBodyHeight, 1)
		if msg.String() == "pgup" {
			delta = -delta
		}
		if m.scrollQuestion(delta) {
			return nil
		}
		// The question body has no more room: page the conversation instead,
		// matching the pointer-aware wheel behavior.
		if msg.String() == "pgup" {
			return m.pageTranscript(-1)
		}
		return m.pageTranscript(1)
	}
	q := m.question.request.Item

	// If custom text input is focused, route most keys to the textarea.
	if m.question.custom || len(q.Options) == 0 {
		return m.handleQuestionTextKey(msg, q)
	}

	return m.handleQuestionOptionKey(msg, q)
}

// handleQuestionOptionKey handles keys while navigating the option list.
func (m *Model) handleQuestionOptionKey(msg tea.KeyMsg, q tools.QuestionItem) tea.Cmd {
	key := msg.String()
	if key == "j" || key == "k" || key == "up" || key == "down" || key == "space" || (len(key) == 1 && key >= "1" && key <= "9") {
		m.question.followCursor = true
	}
	optCount := len(q.Options) + 1 // +1 for the "custom" virtual entry
	if msg.Key().Code == tea.KeySpace {
		idx := m.question.cursor
		if idx == len(q.Options) {
			// Space on "custom" entry → switch to text input
			m.question.custom = true
			m.question.input.Focus()
			m.recalcViewportSize()
			return textareaBlinkCmd()
		}
		if q.Multiple {
			if m.question.selected[idx] {
				delete(m.question.selected, idx)
			} else {
				m.question.selected[idx] = true
			}
		}
		return nil
	}

	switch {
	// Navigation
	case msg.Key().Code == tea.KeyDown || msg.String() == "j":
		if m.question.cursor < optCount-1 {
			m.question.cursor++
		}
		return nil
	case msg.Key().Code == tea.KeyUp || msg.String() == "k":
		if m.question.cursor > 0 {
			m.question.cursor--
		}
		return nil

	// Confirm / submit
	case isPlainKey(msg, tea.KeyEnter):
		idx := m.question.cursor
		// "Custom" virtual entry
		if idx == len(q.Options) {
			m.question.custom = true
			m.question.input.Focus()
			m.recalcViewportSize()
			return textareaBlinkCmd()
		}
		if q.Multiple {
			// Enter in multi-select mode: if nothing toggled, toggle current + submit
			if len(m.question.selected) == 0 {
				m.question.selected[idx] = true
			}
			return m.submitCurrentQuestion(q)
		}
		// Single-select: pick current
		m.question.selected = map[int]bool{idx: true}
		return m.submitCurrentQuestion(q)

	// Number keys 1-9 for quick selection
	case len(msg.String()) == 1 && msg.String() >= "1" && msg.String() <= "9":
		num := int(msg.String()[0] - '0')
		idx := num - 1
		if idx < len(q.Options) {
			if q.Multiple {
				if m.question.selected[idx] {
					delete(m.question.selected, idx)
				} else {
					m.question.selected[idx] = true
				}
			} else {
				m.question.selected = map[int]bool{idx: true}
				return m.submitCurrentQuestion(q)
			}
		}
		return nil

	// Tab → switch to custom input
	case msg.Key().Code == tea.KeyTab:
		m.question.custom = true
		m.question.input.Focus()
		m.recalcViewportSize()
		return textareaBlinkCmd()

	// Cancel
	case msg.Key().Code == tea.KeyEscape:
		return m.cancelQuestion()
	}

	return nil
}

// handleQuestionTextKey handles keys while the custom text input is focused.
func (m *Model) handleQuestionTextKey(msg tea.KeyMsg, q tools.QuestionItem) tea.Cmd {
	switch {
	case isPlainKey(msg, tea.KeyEnter):
		text := m.question.input.Value()
		if strings.TrimSpace(text) == "" {
			return nil // ignore empty submit
		}
		return m.resolveQuestion([]string{text})

	case msg.Key().Code == tea.KeyEscape:
		return m.cancelQuestion()

	case msg.Key().Code == tea.KeyTab:
		if len(q.Options) > 0 {
			// Tab goes back to option selection.
			m.question.custom = false
			m.question.followCursor = true
			m.question.input.Blur()
			m.recalcViewportSize()
			return nil
		}
		return nil

	default:
		var cmd tea.Cmd
		m.question.input, cmd = m.question.input.Update(msg)
		return cmd
	}
}

// submitCurrentQuestion submits selected options in their display order.
func (m *Model) submitCurrentQuestion(q tools.QuestionItem) tea.Cmd {
	var selected []string
	for i, option := range q.Options {
		if m.question.selected[i] {
			selected = append(selected, option.ID)
		}
	}
	if len(selected) == 0 {
		return nil // nothing selected, no-op
	}
	return m.resolveQuestion(selected)
}

// submitQuestionOperation guards against a missing or already-submitting
// question, derives the operation ID from the current question, and dispatches
// the operation. Callers supply only the operation and its payload.
func (m *Model) submitQuestionOperation(op agent.QuestionOperation) tea.Cmd {
	if m.question.request == nil || m.question.submitting {
		return nil
	}
	op.OperationID = questionOperationID(m.question.requestID, op.Operation)
	m.question.submitting = true
	return m.questionOperation(op)
}

// cancelQuestion withdraws the question's requirement. A question can only be
// answered or cancelled — there is no "declined" state that keeps the
// requirement pending while hiding the dialog — so Esc cancels it outright, and
// it is the only binding that does. The withdrawal carries an explicit
// user-facing sentence naming the question, so the transcript reads like any
// other cancelled tool call instead of a raw question ID. Every other key,
// Ctrl+W included, stays an ordinary editing key: a terminal that intercepts it
// never delivers it here, so app-side removal would only disable word deletion
// where the binding does work.
func (m *Model) cancelQuestion() tea.Cmd {
	userText := "Cancelled this question"
	if header := strings.TrimSpace(m.question.request.Item.Header); header != "" {
		userText += ": " + header
	}
	return m.submitQuestionOperation(agent.QuestionOperation{
		Operation:  agent.QuestionOpWithdraw,
		QuestionID: m.question.requestID,
		UserText:   userText,
	})
}

// resolveQuestion submits an explicit answer. The dialog closes after Core
// acknowledgment; failed submissions keep the dialog for retry.
func (m *Model) resolveQuestion(answers []string) tea.Cmd {
	return m.submitQuestionOperation(agent.QuestionOperation{
		Operation:  agent.QuestionOpAnswer,
		QuestionID: m.question.requestID,
		Answers:    answers,
		Custom:     m.question.custom || len(m.question.request.Item.Options) == 0,
	})
}

func textareaBlinkCmd() tea.Cmd {
	return tea.Cmd(nil)
}
