package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestCompactContextCallRendersStructuredSections(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   tools.NameCompactContext,
		Content:    `{"active_objective":"完成用户的最终汇报：按主题提交已完成（5 个纯净本地 commit, 作者 keakon, 未 push）","completed":["5 纯净提交已建（本地未 push, 作者 keakon）35725e09 fix(cli)","fix(agent) downs hift 边界 1d3a4b96"],"decisions":["D1+D4 合并 downs hift 主体:避免二个分支 hug"],"open_issues":["唯一一次会话失败报告，已 autosquash"],"next_step":"先按输出最终完成报告：5 个提交列表 + 验证","state_files":[".chord/notes/20260904-continuation-review.md"]}`,
		RawArgs:    `{"active_objective":"完成用户的最终汇报：按主题提交已完成（5 个纯净本地 commit, 作者 keakon, 未 push）","completed":["5 纯净提交已建（本地未 push, 作者 keakon）35725e09 fix(cli)","fix(agent) downs hift 边界 1d3a4b96"],"decisions":["D1+D4 合并 downs hift 主体:避免二个分支 hug"],"open_issues":["唯一一次会话失败报告，已 autosquash"],"next_step":"先按输出最终完成报告：5 个提交列表 + 验证","state_files":[".chord/notes/20260904-continuation-review.md"]}`,
		ResultDone: true,
	}
	block.ToolCallDetailExpanded = true

	plain := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))

	// Each section gets its own "↳" header instead of a flat key-value dump.
	for _, want := range []string{"↳ Objective:", "↳ Completed:", "↳ Decisions:", "↳ Open issues:", "↳ Next:", "↳ State files:"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected section header %q in compact_context card, got:\n%s", want, plain)
		}
	}
	// Array fields must be split into bullets, not serialized as a single
	// JSON list value sitting on the "key:" line.
	if strings.Contains(plain, "completed:") && !strings.Contains(plain, "Completed") {
		t.Fatalf("expected array field to render under the Completed section, got:\n%s", plain)
	}
	for _, bullet := range []string{
		"• 5 纯净提交已建",
		"• fix(agent) downs hift 边界 1d3a4b96",
		"• D1+D4 合并 downs hift 主体",
		"• 唯一一次会话失败报告",
		"• .chord/notes/20260904-continuation-review.md",
	} {
		if !strings.Contains(plain, bullet) {
			t.Fatalf("expected bullet %q in compact_context card, got:\n%s", bullet, plain)
		}
	}
	// The generic "completed:" inline key-value line must NOT appear.
	if strings.Contains(plain, "completed: [") {
		t.Fatalf("expected completed array not to be serialized as inline JSON, got:\n%s", plain)
	}
}

func TestCompactContextCallAlwaysShowsEverySectionUnderABareHeader(t *testing.T) {
	// The checkpoint card is always expanded behind a bare tool-name
	// header: the objective it used to summarize is the body's own
	// ↳ Objective: section.
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   tools.NameCompactContext,
		Content:    `{"active_objective":"完成用户的最终汇报","completed":["a","b","c"],"decisions":["d"],"open_issues":["o"],"next_step":"先按输出最终完成报告","state_files":["n.md"]}`,
		RawArgs:    `{"active_objective":"完成用户的最终汇报","completed":["a","b","c"],"decisions":["d"],"open_issues":["o"],"next_step":"先按输出最终完成报告","state_files":["n.md"]}`,
		ResultDone: true,
	}
	plain := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))

	for _, want := range []string{"↳ Objective:", "↳ Completed:", "↳ Decisions:", "↳ Open issues:", "↳ Next:", "↳ State files:"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected the always-expanded card to show %q, got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "compact_context 完成用户的最终汇报") {
		t.Fatalf("expected the objective to stay off the header, got:\n%s", plain)
	}
	if strings.Contains(plain, "▸") || strings.Contains(plain, "▾") {
		t.Fatalf("expected the disclosure marker to be gone, got:\n%s", plain)
	}
	if block.ToggleAtWidth(140) {
		t.Fatalf("expected the checkpoint card to ignore the expand/collapse toggle")
	}
}

func TestCompactContextCallStaysBareWhileArgumentsStream(t *testing.T) {
	// While the arguments stream, nothing decodes: the header's "N chars
	// received" progress is the whole card, so the six sections never flash
	// half-decoded and then fold away when the call finishes.
	streaming := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameCompactContext,
		RawArgs:  `{"active_objective":"完成用户的最终`,
	}
	plain := terminaltext.StripANSI(strings.Join(streaming.renderCompactContextCall(140, ""), "\n"))
	if strings.Contains(plain, "↳") {
		t.Fatalf("expected a bare header while arguments stream, got:\n%s", plain)
	}

	running := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameCompactContext,
		RawArgs:  `{"active_objective":"完成用户的最终汇报","completed":["a","b"],"decisions":["d"],"next_step":"输出最终完成报告"}`,
	}
	plain = terminaltext.StripANSI(strings.Join(running.renderCompactContextCall(140, ""), "\n"))
	for _, sec := range []string{"↳ Objective:", "↳ Completed:", "↳ Decisions:", "↳ Next:"} {
		if !strings.Contains(plain, sec) {
			t.Fatalf("expected a running card with decodable args to show %q:\n%s", sec, plain)
		}
	}
	if strings.Contains(plain, "compact_context 完成用户的最终汇报") {
		t.Fatalf("expected the objective to stay off the header, got:\n%s", plain)
	}

	// A restored call with no result renders the same body.
	restored := &Block{
		ID:        1,
		Type:      BlockToolCall,
		ToolName:  tools.NameCompactContext,
		Content:   running.RawArgs,
		RawArgs:   running.RawArgs,
		Collapsed: true,
	}
	plain = terminaltext.StripANSI(strings.Join(restored.Render(140, ""), "\n"))
	if !strings.Contains(plain, "↳ Completed:") {
		t.Fatalf("expected a restored card to show its sections, got:\n%s", plain)
	}
}

func TestCompactContextCallSuccessExpandedShowsResult(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameCompactContext,
		RawArgs:       `{"active_objective":"完成用户的最终汇报","next_step":"先按输出最终完成报告"}`,
		ResultDone:    true,
		ResultContent: "Context checkpoint request accepted. No reset has occurred yet; only a later model-driven context checkpoint confirms successful application.",
	}
	block.ToolCallDetailExpanded = true

	plain := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))

	// The acknowledgement carries the "no reset has occurred yet" caveat, so
	// an expanded successful card must keep it below the sections.
	if !strings.Contains(plain, "↳ Result:") {
		t.Fatalf("expected ↳ Result: header on an expanded successful card, got:\n%s", plain)
	}
	if !strings.Contains(plain, "No reset has occurred yet") {
		t.Fatalf("expected the success acknowledgement to stay visible, got:\n%s", plain)
	}
	resultIdx := strings.Index(plain, "↳ Result:")
	if objectiveIdx := strings.Index(plain, "↳ Objective:"); objectiveIdx == -1 || objectiveIdx > resultIdx {
		t.Fatalf("expected the sections to render above the result envelope, got:\n%s", plain)
	}
	// The collapsed flag no longer hides the body: the card is always
	// expanded, so the acknowledgement stays visible either way.
	block.Collapsed = true
	block.ToolCallDetailExpanded = false
	block.InvalidateCache()
	collapsed := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))
	if !strings.Contains(collapsed, "↳ Result:") || !strings.Contains(collapsed, "No reset has occurred yet") {
		t.Fatalf("expected the card to keep the acknowledgement regardless of the collapse flags, got:\n%s", collapsed)
	}
}

func TestCompactContextCallUnknownFieldsKeepSubmittedArgs(t *testing.T) {
	// A payload that nests the whole state under an unexpected key decodes
	// strictly (unknown fields are ignored) yet resolves no schema field —
	// exactly what the validator rejects, so the card must still show what
	// the model sent instead of rendering an empty body.
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameCompactContext,
		RawArgs:       `{"state":{"active_objective":"wrapped under a nested key","next_step":"x"}}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "Error: missing required argument: active_objective",
	}
	block.ToolCallDetailExpanded = true

	plain := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))

	if !strings.Contains(plain, "↳ Arguments:") {
		t.Fatalf("expected the raw-arguments fallback header, got:\n%s", plain)
	}
	if !strings.Contains(plain, "wrapped under a nested key") {
		t.Fatalf("expected the submitted payload to stay visible, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Error:") {
		t.Fatalf("expected the error envelope below the arguments, got:\n%s", plain)
	}
	// The card is always expanded, so the failure reads the same regardless
	// of the collapse flags.
	block.Collapsed = true
	block.ToolCallDetailExpanded = false
	block.InvalidateCache()
	collapsed := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))
	if !strings.Contains(collapsed, "↳ Arguments:") {
		t.Fatalf("expected the argument dump to stay visible, got:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "missing required argument") {
		t.Fatalf("expected the error message to stay visible, got:\n%s", collapsed)
	}
}

func TestCompactContextCallRawArgFallbackIsBounded(t *testing.T) {
	// One wrong key can carry the whole continuation state; the fallback
	// must not grow the card to a hundred lines.
	var sb strings.Builder
	sb.WriteString(`{"wrong_key":"`)
	sb.WriteString(strings.Repeat("bounded fallback payload ", 200))
	sb.WriteString(`"}`)
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameCompactContext,
		RawArgs:       sb.String(),
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "Error: missing required argument: active_objective",
	}
	block.ToolCallDetailExpanded = true

	plain := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))

	if !strings.Contains(plain, "more lines hidden.") {
		t.Fatalf("expected the bounded fallback to report hidden lines, got:\n%s", plain)
	}
	payloadLines := 0
	for line := range strings.SplitSeq(plain, "\n") {
		if strings.Contains(line, "bounded fallback payload") {
			payloadLines++
		}
	}
	if payloadLines == 0 || payloadLines > compactContextRawArgMaxLines {
		t.Fatalf("expected 1..%d payload lines, got %d:\n%s", compactContextRawArgMaxLines, payloadLines, plain)
	}
}

func TestCompactContextCallRenderDispatchesStructuredCard(t *testing.T) {
	// renderToolCall must route compact_context to the structured renderer,
	// and the card is always expanded: the space toggle is a no-op.
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameCompactContext,
		RawArgs:       `{"active_objective":"完成用户的最终汇报","completed":["a"],"next_step":"先按输出最终完成报告"}`,
		ResultDone:    true,
		ResultContent: "Context checkpoint request accepted.",
	}

	rendered := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(rendered, "↳ Completed:") {
		t.Fatalf("expected the structured card from Render, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "↳ Next:") || !strings.Contains(rendered, "先按输出最终完成报告") {
		t.Fatalf("expected the next step under its own section, got:\n%s", rendered)
	}
	if block.ToggleAtWidth(140) {
		t.Fatalf("expected the compact_context card to ignore the expand/collapse toggle")
	}
	after := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if after != rendered {
		t.Fatalf("expected the card to render identically after the toggle:\n%s", after)
	}
}

func TestBenchmarkCompactContextFixtureDecodesStrictly(t *testing.T) {
	// The benchmark claims to measure the strict-decode path with all six
	// sections; a typo in the fixture JSON silently moved it onto the
	// tolerant fallback and dropped two fields.
	fixture := benchmarkCompactContextBlock()
	var args tools.CompactContextArgs
	if err := json.Unmarshal([]byte(fixture.Content), &args); err != nil {
		t.Fatalf("benchmark fixture must be schema-valid JSON: %v", err)
	}
	if args.ActiveObjective == "" || args.NextStep == "" || len(args.Completed) == 0 ||
		len(args.Decisions) == 0 || len(args.OpenIssues) == 0 || len(args.StateFiles) == 0 {
		t.Fatalf("benchmark fixture must populate all six fields, got %+v", args)
	}
}

func TestCompactContextCallErrorExpandedShowsArgsAndError(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameCompactContext,
		Content:       `{"active_objective":"完成用户的最终汇报","completed":"not-an-array","next_step":"先按输出最终完成报告"}`,
		RawArgs:       `{"active_objective":"完成用户的最终汇报","completed":"not-an-array","next_step":"先按输出最终完成报告"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "Error: arguments do not match compact_context schema: args.completed must be an array, got string \"not-an-array\"",
	}
	block.ToolCallDetailExpanded = true

	plain := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))

	// The error envelope closes the body, after the submitted args.
	if !strings.Contains(plain, "↳ Error:") {
		t.Fatalf("expected ↳ Error: header in error state, got:\n%s", plain)
	}
	errorIdx := strings.Index(plain, "↳ Error:")
	for _, sec := range []string{"↳ Objective:", "↳ Next:"} {
		secIdx := strings.Index(plain, sec)
		if secIdx == -1 {
			t.Fatalf("expected failed expanded card to still show %q section, got:\n%s", sec, plain)
		}
		if secIdx > errorIdx {
			t.Fatalf("expected %q section to render above the ↳ Error: envelope, got:\n%s", sec, plain)
		}
	}
	if !strings.Contains(plain, "args.completed must be an array") {
		t.Fatalf("expected validator message in error body, got:\n%s", plain)
	}
	// The envelope's "Error: " prefix must NOT be repeated under our own
	// "↳ Error:" header.
	if strings.Contains(plain, "↳ Error:\n    Error:") {
		t.Fatalf("expected the inner Error: prefix to be stripped, got:\n%s", plain)
	}
	// An expanded failed card exposes the submitted args, matching
	// renderQuestionCall's "show args, then error" convention.
	for _, sec := range []string{"↳ Objective:", "↳ Next:"} {
		if !strings.Contains(plain, sec) {
			t.Fatalf("expected failed expanded card to still show %q section, got:\n%s", sec, plain)
		}
	}
	if !strings.Contains(plain, "完成用户的最终汇报") {
		t.Fatalf("expected submitted active_objective to remain visible on error, got:\n%s", plain)
	}
}

func TestCompactContextCallErrorShowsSections(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameCompactContext,
		Content:       `{"active_objective":"完成用户的最终汇报","completed":["a","b","c"],"decisions":["d"],"open_issues":["o"],"next_step":"先按输出最终完成报告","state_files":["n.md"]}`,
		RawArgs:       `{"active_objective":"完成用户的最终汇报","completed":["a","b","c"],"decisions":["d"],"open_issues":["o"],"next_step":"先按输出最终完成报告","state_files":["n.md"]}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "Error: arguments do not match compact_context schema",
	}
	// The card is always expanded (ToolCallDetailExpanded left false).

	plain := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))

	// The error envelope closes the body, after the submitted args: a
	// failure is exactly when the six sections matter most.
	if !strings.Contains(plain, "↳ Error:") {
		t.Fatalf("expected ↳ Error: header in error state, got:\n%s", plain)
	}
	for _, sec := range []string{"↳ Completed:", "↳ Decisions:", "↳ Open issues:", "↳ State files:"} {
		if !strings.Contains(plain, sec) {
			t.Fatalf("expected error state to show %q section, got:\n%s", sec, plain)
		}
	}
}

func TestCompactContextCallErrorDropsDisclosureGlyph(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameCompactContext,
		Content:       `{"active_objective":"完成用户的最终汇报","next_step":"先按输出最终完成报告"}`,
		RawArgs:       `{"active_objective":"完成用户的最终汇报","next_step":"先按输出最终完成报告"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "Error: arguments do not match compact_context schema",
	}
	// The card is always expanded (ToolCallDetailExpanded left false).

	plain := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))

	// A failed card shows its submitted args without being opened, so the
	// header carries no ▸ glyph — only the ✗ status.
	if strings.Contains(plain, "▸") || strings.Contains(plain, "▾") {
		t.Fatalf("expected the error card to drop the disclosure glyph, got:\n%s", plain)
	}
	if !strings.Contains(plain, "✗") {
		t.Fatalf("expected the error card to show the ✗ status glyph, got:\n%s", plain)
	}
}

func TestCompactContextCallCancelledExpandedShowsArgsAndCancelled(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameCompactContext,
		Content:       `{"active_objective":"完成用户的最终汇报","completed":["a"],"next_step":"先按输出最终完成报告"}`,
		RawArgs:       `{"active_objective":"完成用户的最终汇报","completed":["a"],"next_step":"先按输出最终完成报告"}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusCancelled,
		ResultContent: "Cancelled",
	}
	block.ToolCallDetailExpanded = true

	plain := terminaltext.StripANSI(strings.Join(block.renderCompactContextCall(140, ""), "\n"))

	cancelledIdx := strings.Index(plain, "↳ Cancelled")
	if cancelledIdx == -1 {
		t.Fatalf("expected Cancelled label in cancelled state, got:\n%s", plain)
	}
	// Expanded cancelled card still exposes the submitted args, and the
	// cancellation envelope closes the body below them.
	for _, sec := range []string{"↳ Objective:", "↳ Completed:", "↳ Next:"} {
		secIdx := strings.Index(plain, sec)
		if secIdx == -1 {
			t.Fatalf("expected cancelled expanded card to show %q section, got:\n%s", sec, plain)
		}
		if secIdx > cancelledIdx {
			t.Fatalf("expected %q section to render above the ↳ Cancelled envelope, got:\n%s", sec, plain)
		}
	}
}
