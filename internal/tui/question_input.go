package tui

import (
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
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

	// Decline
	case msg.Key().Code == tea.KeyEscape:
		return m.declineQuestion()
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
		answer := tools.QuestionAnswer{
			Header:   q.Header,
			Selected: []string{text},
		}
		return m.advanceQuestion(answer)

	case msg.Key().Code == tea.KeyEscape:
		if len(q.Options) > 0 {
			// Escape goes back to option selection.
			m.question.custom = false
			m.question.input.Blur()
			m.question.input.SetValue("")
			m.question.input.MoveToBegin()
			m.recalcViewportSize()
			return nil
		}
		// No options → Esc declines
		return m.declineQuestion()

	case msg.Key().Code == tea.KeyTab:
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
	return m.resolveQuestion(m.question.answers, false)
}

// declineQuestion dismisses the dialog with an explicit declined outcome, so
// the model can tell a refusal apart from an unanswered timeout.
func (m *Model) declineQuestion() tea.Cmd {
	return m.resolveQuestion(nil, true)
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

// resolveQuestion submits the dialog's result to the agent, clears the dialog,
// restores the previous mode, and presents the next queued dialog. declined
// marks an explicit refusal (Esc) rather than a submitted answer. The broker
// decides the terminal state, so a response can lose to the deadline or to a
// newer message; whatever reason won is surfaced as a toast instead of being
// silently dropped.
func (m *Model) resolveQuestion(answers []tools.QuestionAnswer, declined bool) tea.Cmd {
	if m.question.request == nil {
		return nil
	}

	reason := tools.QuestionOutcomeAnswered
	submitted := flattenQuestionAnswers(answers)
	if declined {
		reason = tools.QuestionOutcomeDeclined
		submitted = nil
	}

	terminal, accepted := m.agent.ResolveQuestion(submitted, reason, m.question.requestID)
	var refusalCmd tea.Cmd
	if !accepted || terminal != reason {
		refusalCmd = m.enqueueToast(questionTerminalToast(terminal), "warn")
	}

	prevMode := m.question.prevMode
	m.question = questionState{}
	m.terminalTitleRequestSeen = false
	m.recalcViewportSize()
	titleCmd := m.syncTerminalTitleState()

	// Present the next queued dialog or restore the pre-dialog mode.
	cmds := []tea.Cmd{titleCmd}
	if refusalCmd != nil {
		cmds = append(cmds, refusalCmd)
	}
	if m.displayState == stateBackground {
		cmds = append(cmds, m.updateBackgroundIdleSweepState())
	}
	return m.finishDialog(prevMode, cmds...)
}

func textareaBlinkCmd() tea.Cmd {
	return tea.Cmd(nil)
}

// questionTerminalToast explains a response that did not become the question's
// outcome. A response loses either its request (already closed, unknown) or its
// race with the deadline or a newer message; naming the winning reason tells
// the user what actually happened after the dialog is gone.
func questionTerminalToast(terminal string) string {
	switch terminal {
	case tools.QuestionOutcomeNoResponse:
		return "Question expired before the response arrived; it closed with no answer"
	case tools.QuestionOutcomeSuperseded:
		return "Question was superseded by a newer message"
	case agent.QuestionResolvedReasonCancelled:
		return "Question was cancelled"
	case agent.QuestionResolvedReasonError:
		return "Question was closed because the agent shut down"
	default:
		return "Question response not accepted: the question already closed"
	}
}
