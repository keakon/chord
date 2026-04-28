package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

// handleQuestionKey processes key events while in ModeQuestion.
func (m *Model) handleQuestionKey(msg tea.KeyMsg) tea.Cmd {
	if m.question.request == nil {
		return nil
	}
	q := m.question.request.Questions[m.question.currentQ]

	// If custom text input is focused, route most keys to the textarea.
	if m.question.custom || len(q.Options) == 0 {
		return m.handleQuestionTextKey(msg, q)
	}

	return m.handleQuestionOptionKey(msg, q)
}

// handleQuestionOptionKey handles keys while navigating the option list.
func (m *Model) handleQuestionOptionKey(msg tea.KeyMsg, q tools.QuestionItem) tea.Cmd {
	optCount := len(q.Options) + 1 // +1 for the "custom" virtual entry

	switch msg.String() {
	// Navigation
	case "j", "down":
		if m.question.cursor < optCount-1 {
			m.question.cursor++
		}
		return nil
	case "k", "up":
		if m.question.cursor > 0 {
			m.question.cursor--
		}
		return nil

	// Toggle selection (multi-select). Space may be reported as " " or "space" (tea.KeySpace).
	case " ", "space":
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

	// Confirm / submit
	case "enter":
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
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
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
	case "tab":
		m.question.custom = true
		m.question.input.Focus()
		m.recalcViewportSize()
		return textareaBlinkCmd()

	// Cancel
	case "esc":
		return m.cancelQuestion()
	}

	return nil
}

// handleQuestionTextKey handles keys while the custom text input is focused.
func (m *Model) handleQuestionTextKey(msg tea.KeyMsg, q tools.QuestionItem) tea.Cmd {
	switch msg.String() {
	case "enter":
		text := m.question.input.Value()
		if strings.TrimSpace(text) == "" {
			return nil // ignore empty submit
		}
		answer := tools.QuestionAnswer{
			Header:   q.Header,
			Selected: []string{text},
		}
		return m.advanceQuestion(answer)

	case "esc":
		if len(q.Options) > 0 {
			// Escape goes back to option selection.
			m.question.custom = false
			m.question.input.Blur()
			m.question.input.SetValue("")
			m.question.input.MoveToBegin()
			m.recalcViewportSize()
			return nil
		}
		// No options → Esc cancels
		return m.cancelQuestion()

	case "tab":
		if len(q.Options) > 0 {
			// Tab goes back to option selection.
			m.question.custom = false
			m.question.input.Blur()
			m.question.input.SetValue("")
			m.question.input.MoveToBegin()
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

// submitCurrentQuestion collects selected options and advances.
func (m *Model) submitCurrentQuestion(q tools.QuestionItem) tea.Cmd {
	var selected []string
	for idx := range m.question.selected {
		if idx >= 0 && idx < len(q.Options) {
			selected = append(selected, q.Options[idx].Label)
		}
	}
	if len(selected) == 0 {
		return nil // nothing selected, no-op
	}
	answer := tools.QuestionAnswer{
		Header:   q.Header,
		Selected: selected,
	}
	return m.advanceQuestion(answer)
}

// advanceQuestion records the answer and either moves to the next question
// or resolves the entire dialog.
func (m *Model) advanceQuestion(answer tools.QuestionAnswer) tea.Cmd {
	m.question.answers = append(m.question.answers, answer)

	m.question.currentQ++
	if m.question.currentQ < len(m.question.request.Questions) {
		// Prepare state for the next question
		m.question.cursor = 0
		m.question.selected = make(map[int]bool)
		m.question.custom = false
		m.question.input.SetValue("")
		m.question.input.MoveToBegin()
		m.question.input.Blur()

		var cmd tea.Cmd
		// If next question is text-only, auto-focus input.
		nextQ := m.question.request.Questions[m.question.currentQ]
		if len(nextQ.Options) == 0 {
			cmd = m.question.input.Focus()
		}
		m.recalcViewportSize()
		return cmd
	}

	// All questions answered — send results back.
	return m.resolveQuestion(QuestionResult{Answers: m.question.answers})
}

// cancelQuestion dismisses the dialog and returns empty answers.
func (m *Model) cancelQuestion() tea.Cmd {
	return m.resolveQuestion(QuestionResult{
		Err: fmt.Errorf("user cancelled"),
	})
}

// flattenQuestionAnswers converts TUI question answers to the []string form
// expected by agent.ResolveQuestion (selected labels or free-text per question).
func flattenQuestionAnswers(answers []tools.QuestionAnswer) []string {
	var out []string
	for _, a := range answers {
		out = append(out, a.Selected...)
	}
	return out
}

// resolveQuestion sends the result back via the request-scoped response channel
// (in-process) or agent.ResolveQuestion (remote), clears state, restores the
// previous mode, and re-subscribes to the question channel.
func (m *Model) resolveQuestion(result QuestionResult) tea.Cmd {
	if m.question.request == nil {
		return nil
	}

	if m.question.requestID != "" {
		// Remote mode: send response to server via agent.ResolveQuestion.
		answers := flattenQuestionAnswers(result.Answers)
		m.agent.ResolveQuestion(answers, result.Err != nil, m.question.requestID)
	} else if m.question.responseCh != nil {
		// In-process local mode: send to the request-scoped response channel.
		select {
		case m.question.responseCh <- result:
		default:
		}
	}

	prevMode := m.question.prevMode
	m.question = questionState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	titleCmd := m.syncTerminalTitleState()

	// Re-subscribe to question channel and restore focus.
	cmds := []tea.Cmd{waitForQuestionRequest(m.questionCh), titleCmd}
	if m.displayState == stateBackground {
		cmds = append(cmds, m.updateBackgroundIdleSweepState())
	}
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	if prevMode == ModeInsert {
		cmds = append(cmds, m.input.Focus())
	}
	return tea.Batch(cmds...)
}

func textareaBlinkCmd() tea.Cmd {
	return tea.Cmd(nil)
}
