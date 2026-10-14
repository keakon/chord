package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestQuestionStructuredPromptAndAnswerEscapesANSIRichText(t *testing.T) {
	for _, custom := range []bool{false, true} {
		payload := `{"status":"resolved","answers":[{"header":"HDR","selected":["A"],"selected_ids":["a"],"outcome":"answered"}]}`
		if custom {
			payload = `{"status":"resolved","answers":[{"header":"HDR","selected":["\u001b[33mcustom\u001b[0m"],"outcome":"answered"}]}`
		}
		block := &Block{
			ID: 1, Type: BlockToolCall, ToolName: tools.NameQuestion,
			Content:       `{"questions":[{"header":"\u001b[31mHDR\u001b[0m","question":"Pick \u001b[32mone\u001b[0m","options":[{"id":"a","label":"\u001b[34mA\u001b[0m","description":"\u001b[35mdesc\u001b[0m"}]}]}`,
			ResultContent: payload, ResultPayload: payload, ResultDone: true,
		}
		joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
		if strings.ContainsRune(joined, '\x1b') {
			t.Fatalf("raw ESC: %q", joined)
		}
		for _, want := range []string{`\x1b[31mHDR\x1b[0m`, `\x1b[32mone\x1b[0m`, `\x1b[34mA\x1b[0m`, `\x1b[35mdesc\x1b[0m`} {
			if !strings.Contains(joined, want) {
				t.Fatalf("missing %q:\n%s", want, joined)
			}
		}
		if custom {
			if !strings.Contains(joined, `Custom: \x1b[33mcustom\x1b[0m`) {
				t.Fatalf("custom answer lost:\n%s", joined)
			}
		} else if !strings.Contains(joined, `✓ 1. \x1b[34mA\x1b[0m`) {
			t.Fatalf("selected option lost:\n%s", joined)
		}
	}
}

func TestQuestionCallMarksSelectedOptionInline(t *testing.T) {
	ApplyTheme(uitheme.Default())

	answers := `{"status":"resolved","answers":[{"header":"Messaging platform","selected":["Chat App"],"selected_ids":["chat"],"outcome":"answered"}]}`
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: "question",
		Content: `{"questions":[{"header":"Messaging platform","question":"Which one should we use first?","options":[` +
			`{"id":"email","label":"Email","description":"One inbox per workspace"},` +
			`{"id":"chat","label":"Chat App","description":"Single workspace with direct messages"}` +
			`]}]}`,
		ResultContent: answers,
		ResultPayload: answers,
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "✓ 2. Chat App") {
		t.Fatalf("expected selected option to be marked inline, got:\n%s", plain)
	}
	lines := strings.Split(plain, "\n")
	var emailLine, chatLine string
	for _, line := range lines {
		switch {
		case strings.Contains(line, "Email"):
			emailLine = line
		case strings.Contains(line, "Chat App"):
			chatLine = line
		}
	}
	if emailLine == "" || chatLine == "" {
		t.Fatalf("expected both option lines, got:\n%s", plain)
	}
	emailBefore, _, emailOK := strings.Cut(emailLine, "1.")
	chatBefore, _, chatOK := strings.Cut(chatLine, "2.")
	if !emailOK || !chatOK {
		t.Fatalf("expected option numbers in both lines, got:\n%s", plain)
	}
	if got, want := ansi.StringWidth(emailBefore), ansi.StringWidth(chatBefore); got != want {
		t.Fatalf("expected option numbers to align, got email=%d chat=%d\n%s", got, want, plain)
	}
	if strings.Contains(plain, "↳ Answer:") || strings.Contains(plain, "↳ Answers:") {
		t.Fatalf("expected inline answer rendering without separate answer section, got:\n%s", plain)
	}
	if strings.Contains(plain, "Custom: Enabled") {
		t.Fatalf("expected custom capability line to be replaced/omitted after inline result, got:\n%s", plain)
	}
}

// The runtime records the payload and its notes apart, so the card parses the
// tool's own output instead of the combined model-visible text, and shows the
// notes from their own field.
func TestQuestionCallParsesCleanPayloadAndShowsNotesApart(t *testing.T) {
	ApplyTheme(uitheme.Default())

	answers := `{"status":"resolved","answers":[{"header":"document layout","selected":["Match the reference layout (recommended)"],"selected_ids":["match"],"outcome":"answered"}]}`
	note := `Note: ignored earlier duplicate parameter value(s): args.questions[0].header; the last values were used`

	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: "question",
		Content: `{"questions":[{"header":"document layout","question":"How should the target document be organized?","options":[` +
			`{"id":"match","label":"Match the reference layout (recommended)","description":"Mimic the reference formatting"},` +
			`{"id":"order","label":"Keep the markdown order","description":"Align each item with its model answer"}` +
			`]}]}`,
		ResultContent: answers + "\n" + note,
		ResultPayload: answers,
		ResultNotes:   []string{note},
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "✓ 1. Match the reference layout (recommended)") {
		t.Fatalf("expected the selection to be marked inline, got:\n%s", plain)
	}
	if strings.Contains(plain, `[{"header"`) {
		t.Fatalf("expected no raw answers JSON once the selection renders inline, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Note: ignored earlier duplicate") {
		t.Fatalf("expected the diagnostic note to stay visible, got:\n%s", plain)
	}
	if strings.Contains(plain, "Custom: Enabled") {
		t.Fatalf("expected no custom-answer hint once answers resolved, got:\n%s", plain)
	}
}

// A Question card shows every parameter in its body and carries a rejected
// call's schema message in the ↳ Error block, which already names the
// offending field and the value it rejected. Repeating the argument on the
// header line adds nothing and puts a second, competing "header" on screen.
func TestQuestionCallKeepsArgDiagnosticsOffTheHeader(t *testing.T) {
	ApplyTheme(uitheme.Default())

	ignored := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"document layout","question":"Q","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}`,
		ResultContent: `{"status":"resolved","answers":[{"header":"document layout","selected":["A"],"selected_ids":["a"],"outcome":"answered"}]}`,
		ResultPayload: `{"status":"resolved","answers":[{"header":"document layout","selected":["A"],"selected_ids":["a"],"outcome":"answered"}]}`,
		ResultDone:    true,
		Audit: &message.ToolArgsAudit{
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.questions[0].header",
				ValueJSON: `"document structure"`,
				Reason:    message.IgnoredToolArgReasonShadowed,
			}},
		},
	}
	ignoredLines := ignored.Render(100, "")
	if plain := terminaltext.StripANSI(strings.Join(ignoredLines, "\n")); strings.Contains(plain, "questions[0].header=document structure") {
		t.Fatalf("expected the shadowed header value to stay off the header line, got:\n%s", plain)
	}
	if header := terminaltext.StripANSI(renderedLineContaining(t, ignoredLines, "question")); strings.Contains(header, "document structure") {
		t.Fatalf("expected the question header line to stay clean, got:\n%s", header)
	}

	invalid := &Block{
		ID:            2,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"H","question":"Q"}]}`,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match question schema: args.questions[0].header must be a string, got number 7",
		ResultDone:    true,
		Audit: &message.ToolArgsAudit{
			InvalidArgs: []message.InvalidToolArg{{
				Path:      "args.questions[0].header",
				ValueJSON: `7`,
				Reason:    message.InvalidToolArgReasonInvalid,
			}},
		},
	}
	invalidLines := invalid.Render(100, "")
	header := terminaltext.StripANSI(renderedLineContaining(t, invalidLines, "question"))
	if strings.Contains(header, "questions[0].header") {
		t.Fatalf("expected an invalid arg to stay off the question header line, got:\n%s", header)
	}
	if strings.Contains(header, "·") {
		t.Fatalf("expected the question header line to carry no diagnostic suffix, got:\n%s", header)
	}
	// Nothing is lost: the parameters render in the body and the schema
	// message — which names the field and the rejected value — renders below.
	plain := terminaltext.StripANSI(strings.Join(invalidLines, "\n"))
	if !strings.Contains(plain, "  ↳ H:") {
		t.Fatalf("expected the question to keep rendering its parameters below the header, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Error:") {
		t.Fatalf("expected the schema failure to render as an error section, got:\n%s", plain)
	}
	// The error text wraps at this card width, so match within one line.
	if !strings.Contains(plain, "args.questions[0].header must be a string") {
		t.Fatalf("expected the schema message naming the invalid argument, got:\n%s", plain)
	}
}

func TestQuestionCallRendersCustomAnswerInline(t *testing.T) {
	ApplyTheme(uitheme.Default())

	answers := `{"status":"resolved","answers":[{"header":"Project location","selected":["/tmp/sample-project\n/tmp/sample-cache"]}]}`
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: "question",
		Content: `{"questions":[{"header":"Project location","question":"Where should it go?","options":[` +
			`{"id":"sibling","label":"Sibling directory","description":"For example /home/user/projects/sample-gateway"},` +
			`{"id":"project","label":"Project subdirectory","description":"For example /home/user/projects/sample-app/gateway"}` +
			`]}]}`,
		ResultContent: answers,
		ResultPayload: answers,
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "Custom: /tmp/sample-project") {
		t.Fatalf("expected inline custom answer, got:\n%s", plain)
	}
	if !strings.Contains(plain, "            /tmp/sample-cache") {
		t.Fatalf("expected multiline custom continuation indent, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Answer:") || strings.Contains(plain, "↳ Answers:") {
		t.Fatalf("expected inline custom rendering without separate answer section, got:\n%s", plain)
	}
}

func TestQuestionCallRendersMultilineAnswerWithContinuationIndent(t *testing.T) {
	ApplyTheme(uitheme.Default())

	answers := `{"status":"resolved","answers":[{"header":"top output","selected":["line 1\nline 2\nline 3"]}]}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"top output","question":"Paste a few lines from top"}]}`,
		ResultContent: answers,
		ResultPayload: answers,
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(70, ""), "\n"))
	if !strings.Contains(plain, "Answer: line 1") {
		t.Fatalf("expected inline Question answer, got:\n%s", plain)
	}
	if !strings.Contains(plain, "            line 2") {
		t.Fatalf("expected continuation indent for second line, got:\n%s", plain)
	}
	if !strings.Contains(plain, "            line 3") {
		t.Fatalf("expected continuation indent for third line, got:\n%s", plain)
	}
}

func TestQuestionCallRendersSingleObjectPromptAndStructuredAnswer(t *testing.T) {
	ApplyTheme(uitheme.Default())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":{"header":"log","question":"paste log"}}`,
		ResultContent: `{"status":"resolved","answers":[{"header":"log","selected":["first line"],"outcome":"answered"}]}`,
		ResultPayload: `{"status":"resolved","answers":[{"header":"log","selected":["first line"],"outcome":"answered"}]}`,
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(70, ""), "\n"))
	for _, want := range []string{"log", "paste log", "Answer: first line"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected question card to contain %q, got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, `selected`) {
		t.Fatalf("expected structured answer to render inline instead of raw JSON, got:\n%s", plain)
	}
}

func TestQuestionCallPreservesWhitespaceInMultilineAnswer(t *testing.T) {
	ApplyTheme(uitheme.Default())

	answers := `{"status":"resolved","answers":[{"header":"top output","selected":["PID  CPU   COMMAND\n1    10%   sample-app"]}]}`
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"top output","question":"Paste a few lines from top"}]}`,
		ResultContent: answers,
		ResultPayload: answers,
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(plain, "Answer: PID  CPU   COMMAND") {
		t.Fatalf("expected aligned spaces in first line, got:\n%s", plain)
	}
	if !strings.Contains(plain, "            1    10%   sample-app") {
		t.Fatalf("expected aligned spaces in continuation line, got:\n%s", plain)
	}
}

func TestQuestionCallFallbackResultPreservesMultilineText(t *testing.T) {
	ApplyTheme(uitheme.Default())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"log","question":"paste log"}]}`,
		ResultContent: "first line\nsecond line\nthird line",
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(70, ""), "\n"))
	if !strings.Contains(plain, "↳ ✓") {
		t.Fatalf("expected fallback success header, got:\n%s", plain)
	}
	for _, want := range []string{"first line", "second line", "third line"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected fallback multiline result to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestQuestionCallErrorKeepsValidEntriesVisible(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameQuestion,
		Content:       `{"questions":[{"header":"Scope","question":"Which package should own the helper?","options":[{"label":"internal/tools"},{"label":"internal/tui"}]},{"header":7,"question":"broken"}]}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match question schema: args.questions[1].header must be a string, got number 7",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderQuestionCall(120, ""), "\n"))
	if !strings.Contains(plain, "Which package should own the helper?") {
		t.Fatalf("expected valid question to stay visible next to a malformed one, got:\n%s", plain)
	}
	if !strings.Contains(plain, "internal/tools") {
		t.Fatalf("expected valid question options to stay visible, got:\n%s", plain)
	}
}

func TestQuestionCardOmitsElapsedWaitTime(t *testing.T) {
	// The Question card's elapsed is the time the user spent answering, so it
	// must not be presented as the tool's own cost.
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          tools.NameQuestion,
		Content:           `{"questions":[{"header":"Scope","question":"Which package should own the helper?","options":[{"label":"internal/tools"}]}]}`,
		ResultPayload:     `[{"selected":["internal/tools"]}]`,
		ResultDone:        true,
		ResultStatus:      message.ToolStatusSuccess,
		PersistedDuration: 42 * time.Second,
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderQuestionCall(120, ""), "\n"))
	if strings.Contains(plain, "⏱") || strings.Contains(plain, "42s") {
		t.Fatalf("expected question card to omit the elapsed suffix, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Which package should own the helper?") {
		t.Fatalf("expected the question body to stay intact, got:\n%s", plain)
	}
}

func TestQuestionFallbackResultEscapesANSIRichOutput(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"mode","question":"Pick one"}]}`,
		ResultDone:    true,
		ResultContent: "\x1b[35muser typed answer\x1b[0m",
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected question card to not contain raw ESC: %q", joined)
	}
	if !strings.Contains(joined, `\x1b[35muser typed answer\x1b[0m`) {
		t.Fatalf("expected question fallback result to show ANSI literally, got:\n%s", joined)
	}
}

func TestRenderQuestionDialogEscapesANSIRichPromptAndDescriptions(t *testing.T) {
	m := NewModel(nil)
	m.width = 100
	m.theme = uitheme.Default()
	m.question.request = &QuestionRequest{Item: tools.QuestionItem{
		Header:   "\u001b[31munsafe\u001b[0m",
		Question: "line1\n\u001b[32mline2\u001b[0m",
		Options:  []tools.QuestionOption{{Label: "one", Description: "\u001b[34mdesc\u001b[0m"}},
	}}
	m.question.input = newQuestionTextarea(m.width)

	view := terminaltext.StripANSI(m.renderQuestionDialog())
	if strings.ContainsRune(view, '\x1b') {
		t.Fatalf("expected question dialog to not contain raw ESC: %q", view)
	}
	for _, want := range []string{`\x1b[31munsafe\x1b[0m`, `\x1b[32mline2\x1b[0m`, `\x1b[34mdesc\x1b[0m`} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected question dialog to contain %q, got:\n%s", want, view)
		}
	}
}

func TestQuestionToolShowsQueuedHeaderBadge(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                         1,
		Type:                       BlockToolCall,
		ToolName:                   "question",
		Collapsed:                  false,
		Content:                    `{"questions":[{"header":"Confirm","question":"Continue?","multiple":false,"options":[{"label":"Continue","description":"Continue execution"}]}]}`,
		ToolExecutionState:         agent.ToolCallExecutionStateQueued,
		ToolQueuedByExecutionEvent: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected question queued header badge; got:\n%s", joined)
	}
}
