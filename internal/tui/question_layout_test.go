package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tools"
)

func questionLayoutModel(width, height int) *Model {
	m := NewModelWithSize(nil, width, height)
	m.mode = ModeQuestion
	options := make([]tools.QuestionOption, 12)
	for i := range options {
		options[i] = tools.QuestionOption{Label: fmt.Sprintf("Choice %d", i+1), Description: strings.Repeat("A longer explanation of this choice. ", 12) + "Description end."}
	}
	m.question = questionState{
		request: &QuestionRequest{Item: tools.QuestionItem{Header: "Choice", Question: strings.Repeat("Please select an option. ", 8), Options: options}},
		input:   newQuestionTextarea(width),
	}
	return &m
}

func TestQuestionLayoutFitsAndFollowsEveryOption(t *testing.T) {
	for _, size := range [][2]int{{30, 12}, {40, 12}, {80, 24}, {120, 40}, {160, 50}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := questionLayoutModel(size[0], size[1])
			for i := range 12 {
				if i == 0 {
					m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
				} else {
					m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
				}
				dialog := m.renderQuestionDialog()
				if lipgloss.Width(dialog) > size[0]-1 || lipgloss.Height(dialog) > size[1] {
					t.Fatalf("dialog dimensions %dx%d exceed terminal %dx%d", lipgloss.Width(dialog), lipgloss.Height(dialog), size[0], size[1])
				}
				if !strings.Contains(ansi.Strip(dialog), fmt.Sprintf("Choice %d", i+1)) {
					t.Fatalf("focused choice %d is not visible", i+1)
				}
				if !strings.Contains(strings.ToLower(ansi.Strip(dialog)), "decline") {
					t.Fatal("decline action is not visible")
				}
			}
		})
	}
}

func TestQuestionPagingReadsDescriptionWithoutSelectingAnotherOption(t *testing.T) {
	m := questionLayoutModel(40, 12)
	_ = m.renderQuestionDialog()
	found := false
	for range 100 {
		if strings.Contains(ansi.Strip(m.renderQuestionDialog()), "Description end.") {
			found = true
			break
		}
		m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}))
	}
	if !found || m.question.cursor != 0 || len(m.question.selected) != 0 {
		t.Fatalf("paging failed: found=%t cursor=%d selections=%d", found, m.question.cursor, len(m.question.selected))
	}
	m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m.applyTerminalSize(80, 24, false)
	dialog := m.renderQuestionDialog()
	if lipgloss.Height(dialog) > 24 || !strings.Contains(ansi.Strip(dialog), "Choice 1") {
		t.Fatal("resize did not restore the active option to view")
	}
}

func TestQuestionResizeKeepsCustomAnswerCursorVisible(t *testing.T) {
	m := questionLayoutModel(120, 40)
	m.question.custom = true
	m.question.input.SetValue("first line\nsecond line\nlast line")
	m.question.input.Focus()
	m.question.input.CursorEnd()
	m.applyTerminalSize(40, 12, false)
	dialog := m.renderQuestionDialog()
	if lipgloss.Height(dialog) > 12 || lipgloss.Width(dialog) > 39 {
		t.Fatal("custom answer dialog exceeds terminal bounds")
	}
	if !strings.Contains(ansi.Strip(dialog), "last line") {
		t.Fatalf("custom answer cursor line is clipped: line=%d height=%d offset=%d\n%s", m.question.input.Line(), m.question.input.Height(), m.question.input.ScrollYOffset(), ansi.Strip(dialog))
	}
}
