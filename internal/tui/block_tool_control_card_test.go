package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/markdownrender"
	"github.com/keakon/chord/internal/tui/syntax"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestDelegateWorkerDropsRuntimeNote(t *testing.T) {
	// The Worker section renders the handle's structured fields; the runtime's
	// notes are the model's diagnostics and must not join that field list.
	note := "Note: ignored unrecognized parameter(s): args.unknown_extra.flag"
	block := &Block{
		ID:            5,
		Type:          BlockToolCall,
		ToolName:      tools.NameDelegate,
		Content:       `{"description":"review the card styles","agent_type":"reviewer"}`,
		ResultContent: `{"status":"started","task_id":"adhoc-7","agent_id":"expert-12","message":"running in background"}` + "\n" + note,
		ResultNotes:   []string{note},
		ResultDone:    true,
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(plain, "↳ Task id: adhoc-7") {
		t.Fatalf("expected the handle fields to render; got:\n%s", plain)
	}
	if strings.Contains(plain, "Note:") {
		t.Fatalf("did not expect the runtime note in the Worker section; got:\n%s", plain)
	}
}

func TestDelegateErrorDoesNotShowDoneSummaryLabel(t *testing.T) {
	block := &Block{
		Type:         BlockToolCall,
		ToolName:     tools.NameDelegate,
		ResultDone:   true,
		ResultStatus: message.ToolStatusError,
		DoneSummary:  "done summary",
	}
	if got := formatToolResultSummaryLine(block); got != "" {
		t.Fatalf("summary = %q, want empty summary for errored delegate", got)
	}
}

func TestCompleteCardAlwaysRendersItsFullSummaryUnderABareHeader(t *testing.T) {
	// Completion cards are always expanded behind a bare tool-name header:
	// the summary is the body's prose section, not a header preview.
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "complete",
		Content:                `{"summary":"Status: success\nChanges: line one\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight\nline nine\nline ten\nline eleven\nline twelve"}`,
		ResultContent:          "Status: success\nChanges: line one\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight\nline nine\nline ten\nline eleven\nline twelve",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(88, ""), "\n"))
	if !strings.Contains(joined, "line twelve") {
		t.Fatalf("expected the Complete card to render its whole summary; got:\n%s", joined)
	}
	if !strings.Contains(joined, "✓ complete") {
		t.Fatalf("expected Complete header to show the bare tool name; got:\n%s", joined)
	}
	if strings.Contains(joined, "▸") || strings.Contains(joined, "▾") {
		t.Fatalf("expected Complete card to drop the disclosure marker; got:\n%s", joined)
	}
	if strings.Count(joined, "Status: success") < 1 {
		t.Fatalf("expected the summary to stay visible; got:\n%s", joined)
	}
	if block.ToggleAtWidth(88) {
		t.Fatalf("expected Complete card to ignore the expand/collapse toggle")
	}
}

func TestExpandedCompleteRendersSummaryMarkdownAndStructuredDetails(t *testing.T) {
	ApplyTheme(uitheme.Default())
	contentMarkdown = markdownrender.New(currentStyles.Palette)

	args := `{"summary":"## Result\n\nImplemented **streaming** support.\n\n` + "```go\\nfmt.Println(1)\\n```" + `","files_changed":["internal/tui/block_tool_render_generic.go"]}`
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameComplete,
		RawArgs:                args,
		Content:                args,
		ResultContent:          "Implemented streaming support.",
		ResultDone:             true,
		ResultStatus:           message.ToolStatusSuccess,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"Result", "Implemented streaming support.", "fmt.Println(1)", "Files changed:", "internal/tui/block_tool_render_generic.go"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected expanded Complete card to contain %q, got:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "Implemented streaming support.") != 1 {
		t.Fatalf("expected Complete summary to render once instead of repeating the result, got:\n%s", joined)
	}
}

func TestExpandedEscalateRendersReasonMarkdownWithoutRepeatingResult(t *testing.T) {
	ApplyTheme(uitheme.Default())
	contentMarkdown = markdownrender.New(currentStyles.Palette)

	args := `{"reason":"## Decision needed\n\nChoose **one** migration strategy."}`
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameEscalate,
		RawArgs:                args,
		Content:                args,
		ResultContent:          "Escalation sent: Choose one migration strategy.",
		ResultDone:             true,
		ResultStatus:           message.ToolStatusSuccess,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"Decision needed", "Choose one migration strategy."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected expanded Escalate card to contain %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Escalation sent:") {
		t.Fatalf("expected successful Escalate result to stay hidden when reason is present, got:\n%s", joined)
	}
}

func TestProseControlCardFallsBackToContentForRestoredArgs(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameComplete,
		Content:                `{"summary":"Restored **summary**"}`,
		ResultDone:             true,
		ResultStatus:           message.ToolStatusSuccess,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(88, ""), "\n"))
	if !strings.Contains(joined, "Restored summary") {
		t.Fatalf("expected restored Complete args to render from Content, got:\n%s", joined)
	}
}

// The notify card used to build its header inline instead of going through
// buildToolHeaderLine. It now shares the helper, so this pins both halves of
// the badge rule it has to keep: a card queued by the scheduler shows the badge,
// and a running one does not.
func TestNotifyCardHeaderFollowsTheSharedQueuedBadgeRule(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                         1,
		Type:                       BlockToolCall,
		ToolName:                   tools.NameNotify,
		Content:                    `{"message":"build finished"}`,
		ToolExecutionState:         agent.ToolCallExecutionStateQueued,
		ToolQueuedByExecutionEvent: true,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected the queued notify card to carry the badge; got:\n%s", joined)
	}

	block.ToolExecutionState = agent.ToolCallExecutionStateRunning
	block.ToolQueuedByExecutionEvent = false
	block.InvalidateCache()
	running := terminaltext.StripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if strings.Contains(running, "Queued") {
		t.Fatalf("a running notify card must not carry the queued badge; got:\n%s", running)
	}
}

func TestDelegateAndControlCardsEscapeStructuredFields(t *testing.T) {
	cases := []struct {
		name  string
		block *Block
		want  []string
	}{
		{
			name: "delegate",
			block: &Block{
				ID:            1,
				Type:          BlockToolCall,
				ToolName:      "delegate",
				Collapsed:     false,
				Content:       `{"description":"run \u001b[32mworker\u001b[0m","agent_type":"\u001b[36mreviewer\u001b[0m"}`,
				ResultContent: `{"status":"started","task_id":"adhoc-1","agent_id":"\u001b[31mreviewer-1\u001b[0m","message":"\u001b[33mrunning\u001b[0m"}`,
				ResultDone:    true,
			},
			want: []string{`\x1b[32mworker\x1b[0m`, `\x1b[36mreviewer\x1b[0m`, `\x1b[31mreviewer-1\x1b[0m`, `\x1b[33mrunning\x1b[0m`},
		},
		{
			name: "cancel",
			block: &Block{
				ID:            2,
				Type:          BlockToolCall,
				ToolName:      "cancel",
				Collapsed:     false,
				Content:       `{"target_task_id":"adhoc-7","reason":"\u001b[35mstop now\u001b[0m"}`,
				ResultContent: `{"status":"stopped","task_id":"adhoc-7","agent_id":"\u001b[31mreviewer-2\u001b[0m","message":"\u001b[33mstopped\u001b[0m"}`,
				ResultDone:    true,
			},
			want: []string{`\x1b[35mstop now\x1b[0m`, `\x1b[31mreviewer-2\x1b[0m`, `\x1b[33mstopped\x1b[0m`},
		},
		{
			name: "notify",
			block: &Block{
				ID:            3,
				Type:          BlockToolCall,
				ToolName:      "notify",
				Collapsed:     false,
				Content:       `{"target_task_id":"adhoc-5","message":"\u001b[36mplease continue\u001b[0m","kind":"\u001b[34mreply\u001b[0m"}`,
				ResultContent: `{"status":"delivered","task_id":"adhoc-5","agent_id":"\u001b[31mreviewer-3\u001b[0m","message":"\u001b[33mdelivered\u001b[0m"}`,
				ResultDone:    true,
			},
			want: []string{`\x1b[36mplease continue\x1b[0m`, `\x1b[34mreply\x1b[0m`, `\x1b[31mreviewer-3\x1b[0m`, `\x1b[33mdelivered\x1b[0m`},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			joined := terminaltext.StripANSI(strings.Join(tt.block.Render(120, ""), "\n"))
			if strings.ContainsRune(joined, '\x1b') {
				t.Fatalf("expected %s card to not contain raw ESC: %q", tt.name, joined)
			}
			for _, want := range tt.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("expected %s card to contain %q, got:\n%s", tt.name, want, joined)
				}
			}
		})
	}
}

func TestTaskCardAlwaysRendersItsBodyUnderABareHeader(t *testing.T) {
	// Delegation cards are always expanded: the header is the bare tool
	// name (+ agent type), and the description it used to summarize belongs
	// to the body's own section.
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delegate",
		Collapsed:              true,
		Content:                `{"description":"review tests\ncheck coverage\nupdate docs","agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2","message":"running in background"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "✓ delegate (reviewer)") {
		t.Fatalf("expected Delegate header to show the tool name and agent type; got:\n%s", joined)
	}
	if strings.Contains(joined, "▸") || strings.Contains(joined, "▾") {
		t.Fatalf("expected Delegate card to drop the disclosure marker; got:\n%s", joined)
	}
	for _, want := range []string{"↳ Description:", "review tests", "check coverage", "update docs", "↳ Worker:", "↳ Agent id: reviewer-2", "↳ Task id: adhoc-7"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected Delegate body to contain %q; got:\n%s", want, joined)
		}
	}
	// The full machine-readable task id is shown on exactly one field row;
	// the readable "#N" short form is reserved for compact titles. Counting
	// the bare id would misfire when the args body or the runtime note
	// legally mentions the same id, so the row itself is the assertion
	// target (a second row would mean the handle section rendered twice).
	if got := strings.Count(joined, "↳ Task id: adhoc-7"); got != 1 {
		t.Fatalf("expected Delegate body to render exactly one task id field row; got %d:\n%s", got, joined)
	}
	// The collapsed flag no longer hides anything: the card renders the same
	// body either way.
	if block.ToggleAtWidth(90) {
		t.Fatalf("expected Delegate card to ignore the expand/collapse toggle")
	}
}

func TestExpandedTaskShowsDescriptionAndWorkerWithTaskID(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delegate",
		Collapsed:              false,
		Content:                `{"description":"review tests","agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Description:") {
		t.Fatalf("expected expanded Delegate to show description heading; got:\n%s", joined)
	}
	if !strings.Contains(joined, "review tests") {
		t.Fatalf("expected expanded Delegate to show description content; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Worker:") {
		t.Fatalf("expected expanded Delegate to show Worker section; got:\n%s", joined)
	}
	// The worker area echoes the machine-readable task id in full, adhoc-
	// prefix included; the readable "#N" form is reserved for compact titles.
	if !strings.Contains(joined, "↳ Task id: adhoc-7") {
		t.Fatalf("expected expanded Delegate worker area to include the full task_id; got:\n%s", joined)
	}
}

func TestExpandedTaskDescriptionUsesPlainTextForMarkdownLookingContent(t *testing.T) {
	ApplyTheme(uitheme.Default())
	desc := "## Plan\n- item one\n- item two\n\n```go\nfmt.Println(\"ok\")\n```"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delegate",
		Content:                `{"description":` + strconv.Quote(desc) + `,"agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Description:") {
		t.Fatalf("expected expanded Delegate to show description heading; got:\n%s", joined)
	}
	for _, want := range []string{"## Plan", "- item one", "- item two", "```go", `fmt.Println("ok")`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected Delegate description to keep literal markdown-looking text %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "• item one") {
		t.Fatalf("delegate description should not render markdown bullets; got:\n%s", joined)
	}
}

func TestCancelledGenericToolOmitsDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"echo hi"}`,
		ResultContent:          "Cancelled",
		ResultStatus:           message.ToolStatusCancelled,
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "shell echo hi") {
		t.Fatalf("expected shell header to remain visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected generic cancelled card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestCancelledSpecialToolOmitsDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "question",
		Content:       `{"questions":[{"header":"log","question":"paste log"}]}`,
		ResultContent: "Cancelled",
		ResultStatus:  message.ToolStatusCancelled,
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "question") {
		t.Fatalf("expected question header to remain visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected special cancelled card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestDoneCallRejectedUsesCrossAndSimplifiedReason(t *testing.T) {
	ApplyTheme(uitheme.Default())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "done",
		ResultDone:    true,
		ResultStatus:  message.ToolStatusCancelled,
		ResultContent: "Done rejected: require all checks to pass before exit and include verification results",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(plain, "✗ done") {
		t.Fatalf("expected rejected Done to use failure icon, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Rejected:") || !strings.Contains(plain, "require all checks to pass before exit and include") ||
		!strings.Contains(plain, "verification results") {
		t.Fatalf("expected simplified rejected reason text, got:\n%s", plain)
	}
	for _, unwanted := range []string{"Status:", "Done rejected:"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected rejected Done to omit %q, got:\n%s", unwanted, plain)
		}
	}
}

func TestDoneCallAutoRejectedUsesCrossAndSimplifiedReason(t *testing.T) {
	ApplyTheme(uitheme.Default())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "done",
		ResultDone:    true,
		ResultStatus:  message.ToolStatusSuccess,
		ResultContent: "Done rejected automatically: loop exit conditions are not satisfied yet: open TODO items remain. Finish the remaining work before calling Done again.",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "✗ done") {
		t.Fatalf("expected auto-rejected Done to use failure icon, got:\n%s", plain)
	}
	for _, want := range []string{"↳ Rejected:", "loop exit conditions are not satisfied yet: open TODO items remain.", "Finish the remaining", "work before calling Done again."} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected auto-rejected Done render to contain %q, got:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"Status:", "Done rejected automatically:"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected auto-rejected Done to omit %q, got:\n%s", unwanted, plain)
		}
	}
}

func TestDoneCallRejectedRendersReportAndSingleReason(t *testing.T) {
	ApplyTheme(uitheme.Default())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "done",
		ResultDone:    true,
		ResultStatus:  message.ToolStatusSuccess,
		ResultContent: "Done rejected: coverage must be >= 70% before exiting",
		DoneReport:    "## Completion status\nReport body stays visible",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(72, ""), "\n"))
	for _, want := range []string{"Completion status", "Report body stays visible", "↳ Rejected:", "coverage must be >= 70% before"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rejected Done render to contain %q, got:\n%s", want, plain)
		}
	}
	if strings.Count(plain, "↳ Rejected:") != 1 {
		t.Fatalf("expected exactly one rejected reason line, got:\n%s", plain)
	}
	if strings.Contains(plain, "Done rejected:") || strings.Contains(plain, "Status:") {
		t.Fatalf("expected rejected Done render to omit raw rejection/status, got:\n%s", plain)
	}
}

func TestDoneCallAcceptedOmitsStatusAndRejectedReason(t *testing.T) {
	ApplyTheme(uitheme.Default())

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "done",
		ResultDone:    true,
		ResultStatus:  message.ToolStatusSuccess,
		ResultContent: "done",
		DoneReport:    "## Summary\n- shipped\n- verified",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	for _, want := range []string{"✓ done", "Summary", "• shipped", "• verified"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected accepted Done render to contain %q, got:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"Status:", "↳ Rejected:", "Done rejected:"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("expected accepted Done render to omit %q, got:\n%s", unwanted, plain)
		}
	}
}

func TestDoneCallRendersDoneReportMarkdown(t *testing.T) {
	ApplyTheme(uitheme.Default())

	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     "done",
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
		DoneReport:   "## Summary\n- shipped\n- verified",
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	for _, want := range []string{"Summary", "• shipped", "• verified"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered Done report to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestDoneCallHeaderKeepsBareToolLine(t *testing.T) {
	ApplyTheme(uitheme.Default())

	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "done",
		ResultDone:        true,
		ResultStatus:      message.ToolStatusSuccess,
		ResultContent:     "done",
		DoneReport:        "## Completion status\n改动已全部提交",
		PersistedDuration: 19 * time.Second,
	}

	lines := stripANSILines(block.renderDoneCall(100, ""))
	var header string
	for _, line := range lines {
		if strings.Contains(line, "done") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatalf("expected a header line naming the tool, got:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(header, "Completion status") || strings.Contains(header, "改动已全部提交") {
		t.Fatalf("expected header to omit the report summary, got %q", header)
	}
	for _, line := range lines {
		if strings.Contains(line, "⏱") {
			t.Fatalf("expected done card to omit the elapsed suffix, got:\n%s", strings.Join(lines, "\n"))
		}
	}
}

func TestDoneCallUsesProseWidthForReportCard(t *testing.T) {
	ApplyTheme(uitheme.Default())

	width := maxProseWidth + 80
	block := &Block{
		ID:           1,
		Type:         BlockToolCall,
		ToolName:     "done",
		ResultDone:   true,
		ResultStatus: message.ToolStatusSuccess,
		DoneReport:   "## Summary\nAll requested work is complete.",
	}

	lines := block.renderDoneCall(width, "")
	got := 0
	for _, line := range stripANSILines(lines) {
		got = max(got, terminaltext.Width(line))
	}

	metrics := newDoneToolCardMetrics(width)
	style := metrics.blockStyle
	want := style.GetMarginLeft() + style.GetPaddingLeft() + metrics.cardWidth + style.GetPaddingRight() + style.GetMarginRight()
	if cardRailPrefix("tool", false) != "" {
		want++
	}
	if got != want {
		t.Fatalf("done card width = %d, want %d", got, want)
	}

	// Tool-call cards now share the same prose cap as done/report cards (the
	// width-unification fix), so the done card is no longer wider than a generic
	// tool card — they match by design.
	generic := newToolCardMetrics(width)
	if metrics.cardWidth != generic.cardWidth {
		t.Fatalf("done card width = %d, want equal to generic tool card width %d (unified prose cap)", metrics.cardWidth, generic.cardWidth)
	}
	if metrics.contentWidth > maxProseWidth {
		t.Fatalf("done content width = %d, want no more than %d", metrics.contentWidth, maxProseWidth)
	}
}

func TestDoneReportCodeBlockKeepsTextWidthCap(t *testing.T) {
	ApplyTheme(uitheme.Default())
	contentMarkdown = markdownrender.New(currentStyles.Palette)

	metrics := newDoneToolCardMetrics(maxProseWidth + 80)
	var hl *syntax.Highlighter
	lines := renderRichMarkdownContent("```text\nhello\n```", metrics.contentWidth, &hl)

	var codeLine string
	for _, line := range lines {
		if strings.Contains(terminaltext.StripANSI(line), "hello") {
			codeLine = line
			break
		}
	}
	if codeLine == "" {
		t.Fatalf("missing rendered code line in %#v", stripANSILines(lines))
	}
	if got := terminaltext.Width(codeLine); got != maxTextWidth {
		t.Fatalf("done report code block width = %d, want %d; line=%q", got, maxTextWidth, codeLine)
	}
}

func TestTodoCallErrorKeepsValidEntriesVisible(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameTodoWrite,
		Content:       `{"todos":[{"id":"1","content":"first task","status":"pending"},{"id":2,"content":"second task","status":"pending"}]}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match todo_write schema: args.todos[1].id must be a string, got number 2",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderTodoCall(120, ""), "\n"))
	if !strings.Contains(plain, "first task") {
		t.Fatalf("expected valid todo entry to stay visible next to a malformed one, got:\n%s", plain)
	}
	if !strings.Contains(plain, "arguments do not match todo_write schema") {
		t.Fatalf("expected the validation error to stay visible, got:\n%s", plain)
	}
}

func TestEscalateCallErrorDropsDisclosureGlyphAndKeepsReason(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEscalate,
		Content:       `{"reason":"Need approval to rotate credentials","files_changed":"src/auth.go"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match escalate schema: args.files_changed must be an array, got string \"src/auth.go\"",
	}
	// Always expanded: the flags no longer gate the body.

	plain := terminaltext.StripANSI(strings.Join(block.renderProseControlCall(140, ""), "\n"))

	// A failed Complete/Escalate card shows its report and the envelope
	// without being opened, so the header carries no ▸ glyph.
	if strings.Contains(plain, "▸") || strings.Contains(plain, "▾") {
		t.Fatalf("expected the error escalate card to drop the disclosure glyph, got:\n%s", plain)
	}
	if !strings.Contains(plain, "Need approval to rotate credentials") {
		t.Fatalf("expected the escalate reason to stay visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Error:") {
		t.Fatalf("expected the error envelope in the body, got:\n%s", plain)
	}
}

func TestNotifyExpandedEscapesANSIRichMessageAndResult(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "notify",
		Collapsed:              false,
		Content:                `{"target_task_id":"adhoc-5","message":"\u001b[36mreply now\u001b[0m","kind":"reply"}`,
		ResultContent:          "\x1b[33mdelivered\x1b[0m",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected notify card to not contain raw ESC: %q", joined)
	}
	for _, want := range []string{`\x1b[36mreply now\x1b[0m`, `\x1b[33mdelivered\x1b[0m`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected notify card to contain %q, got:\n%s", want, joined)
		}
	}
}

func TestCancelSubAgentCollapsedDoesNotShowRawJSON(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "cancel",
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-7","reason":"task superseded"}`,
		ResultContent:          `{"status":"stopped","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected cancel collapsed view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "cancel") {
		t.Fatalf("expected cancel header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "#7") {
		t.Fatalf("expected cancel to show readable target (#7 not adhoc-7); got:\n%s", joined)
	}
	if strings.Contains(joined, "adhoc-7") {
		t.Fatalf("expected cancel collapsed view to not expose adhoc- prefix; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Stopped") {
		t.Fatalf("expected cancel to show stopped semantic; got:\n%s", joined)
	}
}

func TestCancelSubAgentExpandedShowsStructuredDetails(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "cancel",
		Collapsed:              false,
		Content:                `{"target_task_id":"adhoc-7","reason":"task superseded"}`,
		ResultContent:          `{"status":"stopped","task_id":"adhoc-7","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected cancel expanded view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "cancel") {
		t.Fatalf("expected cancel header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "↳ Reason:") || !strings.Contains(joined, "task superseded") {
		t.Fatalf("expected cancel expanded to show reason; got:\n%s", joined)
	}
	// The two-layer contract holds inside one card: the header keeps the
	// readable "#7" short form while the expanded Result section echoes the
	// machine-readable handle values in full ("adhoc-7").
	if !strings.Contains(joined, "#7") {
		t.Fatalf("expected cancel header to keep the readable target (#7); got:\n%s", joined)
	}
	if !strings.Contains(joined, "↳ Result:") {
		t.Fatalf("expected cancel expanded to show a Result section; got:\n%s", joined)
	}
	for _, want := range []string{"↳ Status: stopped", "↳ Task id: adhoc-7", "↳ Agent id: reviewer-2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected cancel expanded Result to contain %q; got:\n%s", want, joined)
		}
	}
}

// TestCancelExpandedHandleRendersFullTaskIDOnceWithoutDuplicateStatus pins
// the expanded Cancel contract end to end: the Result section echoes the
// handle's machine-readable values (full task id, "adhoc-" prefix included),
// and the status is reported exactly once — the top-level "↳ Status:"
// summary is dropped because the nested status field row already carries it.
func TestCancelExpandedHandleRendersFullTaskIDOnceWithoutDuplicateStatus(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "cancel",
		Collapsed:              false,
		Content:                `{"target_task_id":"adhoc-9","reason":"workflow changed"}`,
		ResultContent:          `{"status":"stopped","task_id":"adhoc-9","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if got := strings.Count(joined, "adhoc-9"); got != 1 {
		t.Fatalf("expected the full task id to appear exactly once (field row), got %d:\n%s", got, joined)
	}
	if !strings.Contains(joined, "↳ Task id: adhoc-9") {
		t.Fatalf("expected expanded Result to carry the full task_id row; got:\n%s", joined)
	}
	if got := strings.Count(strings.ToLower(joined), "stopped"); got != 1 {
		t.Fatalf("expected the status to appear exactly once, got %d:\n%s", got, joined)
	}
	if strings.Contains(joined, "↳ Status: Stopped") {
		t.Fatalf("expanded card must not repeat the status in a top-level summary; got:\n%s", joined)
	}
	if strings.Contains(joined, `"status"`) {
		t.Fatalf("expanded card must not leak raw JSON; got:\n%s", joined)
	}
}

func TestCancelSubAgentShowsCancelledSemantic(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "cancel",
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-3","reason":"user requested"}`,
		ResultContent:          `{"status":"cancelled","task_id":"adhoc-3","agent_id":"worker-1"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Cancelled") {
		t.Fatalf("expected cancel to show cancelled semantic; got:\n%s", joined)
	}
}

// TestCollapsedCancelCardInlinesStatusOnHeader pins the folded Cancel contract:
// the status joins the header — after the readable target and before the reason
// — so the card is a single line instead of spending a "↳ Stopped" row on a
// status the folded card never renders a Result section for.
func TestCollapsedCancelCardInlinesStatusOnHeader(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameCancel,
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-9","reason":"workflow changed"}`,
		ResultContent:          `{"status":"stopped","task_id":"adhoc-9","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ResultStatus:           message.ToolStatusSuccess,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "cancel #9 · Stopped (workflow changed)") {
		t.Fatalf("expected the folded cancel header to inline the status, got:\n%s", joined)
	}
	if strings.Contains(joined, "↳ Stopped") {
		t.Fatalf("folded cancel must not repeat the status in a body row, got:\n%s", joined)
	}
	if strings.Contains(joined, "adhoc-9") {
		t.Fatalf("folded cancel must keep the readable target, got:\n%s", joined)
	}
}

func TestNotifySubAgentCollapsedDoesNotShowRawJSON(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "notify",
		Collapsed:              true,
		Content:                `{"target_task_id":"adhoc-5","message":"continue with option B","kind":"reply"}`,
		ResultContent:          `{"status":"delivered","task_id":"adhoc-5","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected notify collapsed view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "notify") {
		t.Fatalf("expected notify header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "#5") {
		t.Fatalf("expected notify to show readable target (#5 not adhoc-5); got:\n%s", joined)
	}
	if strings.Contains(joined, "adhoc-5") {
		t.Fatalf("expected notify collapsed view to not expose adhoc- prefix; got:\n%s", joined)
	}
	// The Result section echoes the status field, so a second top-level
	// "↳ Status: Delivered" row would only repeat it.
	if !strings.Contains(joined, "↳ Status: delivered") {
		t.Fatalf("expected notify to show the delivered status; got:\n%s", joined)
	}
	if got := strings.Count(strings.ToLower(joined), "delivered"); got != 1 {
		t.Fatalf("expected the status to appear exactly once, got %d:\n%s", got, joined)
	}
	if !strings.Contains(joined, "reply") {
		t.Fatalf("expected notify to show kind; got:\n%s", joined)
	}
}

func TestNotifySubAgentExpandedShowsStructuredDetails(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "notify",
		Collapsed:              false,
		Content:                `{"target_task_id":"adhoc-5","message":"continue with option B","kind":"reply"}`,
		ResultContent:          `{"status":"delivered","task_id":"adhoc-5","agent_id":"reviewer-2"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, `"status"`) || strings.Contains(joined, `"target_task_id"`) {
		t.Fatalf("expected notify expanded view to not show raw JSON; got:\n%s", joined)
	}
	if !strings.Contains(joined, "notify") {
		t.Fatalf("expected notify header to show tool name; got:\n%s", joined)
	}
	if !strings.Contains(joined, "↳ Message:") || !strings.Contains(joined, "continue with option B") {
		t.Fatalf("expected notify expanded to show message; got:\n%s", joined)
	}
	if !strings.Contains(joined, "↳ Status: delivered") {
		t.Fatalf("expected notify to show the delivered status; got:\n%s", joined)
	}
}

func TestNotifySubAgentShowsQueuedSemantic(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                         1,
		Type:                       BlockToolCall,
		ToolName:                   "notify",
		Collapsed:                  true,
		Content:                    `{"target_task_id":"adhoc-slot","message":"continue","kind":"follow_up"}`,
		ToolExecutionState:         agent.ToolCallExecutionStateQueued,
		ToolQueuedByExecutionEvent: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected notify queued header badge; got:\n%s", joined)
	}
}

func TestCancelToolShowsQueuedHeaderBadge(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                         1,
		Type:                       BlockToolCall,
		ToolName:                   "cancel",
		Collapsed:                  true,
		Content:                    `{"target_task_id":"adhoc-7","reason":"stopped"}`,
		ToolExecutionState:         agent.ToolCallExecutionStateQueued,
		ToolQueuedByExecutionEvent: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected cancel queued header badge; got:\n%s", joined)
	}
}

func TestControlToolsCollapsedDoNotExposeAdhocTaskID(t *testing.T) {
	ApplyTheme(uitheme.Default())
	tests := []struct {
		name          string
		toolName      string
		content       string
		resultContent string
	}{
		{
			name:          "cancel",
			toolName:      "cancel",
			content:       `{"target_task_id":"adhoc-7","reason":"stopped"}`,
			resultContent: `{"status":"stopped","task_id":"adhoc-7"}`,
		},
		{
			name:          "notify",
			toolName:      "notify",
			content:       `{"target_task_id":"adhoc-5","message":"continue","kind":"reply"}`,
			resultContent: `{"status":"delivered","task_id":"adhoc-5"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               tt.toolName,
				Collapsed:              true,
				Content:                tt.content,
				ResultContent:          tt.resultContent,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			}

			joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
			if strings.Contains(joined, "adhoc-") {
				t.Fatalf("expected %s collapsed view to not expose adhoc- prefix; got:\n%s", tt.toolName, joined)
			}
		})
	}
}

func TestRenderTodoCall_EmptyList(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "todo_write",
		Content:    `{"todos":[]}`,
		ResultDone: true,
		Collapsed:  false,
		Streaming:  false,
	}
	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = terminaltext.StripANSI(line)
	}
	joined := strings.Join(plain, "\n")
	// Empty list should not prominently show "(no items)"
	if strings.Contains(joined, "(no items)") {
		t.Fatalf("empty TodoWrite should not show '(no items)': %q", joined)
	}
	// Should still show the tool header
	if !strings.Contains(joined, "todo_write") {
		t.Fatalf("missing tool header: %q", joined)
	}
}

func TestRenderTodoCall_EmptyListCollapsed(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "todo_write",
		Content:    `{"todos":[]}`,
		ResultDone: true,
		Collapsed:  true,
		Streaming:  false,
	}
	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = terminaltext.StripANSI(line)
	}
	joined := strings.Join(plain, "\n")
	if strings.Contains(joined, "(no items)") {
		t.Fatalf("collapsed empty TodoWrite should not show '(no items)': %q", joined)
	}
}

func TestRenderTodoCall_WithItems(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "todo_write",
		Content:    `{"todos":[{"content":"Task 1","status":"completed"},{"content":"Task 2","status":"pending"}]}`,
		ResultDone: true,
		Collapsed:  false,
		Streaming:  false,
	}
	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = terminaltext.StripANSI(line)
	}
	joined := strings.Join(plain, "\n")
	if !strings.Contains(joined, "Task 1") {
		t.Fatalf("missing Task 1: %q", joined)
	}
	if !strings.Contains(joined, "Task 2") {
		t.Fatalf("missing Task 2: %q", joined)
	}
}

func TestTaskDoneSummaryUsesPlainTextWhenExpanded(t *testing.T) {
	ApplyTheme(uitheme.Default())
	doneSummary := "## Findings\n- item one\n- item two\n\n```go\nfmt.Println(\"ok\")\n```"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "delegate",
		Content:                `{"description":"Investigate task-complete rendering","agent_type":"reviewer"}`,
		ResultContent:          `{"status":"started","task_id":"adhoc-7","agent_id":"reviewer-2","message":"running in background"}`,
		ResultDone:             true,
		ToolCallDetailExpanded: true,
		DoneSummary:            doneSummary,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "Completed:") {
		t.Fatalf("expected Completed section, got:\n%s", joined)
	}
	for _, want := range []string{"## Findings", "- item one", "- item two", "```go", `fmt.Println("ok")`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected done summary to keep literal markdown-looking text %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "• item one") {
		t.Fatalf("done summary should not render markdown bullets, got:\n%s", joined)
	}
}
