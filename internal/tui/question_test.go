package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

// questionResolverAgent records what a question dialog submits and reports the
// terminal reason the broker settled on, standing in for the real agent in
// tests.
type questionResolverAgent struct {
	loopBusyAgentStub
	accepted bool
	// terminal overrides the reason reported back for an accepted response: a
	// response that loses its race with the deadline comes back as no_response.
	terminal string
	calls    []questionResolveCall
}

type questionResolveCall struct {
	answers   []string
	reason    string
	requestID string
}

func (a *questionResolverAgent) ApplyQuestionOperation(_ context.Context, op agent.QuestionOperation) (agent.QuestionReceipt, error) {
	reason := tools.QuestionOutcomeAnswered
	if op.Operation == agent.QuestionOpDecline {
		reason = tools.QuestionOutcomeDeclined
	}
	if op.Operation == agent.QuestionOpInteract || op.Operation == agent.QuestionOpPresented {
		return agent.QuestionReceipt{Accepted: true}, nil
	}
	a.calls = append(a.calls, questionResolveCall{answers: op.Answers, reason: reason, requestID: op.QuestionID})
	receipt := agent.QuestionReceipt{Accepted: a.accepted, Status: reason, Error: "response was not accepted"}
	if a.terminal != "" {
		receipt.Accepted = false
		receipt.Status = a.terminal
		receipt.Error = "question expired: " + a.terminal
	}
	return receipt, nil
}
func runQuestionCmd(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case questionOperationMsg:
		m.handleQuestionOperation(msg)
	case tea.BatchMsg:
		for _, c := range msg {
			runQuestionCmd(m, c)
		}
	}
}

func TestQuestionTextOnlySupportsMultilineSubmit(t *testing.T) {
	backend := &questionResolverAgent{accepted: true}
	m := NewModel(backend)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request:   &QuestionRequest{Item: tools.QuestionItem{Header: "log", Question: "paste log"}},
		requestID: "req-log",
		input:     newQuestionTextarea(m.width),
	}
	m.question.input.Focus()

	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "a", Code: 'a'}), m.question.request.Item)
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}), m.question.request.Item)
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "b", Code: 'b'}), m.question.request.Item)
	runQuestionCmd(&m, m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}), m.question.request.Item))

	if len(backend.calls) != 1 {
		t.Fatalf("resolve calls = %d, want 1", len(backend.calls))
	}
	call := backend.calls[0]
	if call.requestID != "req-log" || call.reason != tools.QuestionOutcomeAnswered {
		t.Fatalf("resolve call = %+v, want answered for req-log", call)
	}
	if len(call.answers) != 1 || call.answers[0] != "a\nb" {
		t.Fatalf("submitted answers = %#v, want [\"a\\nb\"]", call.answers)
	}
}

func TestQuestionSubmitPreservesLeadingWhitespace(t *testing.T) {
	backend := &questionResolverAgent{accepted: true}
	m := NewModel(backend)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request:   &QuestionRequest{Item: tools.QuestionItem{Header: "log", Question: "paste log"}},
		requestID: "req-log",
		input:     newQuestionTextarea(m.width),
	}
	m.question.input.Focus()
	m.question.input.SetValue("  foo\n bar")

	runQuestionCmd(&m, m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}), m.question.request.Item))

	if len(backend.calls) != 1 || len(backend.calls[0].answers) != 1 {
		t.Fatalf("resolve calls = %+v, want one answer", backend.calls)
	}
	if got := backend.calls[0].answers[0]; got != "  foo\n bar" {
		t.Fatalf("submitted text = %q, want %q", got, "  foo\n bar")
	}
}

func TestQuestionCustomSupportsCtrlJNewline(t *testing.T) {
	backend := &questionResolverAgent{accepted: true}
	m := NewModel(backend)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Item: tools.QuestionItem{
			Header:   "top",
			Question: "paste output",
			Options:  []tools.QuestionOption{{ID: "skip", Label: "skip"}},
		}},
		requestID: "req-top",
		custom:    true,
		input:     newQuestionTextarea(m.width),
	}
	m.question.input.Focus()

	q := m.question.request.Item
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}), q)
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModCtrl}), q)
	_ = m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}), q)
	runQuestionCmd(&m, m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}), q))

	if len(backend.calls) != 1 || len(backend.calls[0].answers) != 1 {
		t.Fatalf("resolve calls = %+v, want one answer", backend.calls)
	}
	if got := backend.calls[0].answers[0]; got != "x\ny" {
		t.Fatalf("submitted text = %q, want %q", got, "x\ny")
	}
}

// TestQuestionSubmitSurfacesRefusedResponse pins that a response the broker no
// longer accepts (the question already closed) is reported instead of looking
// like an accepted answer.
func TestQuestionSubmitSurfacesRefusedResponse(t *testing.T) {
	backend := &questionResolverAgent{accepted: false}
	m := NewModel(backend)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request:   &QuestionRequest{Item: tools.QuestionItem{Header: "target", Question: "which?"}},
		requestID: "req-late",
		input:     newQuestionTextarea(m.width),
	}
	m.question.input.Focus()
	m.question.input.SetValue("late")

	runQuestionCmd(&m, m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}), m.question.request.Item))

	if len(backend.calls) != 1 {
		t.Fatalf("resolve calls = %d, want 1", len(backend.calls))
	}
	if m.question.request == nil {
		t.Fatal("the draft must remain when the response was refused")
	}
	if m.activeToast == nil {
		t.Fatal("a refused response must surface a toast")
	}
	if m.activeToast.Level != "warn" || !strings.Contains(m.activeToast.Message, "not accepted") {
		t.Fatalf("activeToast = %+v, want a warn toast about the refused response", m.activeToast)
	}
}

func TestQuestionSubmitReportsTheWinningTerminalReason(t *testing.T) {
	backend := &questionResolverAgent{accepted: true, terminal: tools.QuestionOutcomeNoResponse}
	m := NewModel(backend)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request:   &QuestionRequest{Item: tools.QuestionItem{Header: "target", Question: "which?"}},
		requestID: "req-expired",
		input:     newQuestionTextarea(m.width),
	}
	m.question.input.Focus()
	m.question.input.SetValue("late")

	runQuestionCmd(&m, m.handleQuestionTextKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}), m.question.request.Item))

	if m.activeToast == nil {
		t.Fatal("a response that lost its race with the deadline must surface a toast")
	}
	if m.activeToast.Level != "warn" || !strings.Contains(m.activeToast.Message, "expired") {
		t.Fatalf("activeToast = %+v, want a warn toast naming the expired question", m.activeToast)
	}
}

func TestQuestionTextInputEscDeclines(t *testing.T) {
	backend := &questionResolverAgent{accepted: true}
	m := NewModelWithSize(backend, 80, 24)
	m.mode = ModeInsert
	m.handleQuestionState(questionEventForTest("q-1", "Choice", "Choose", []string{"one"}, nil, false, time.Time{}, "main").Question)
	m.question.custom = true
	m.question.input.SetValue("draft")
	runQuestionCmd(&m, m.handleQuestionKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})))
	if len(backend.calls) != 1 || backend.calls[0].reason != tools.QuestionOutcomeDeclined {
		t.Fatalf("Esc must decline, calls = %+v", backend.calls)
	}
	if m.mode != ModeInsert || m.question.request != nil {
		t.Fatal("a declined question must close the dialog")
	}
}

func TestNewQuestionTextareaConfiguresMultilineKeys(t *testing.T) {
	ta := newQuestionTextarea(80)
	if ta.ShowLineNumbers {
		t.Fatal("question textarea should hide line numbers")
	}
	if !ta.DynamicHeight {
		t.Fatal("question textarea should use dynamic height")
	}
	if ta.MinHeight != 1 || ta.MaxHeight != questionInputHeight {
		t.Fatalf("textarea height bounds = %d..%d, want 1..%d", ta.MinHeight, ta.MaxHeight, questionInputHeight)
	}
	if ta.Height() != 1 {
		t.Fatalf("textarea initial height = %d, want 1", ta.Height())
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

func TestQuestionTextareaSoftWrapDoesNotIndentContinuation(t *testing.T) {
	ta := newQuestionTextarea(80)
	ta.SetWidth(10)
	ta.SetValue("abcdefghijk")

	plain := stripANSI(strings.TrimSuffix(ta.View(), "\n"))
	lines := strings.Split(plain, "\n")
	if len(lines) != 2 {
		t.Fatalf("wrapped line count = %d, want 2\n%s", len(lines), plain)
	}
	if strings.HasPrefix(lines[1], " ") {
		t.Fatalf("wrapped continuation should not be indented, got %q in:\n%s", lines[1], plain)
	}
	if got := strings.TrimSpace(lines[1]); got != "k" {
		t.Fatalf("wrapped continuation text = %q, want k\n%s", got, plain)
	}
}

func TestQuestionTextareaShrinksToContentHeight(t *testing.T) {
	ta := newQuestionTextarea(80)
	if ta.Height() != 1 {
		t.Fatalf("empty textarea height = %d, want 1", ta.Height())
	}
	ta.SetValue("one\ntwo")
	if ta.Height() != 2 {
		t.Fatalf("two-line textarea height = %d, want 2", ta.Height())
	}
	ta.SetValue("one\ntwo\nthree\nfour\nfive")
	if ta.Height() != questionInputHeight {
		t.Fatalf("long textarea height = %d, want capped at %d", ta.Height(), questionInputHeight)
	}
}

func TestQuestionRequestTextOnlyReturnsFocusCmd(t *testing.T) {
	m := NewModel(nil)

	cmd := m.presentQuestionRequest(questionDialog{request: QuestionRequest{Item: tools.QuestionItem{Header: "log", Question: "paste log"}}}, ModeNormal)
	if cmd == nil {
		t.Fatal("a text-only question request should return a focus cmd")
	}
	if !m.question.input.Focused() {
		t.Fatal("text-only question input should be focused")
	}
}

func TestQuestionTextInputSupportsUpDownNavigation(t *testing.T) {
	m := NewModel(nil)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Item: tools.QuestionItem{Header: "log", Question: "paste log"}},
		input:   newQuestionTextarea(m.width),
	}
	m.question.input.Focus()
	q := m.question.request.Item

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
	m := NewModel(loopBusyAgentStub{})
	m.mode = ModeQuestion
	m.question = questionState{
		request:   &QuestionRequest{Item: tools.QuestionItem{Header: "name", Question: "who?"}},
		requestID: "req-ime",
		prevMode:  ModeInsert,
		input:     newQuestionTextarea(80),
	}
	m.ime.beforeNormal = "zh-orig"
	preventIMEApplyInTests(&m)

	runQuestionCmd(&m, m.resolveQuestion(nil, true))
	if m.mode != ModeInsert {
		t.Fatalf("mode = %v, want ModeInsert", m.mode)
	}
	if !m.ime.pending || m.ime.pendingTarget != "zh-orig" {
		t.Fatalf("pending IME restore = (%v, %q), want (true, zh-orig)", m.ime.pending, m.ime.pendingTarget)
	}
}

func TestQuestionDialogWrapsCurrentOptionDescription(t *testing.T) {
	m := NewModel(nil)
	m.width = 80
	m.mode = ModeQuestion
	m.question = questionState{
		request: &QuestionRequest{Item: tools.QuestionItem{
			Header:   "Direction",
			Question: "Choose one",
			Options: []tools.QuestionOption{
				{ID: "Option A", Label: "Option A", Description: "Show the full setup instructions in the dialog so the content wraps across multiple lines instead of being shortened with an ellipsis."},
				{ID: "Option B", Label: "Option B", Description: "Keep the current setup."},
			},
		}},
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
		request: &QuestionRequest{Item: tools.QuestionItem{
			Header:   "Direction",
			Question: "Choose one",
			Options: []tools.QuestionOption{
				{ID: "Option A", Label: "Option A", Description: "desc1"},
				{ID: "Option B", Label: "Option B", Description: "desc2"},
			},
		}},
	}

	plain := stripANSI(m.renderQuestionDialog())
	if !strings.Contains(plain, "[1-2] quick-select") {
		t.Fatalf("quick-select hint should reflect 2 options, got:\n%s", plain)
	}
	if strings.Contains(plain, "[1-9] quick-select") {
		t.Fatalf("quick-select hint should not advertise 1-9 for 2 options, got:\n%s", plain)
	}
}

// TestQuestionDialogCustomInputPreservesDialogBackground guards the regression
// where the custom-answer textarea rows dropped the dialog background: the
// textarea's View() emits SGR resets that wiped the DialogBg established by the
// border box, leaving the answer rows on the terminal default background.
func TestQuestionDialogCustomInputPreservesDialogBackground(t *testing.T) {
	ApplyTheme(DefaultTheme())

	m := NewModelWithSize(nil, 120, 40)
	m.mode = ModeQuestion
	ta := newQuestionTextarea(80)
	ta.SetValue("alpha query\nbeta view\n")
	m.question = questionState{
		request: &QuestionRequest{Item: tools.QuestionItem{
			Header:   "Direction",
			Question: "Choose one",
		}},
		input:  ta,
		custom: true,
	}

	out := m.renderQuestionDialog()
	if out == "" {
		t.Fatal("expected non-empty question dialog")
	}

	dialogBg := colorOfTheme(currentTheme.DialogBg)

	// The first answer line and a later line must both sit on the dialog
	// background, including the cell that abuts the textarea's trailing pad
	// cells. Each needle is the last character of one answer line and occurs
	// nowhere earlier in the dialog.
	for _, needle := range []string{"y", "w"} {
		line := findRenderedLineContaining(out, needle)
		if line == "" {
			t.Fatalf("missing input line containing %q in dialog: %q", needle, stripANSI(out))
		}
		cell, ok := findRenderedCell(line, needle)
		if !ok {
			t.Fatalf("missing input text cell %q in line: %q", needle, stripANSI(line))
		}
		if !colorsEqual(cell.Style.Bg, dialogBg) {
			t.Fatalf("custom-answer cell %q background = %v, want dialog bg %v", needle, cell.Style.Bg, dialogBg)
		}
	}
}
