package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

func TestQuestionTextOnlySupportsMultilineSubmit(t *testing.T) {
	m := NewModel(nil)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Questions: []tools.QuestionItem{{Header: "log", Question: "paste log"}}},
		input:   newQuestionTextarea(m.width),
	}
	respCh := make(chan QuestionResult, 1)
	m.question.responseCh = respCh
	m.question.input.Focus()

	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "a", Code: 'a'}), m.question.request.Questions[0])
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}), m.question.request.Questions[0])
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "b", Code: 'b'}), m.question.request.Questions[0])
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}), m.question.request.Questions[0])

	select {
	case result := <-respCh:
		if result.Err != nil {
			t.Fatalf("question result err = %v, want nil", result.Err)
		}
		if got := len(result.Answers); got != 1 {
			t.Fatalf("answer count = %d, want 1", got)
		}
		if got := len(result.Answers[0].Selected); got != 1 {
			t.Fatalf("selected count = %d, want 1", got)
		}
		if got := result.Answers[0].Selected[0]; got != "a\nb" {
			t.Fatalf("submitted text = %q, want %q", got, "a\nb")
		}
	default:
		t.Fatal("expected question result after submit")
	}
}

func TestQuestionSubmitPreservesLeadingWhitespace(t *testing.T) {
	m := NewModel(nil)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Questions: []tools.QuestionItem{{Header: "log", Question: "paste log"}}},
		input:   newQuestionTextarea(m.width),
	}
	respCh := make(chan QuestionResult, 1)
	m.question.responseCh = respCh
	m.question.input.Focus()
	m.question.input.SetValue("  foo\n bar")

	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}), m.question.request.Questions[0])

	select {
	case result := <-respCh:
		if result.Err != nil {
			t.Fatalf("question result err = %v, want nil", result.Err)
		}
		if got := result.Answers[0].Selected[0]; got != "  foo\n bar" {
			t.Fatalf("submitted text = %q, want %q", got, "  foo\n bar")
		}
	default:
		t.Fatal("expected question result after submit")
	}
}

func TestQuestionCustomSupportsCtrlJNewline(t *testing.T) {
	m := NewModel(nil)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Questions: []tools.QuestionItem{{
			Header:   "top",
			Question: "paste output",
			Options:  []tools.QuestionOption{{Label: "skip"}},
		}}},
		custom: true,
		input:  newQuestionTextarea(m.width),
	}
	respCh := make(chan QuestionResult, 1)
	m.question.responseCh = respCh
	m.question.input.Focus()

	q := m.question.request.Questions[0]
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}), q)
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModCtrl}), q)
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}), q)
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}), q)

	select {
	case result := <-respCh:
		if result.Err != nil {
			t.Fatalf("question result err = %v, want nil", result.Err)
		}
		if got := len(result.Answers); got != 1 {
			t.Fatalf("answer count = %d, want 1", got)
		}
		if got := result.Answers[0].Selected[0]; got != "x\ny" {
			t.Fatalf("submitted text = %q, want %q", got, "x\ny")
		}
	default:
		t.Fatal("expected question result after submit")
	}
}

func TestQuestionTextInputEscReturnsToOptionsAndClearsDraft(t *testing.T) {
	m := NewModel(nil)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Questions: []tools.QuestionItem{{
			Header:   "top",
			Question: "paste output",
			Options:  []tools.QuestionOption{{Label: "skip"}},
		}}},
		custom: true,
		input:  newQuestionTextarea(m.width),
	}
	m.question.input.Focus()
	m.question.input.SetValue("draft")

	q := m.question.request.Questions[0]
	cmd := m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}), q)
	if cmd != nil {
		t.Fatalf("esc with options should not return cmd, got %#v", cmd)
	}
	if m.question.custom {
		t.Fatal("custom should be false after esc back to options")
	}
	if got := m.question.input.Value(); got != "" {
		t.Fatalf("input value = %q, want empty after esc", got)
	}
}

func TestNewQuestionTextareaConfiguresMultilineKeys(t *testing.T) {
	ta := newQuestionTextarea(80)
	if ta.ShowLineNumbers {
		t.Fatal("question textarea should hide line numbers")
	}
	if ta.Height() != questionInputHeight {
		t.Fatalf("textarea height = %d, want %d", ta.Height(), questionInputHeight)
	}
	if ta.Width() != questionInputWidth(80) {
		t.Fatalf("textarea width = %d, want %d", ta.Width(), questionInputWidth(80))
	}
	keys := ta.KeyMap.InsertNewline.Keys()
	joined := strings.Join(keys, ",")
	if joined != "shift+enter,ctrl+j" {
		t.Fatalf("newline keys = %q, want shift+enter,ctrl+j", joined)
	}
	if got := strings.Join(ta.KeyMap.LineNext.Keys(), ","); got != "down,ctrl+n" {
		t.Fatalf("line-next keys = %q, want down,ctrl+n", got)
	}
	if got := strings.Join(ta.KeyMap.LinePrevious.Keys(), ","); got != "up,ctrl+p" {
		t.Fatalf("line-previous keys = %q, want up,ctrl+p", got)
	}
}

func TestQuestionRequestTextOnlyReturnsFocusCmd(t *testing.T) {
	m := NewModel(nil)

	updated, cmd := m.Update(questionRequestMsg{request: QuestionRequest{Questions: []tools.QuestionItem{{Header: "log", Question: "paste log"}}}})
	if cmd == nil {
		t.Fatal("questionRequestMsg for text-only question should return focus cmd")
	}
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if !model.question.input.Focused() {
		t.Fatal("text-only question input should be focused")
	}
}

func TestQuestionAdvanceToTextOnlyReturnsFocusCmd(t *testing.T) {
	m := NewModel(nil)
	m.question = questionState{
		request: &QuestionRequest{Questions: []tools.QuestionItem{
			{Header: "pick", Question: "choose", Options: []tools.QuestionOption{{Label: "one"}}},
			{Header: "detail", Question: "explain"},
		}},
		selected: map[int]bool{0: true},
		input:    newQuestionTextarea(80),
	}

	cmd := m.submitCurrentQuestion(m.question.request.Questions[0])
	if cmd == nil {
		t.Fatal("advance to text-only question should return focus cmd")
	}
	if !m.question.input.Focused() {
		t.Fatal("next text-only question input should be focused")
	}
}

func TestMakeQuestionFuncUsesRequestScopedResponseChannel(t *testing.T) {
	reqCh := make(chan QuestionRequest, 2)
	qf := MakeQuestionFunc(reqCh, 200*time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		first := <-reqCh
		if first.ResponseCh == nil {
			panic("first.ResponseCh is nil")
		}
		first.ResponseCh <- QuestionResult{Answers: []tools.QuestionAnswer{{Header: "first", Selected: []string{"first"}}}}
		second := <-reqCh
		if second.ResponseCh == nil {
			panic("second.ResponseCh is nil")
		}
		second.ResponseCh <- QuestionResult{Answers: []tools.QuestionAnswer{{Header: "second", Selected: []string{"second"}}}}
	}()

	answers, err := qf(context.Background(), []tools.QuestionItem{{Header: "h1", Question: "q1"}})
	if err != nil {
		t.Fatalf("first question err = %v", err)
	}
	if got := answers[0].Selected[0]; got != "first" {
		t.Fatalf("first question got %q, want first", got)
	}

	answers, err = qf(context.Background(), []tools.QuestionItem{{Header: "h2", Question: "q2"}})
	if err != nil {
		t.Fatalf("second question err = %v", err)
	}
	if got := answers[0].Selected[0]; got != "second" {
		t.Fatalf("second question got %q, want second", got)
	}

	<-done
}

func TestMakeQuestionFuncTimeoutDoesNotBlockNextQuestion(t *testing.T) {
	reqCh := make(chan QuestionRequest, 2)
	firstQ := MakeQuestionFunc(reqCh, 20*time.Millisecond)
	secondQ := MakeQuestionFunc(reqCh, 200*time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		first := <-reqCh
		time.Sleep(50 * time.Millisecond)
		select {
		case first.ResponseCh <- QuestionResult{Answers: []tools.QuestionAnswer{{Header: "first", Selected: []string{"late-first"}}}}:
		default:
		}
		second := <-reqCh
		second.ResponseCh <- QuestionResult{Answers: []tools.QuestionAnswer{{Header: "second", Selected: []string{"second"}}}}
	}()

	if _, err := firstQ(context.Background(), []tools.QuestionItem{{Header: "h1", Question: "q1"}}); err == nil {
		t.Fatal("first question should time out")
	}

	answers, err := secondQ(context.Background(), []tools.QuestionItem{{Header: "h2", Question: "q2"}})
	if err != nil {
		t.Fatalf("second question err = %v", err)
	}
	if got := answers[0].Selected[0]; got != "second" {
		t.Fatalf("second question got %q, want second", got)
	}
	<-done
}

func TestQuestionTextInputSupportsUpDownNavigation(t *testing.T) {
	m := NewModel(nil)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Questions: []tools.QuestionItem{{Header: "log", Question: "paste log"}}},
		input:   newQuestionTextarea(m.width),
	}
	m.question.input.Focus()
	q := m.question.request.Questions[0]

	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "a", Code: 'a'}), q)
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}), q)
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "b", Code: 'b'}), q)
	if got := m.question.input.Line(); got != 1 {
		t.Fatalf("line before navigation = %d, want 1", got)
	}

	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}), q)
	if got := m.question.input.Line(); got != 0 {
		t.Fatalf("line after up = %d, want 0", got)
	}

	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}), q)
	if got := m.question.input.Line(); got != 1 {
		t.Fatalf("line after down = %d, want 1", got)
	}
}

func TestResolveQuestionRestoresInsertModeWithTextareaState(t *testing.T) {
	m := NewModel(nil)
	m.mode = ModeQuestion
	m.question = questionState{
		request:  &QuestionRequest{Questions: []tools.QuestionItem{{Header: "name", Question: "who?"}}},
		prevMode: ModeInsert,
		input:    newQuestionTextarea(80),
	}
	m.imeBeforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	cmd := m.resolveQuestion(QuestionResult{Err: errors.New("cancelled")})
	if cmd == nil {
		t.Fatal("resolveQuestion() returned nil cmd")
	}
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.imePending || m.imePendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.imePending, m.imePendingTarget)
	}
}

func TestQuestionDialogWrapsCurrentOptionDescription(t *testing.T) {
	m := NewModel(nil)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Questions: []tools.QuestionItem{{
			Header:   "Direction",
			Question: "Choose one",
			Options: []tools.QuestionOption{
				{Label: "Option A", Description: "Show the full setup instructions in the dialog so the content wraps across multiple lines instead of being shortened with an ellipsis."},
				{Label: "Option B", Description: "Keep the current setup."},
			},
		}}},
		cursor: 0,
	}

	plain := stripANSI(m.renderQuestionDialog())
	if strings.Contains(plain, "Option A  Show the full setup instructions") {
		t.Fatalf("current option should not keep description on the selected row, got:\n%s", plain)
	}
	if strings.Contains(plain, "shortened with an ellipsis...") {
		t.Fatalf("current option description should wrap instead of truncating, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Show the full setup instructions") || !strings.Contains(plain, "ellipsis.") {
		t.Fatalf("wrapped current option description missing full text, got:\n%s", plain)
	}
}

func TestQuestionDialogQuickSelectHintMatchesOptionCount(t *testing.T) {
	m := NewModel(nil)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Questions: []tools.QuestionItem{{
			Header:   "Direction",
			Question: "Choose one",
			Options: []tools.QuestionOption{
				{Label: "Option A", Description: "desc1"},
				{Label: "Option B", Description: "desc2"},
			},
		}}},
	}

	plain := stripANSI(m.renderQuestionDialog())
	if !strings.Contains(plain, "[1-2] Quick-select") {
		t.Fatalf("quick-select hint should reflect 2 options, got:\n%s", plain)
	}
	if strings.Contains(plain, "[1-9] Quick-select") {
		t.Fatalf("quick-select hint should not advertise 1-9 for 2 options, got:\n%s", plain)
	}
}
