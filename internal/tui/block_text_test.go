package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// sgrActiveOnTrailingSpaces walks an ANSI-encoded line, tracks SGR state and
// reports whether any space column after the last visible (non-space)
// character is still emitted while the given SGR "on" code is active. This
// isolates padding leaks (pad spaces should always be in the default state
// for that attribute) from legitimate intra-text whitespace (e.g. the space
// between "Add" and "edge" in strike-through body text).
//
// onCode is the "on" SGR parameter (e.g. "9" for strike, "4" for underline).
func sgrActiveOnTrailingSpaces(line, onCode string) bool {
	plain := stripANSI(line)
	contentEnd := len(strings.TrimRight(plain, " \t"))
	if contentEnd == 0 {
		return false
	}
	active := false
	plainCol := 0
	i := 0
	for i < len(line) {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j < len(line) && line[j] == 'm' {
				active = applySGRCodesForAttr(line[i+2:j], onCode, active)
				i = j + 1
				continue
			}
			i = j + 1
			continue
		}
		if line[i] == ' ' && active && plainCol >= contentEnd {
			return true
		}
		plainCol++
		i++
	}
	return false
}

// applySGRCodesForAttr returns the updated attribute state after applying the
// semicolon-separated SGR parameter list. It tracks only the single attribute
// identified by onCode; indexed/RGB colour sub-parameters are consumed so
// strike/underline codes aren't misread from them.
func applySGRCodesForAttr(codes, onCode string, active bool) bool {
	if codes == "" {
		return false
	}
	parts := strings.Split(codes, ";")
	for idx := 0; idx < len(parts); idx++ {
		c := parts[idx]
		switch c {
		case "", "0":
			active = false
			continue
		case "38", "48":
			if idx+1 < len(parts) {
				mode := parts[idx+1]
				if mode == "5" && idx+2 < len(parts) {
					idx += 2
					continue
				}
				if mode == "2" && idx+4 < len(parts) {
					idx += 4
					continue
				}
			}
			continue
		}
		if c == onCode {
			active = true
			continue
		}
		switch c {
		case "22":
			if onCode == "1" {
				active = false
			}
		case "24":
			if onCode == "4" {
				active = false
			}
		case "29":
			if onCode == "9" {
				active = false
			}
		}
	}
	return active
}

func TestExpandTabsForDisplayUsesFixedTabWidth(t *testing.T) {
	got := expandTabsForDisplay(" \tswitch", preformattedTabWidth)
	want := "    switch"
	if got != want {
		t.Fatalf("expandTabsForDisplay() = %q, want %q", got, want)
	}
}

func TestExpandTabsForDisplayANSIIgnoresEscapeSequenceWidth(t *testing.T) {
	input := "\x1b[48;5;235mA\tB\x1b[m"
	got := expandTabsForDisplayANSI(input, 4)
	// Visible text should align as if the escape sequence has zero width.
	if stripANSI(got) != "A   B" {
		t.Fatalf("stripANSI(expandTabsForDisplayANSI())=%q, want %q", stripANSI(got), "A   B")
	}
	if strings.ContainsRune(got, '\t') {
		t.Fatalf("expandTabsForDisplayANSI should remove tabs, got %q", got)
	}
	// Preserve the SGR sequences.
	if !strings.Contains(got, "\x1b[48;5;235m") || !strings.Contains(got, "\x1b[m") {
		t.Fatalf("expected output to preserve ANSI sequences, got %q", got)
	}
}

func TestWrapPreformattedTextPreservesIndentationAndTabs(t *testing.T) {
	lines := wrapPreformattedText("-\t\tif llm.IsResponsesModel(modelID) || normalizedCfg.Preset == \"codex\" {", 30)
	if len(lines) < 2 {
		t.Fatalf("wrapPreformattedText() should soft-wrap long preformatted line, got %#v", lines)
	}
	if !strings.HasPrefix(lines[0], "-       if ") {
		t.Fatalf("first wrapped line should preserve expanded tab indentation, got %q", lines[0])
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "IsResponsesModel(modelID)") {
		t.Fatalf("wrapped text lost code content: %#v", lines)
	}
}

func TestRenderUserPlainDoesNotInsertSyntheticBlankLineInIndentedYAML(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:   1,
		Type: BlockUser,
		Content: "我是这样配置的：\n" +
			"  codex:\n" +
			"    preset: \"codex\"\n" +
			"\tproxy: \"socks5://127.0.0.1:1080\"",
	}
	lines := stripANSILines(block.renderUserPlain(120))
	trimRight := func(s string) string { return strings.TrimRight(s, " ") }

	idx := -1
	for i, l := range lines {
		if strings.Contains(l, "codex:") {
			idx = i
			break
		}
	}
	if idx == -1 {
		t.Fatalf("expected rendered card to contain codex:, got %q", strings.Join(lines, "\n"))
	}
	if idx+1 >= len(lines) {
		t.Fatalf("unexpected end of rendered lines after codex:, got %q", strings.Join(lines, "\n"))
	}
	if trimRight(lines[idx+1]) == "" {
		t.Fatalf("should not insert a blank line after codex:, got %q", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[idx+1], "preset: \"codex\"") {
		t.Fatalf("expected preset line to immediately follow codex:, got %q", strings.Join(lines[idx:idx+3], "\n"))
	}
	for _, l := range lines {
		if strings.Contains(l, "proxy:") {
			if strings.Contains(l, "\t") {
				t.Fatalf("should expand tabs; found tab in %q", l)
			}
			return
		}
	}

	t.Fatalf("expected rendered card to contain proxy:, got %q", strings.Join(lines, "\n"))
}

func TestRenderUserPlainPreservesCodeIndentationForPreformattedContent(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:      1,
		Type:    BlockUser,
		Content: "这是卡片内容：\n--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,3 @@\n \tswitch providerType {\n-\t\tif llm.IsResponsesModel(modelID) || normalizedCfg.Preset == \"codex\" {\n+\t\tif llm.IsResponsesAPIURL(apiURL) || normalizedCfg.Preset == \"codex\" {",
	}
	lines := block.renderUserPlain(120)
	plain := strings.Join(stripANSILines(lines), "\n")
	if !strings.Contains(plain, "  --- a/file.go") {
		t.Fatalf("rendered user card missing diff header: %q", plain)
	}
	if !strings.Contains(plain, "  -       if llm.IsResponsesModel(modelID) || normalizedCfg.Preset == \"codex\" {") {
		t.Fatalf("rendered user card did not preserve expanded code indentation: %q", plain)
	}
	if !strings.Contains(plain, "  +       if llm.IsResponsesAPIURL(apiURL) || normalizedCfg.Preset == \"codex\" {") {
		t.Fatalf("rendered user card did not preserve added-line indentation: %q", plain)
	}
}

func TestRenderUserPlainSessionLikePastedEditDiffUsesPreformattedPath(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:   1,
		Type: BlockUser,
		Content: "这是卡片内容：\n" +
			`{"path": "cmd/chord/common_provider_cache.go", "new_string": "\tcase \"chat-completions\":\n\t\t// Auto-switch to responses provider if needed"}` +
			"\n\nDiff:\n" +
			"--- cmd/chord/common_provider_cache.go\n" +
			"+++ cmd/chord/common_provider_cache.go\n" +
			"@@ -143,7 +143,7 @@\n" +
			" \tswitch providerType {\n" +
			" \tcase \"chat-completions\":\n" +
			"-\t\tif llm.IsResponsesAPIURL(apiURL) || llm.IsResponsesModel(modelID) || normalizedCfg.Preset == \"codex\" {\n" +
			"+\t\tif llm.IsResponsesAPIURL(apiURL) || normalizedCfg.Preset == \"codex\" {",
	}
	lines := block.renderUserPlain(110)
	plain := strings.Join(stripANSILines(lines), "\n")
	if !strings.Contains(plain, "  这是卡片内容：") {
		t.Fatalf("rendered user card missing intro text: %q", plain)
	}
	if !strings.Contains(plain, `  {"path": "cmd/chord/common_provider_cache.go"`) {
		t.Fatalf("rendered user card missing pasted JSON payload: %q", plain)
	}
	if !strings.Contains(plain, "  --- cmd/chord/common_provider_cache.go") {
		t.Fatalf("rendered user card missing diff header: %q", plain)
	}
	if !strings.Contains(plain, "      switch providerType {") {
		t.Fatalf("rendered user card did not expand tab-indented context line: %q", plain)
	}
	if !strings.Contains(plain, "  -       if llm.IsResponsesAPIURL(apiURL) || llm.IsResponsesModel(modelID) ||") {
		t.Fatalf("rendered user card did not preserve deleted diff indentation: %q", plain)
	}
	if !strings.Contains(plain, "  +       if llm.IsResponsesAPIURL(apiURL) || normalizedCfg.Preset == \"codex\" {") {
		t.Fatalf("rendered user card did not preserve added diff indentation: %q", plain)
	}
}

func TestPadLineToDisplayWidthWithStyleDoesNotLeakStrikethrough(t *testing.T) {
	body := lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Strikethrough(true)
	cardBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	line := body.Render("abc")
	padded := padLineToDisplayWidthWithStyle(cardBg, line, 20)
	if sgrActiveOnTrailingSpaces(padded, "9") {
		t.Fatalf("padding inherited strikethrough: %q", padded)
	}
}

func TestPadLineToDisplayWidthWithStyleDoesNotLeakUnderline(t *testing.T) {
	body := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Underline(true)
	cardBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	line := body.Render("abc")
	padded := padLineToDisplayWidthWithStyle(cardBg, line, 20)
	if sgrActiveOnTrailingSpaces(padded, "4") {
		t.Fatalf("padding inherited underline: %q", padded)
	}
}

func TestPadLineToDisplayWidthWithStyleKeepsCardBackgroundOnPadding(t *testing.T) {
	cardBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	padded := padLineToDisplayWidthWithStyle(cardBg, "abc", 10)
	if !strings.Contains(padded, "\x1b[48;5;236m") {
		t.Fatalf("padding missing card background sequence: %q", padded)
	}
}

func TestPadLineToDisplayWidthWithStylePreservesExactWidthLineVerbatim(t *testing.T) {
	cardBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	const line = "abcdefghij" // exactly width=10
	if got := padLineToDisplayWidthWithStyle(cardBg, line, 10); got != line {
		t.Fatalf("line at exact width should pass through unchanged: %q -> %q", line, got)
	}
}

func TestRenderTodoCallCancelledItemDoesNotStrikeThroughPadding(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: "TodoWrite",
		Content: `{"todos":[` +
			`{"id":"1","content":"first item","status":"completed"},` +
			`{"id":"2","content":"second item","status":"cancelled"}` +
			`]}`,
		ResultDone: true,
	}
	lines := block.Render(80, "")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(stripANSI(joined), "second item") {
		t.Fatalf("cancelled item text missing: %q", stripANSI(joined))
	}
	for i, line := range lines {
		if sgrActiveOnTrailingSpaces(line, "9") {
			t.Fatalf("line %d has strikethrough active over trailing pad spaces: %q", i, line)
		}
	}
}

func TestRailANSISeqUsesFocusedColor(t *testing.T) {
	ApplyTheme(DefaultTheme())
	if got, want := railANSISeq("assistant", false), "\x1b[38;5;"+currentTheme.RailAssistantFg+"m"; got != want {
		t.Fatalf("assistant base rail seq=%q want %q", got, want)
	}
	if got, want := railANSISeq("assistant", true), "\x1b[38;5;"+currentTheme.RailAssistantFocusedFg+"m"; got != want {
		t.Fatalf("assistant focused rail seq=%q want %q", got, want)
	}
}

func TestWrapPreformattedTextUsesGraphemeClustersForEmoji(t *testing.T) {
	line := "👩‍⚕️x"
	if got, want := tuiStringWidth(line), 3; got != want {
		t.Fatalf("test fixture width=%d want %d", got, want)
	}
	lines := wrapPreformattedText(line, 3)
	if len(lines) != 1 || lines[0] != line {
		t.Fatalf("wrapPreformattedText split grapheme cluster line: %#v", lines)
	}
}

func TestPadLineToDisplayWidthUsesViewportGraphemeWidth(t *testing.T) {
	line := "👩‍⚕️x"
	padded := padLineToDisplayWidth(line, 3)
	if got := stripANSI(padded); got != line {
		t.Fatalf("padLineToDisplayWidth added padding with viewport-full grapheme line: %q", got)
	}
}

func TestTruncateLineToDisplayWidthKeepsEmojiGraphemeIntact(t *testing.T) {
	line := "⚠️ ok"
	got := truncateLineToDisplayWidth(line, 2)
	if got != "⚠️" {
		t.Fatalf("truncateLineToDisplayWidth split or overran emoji grapheme: %q", got)
	}
}

func TestWrapPreformattedTextKeepsOversizeGraphemeWithoutLeadingBlank(t *testing.T) {
	lines := wrapPreformattedText("⚠️x", 1)
	want := []string{"⚠️", "x"}
	if len(lines) != len(want) {
		t.Fatalf("wrapPreformattedText lines=%#v want %#v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("wrapPreformattedText lines=%#v want %#v", lines, want)
		}
	}
}

func TestWrapTextKeepsOversizeGraphemeWithoutLeadingBlank(t *testing.T) {
	lines := wrapText("⚠️x", 1)
	want := []string{"⚠️", "x"}
	if len(lines) != len(want) {
		t.Fatalf("wrapText lines=%#v want %#v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("wrapText lines=%#v want %#v", lines, want)
		}
	}
}

func TestTruncateLineToDisplayWidthDropsOversizeFirstGraphemeStrictly(t *testing.T) {
	got := truncateLineToDisplayWidth("⚠️x", 1)
	if got != "" {
		t.Fatalf("truncateLineToDisplayWidth should not exceed requested width: %q", got)
	}
}

func TestTuiWrapHeadTailKeepsStyledOversizeFirstGrapheme(t *testing.T) {
	line := "\x1b[38;5;196m⚠️x\x1b[0m"
	head, tail := tuiWrapHeadTail(line, 1)
	if got := stripANSI(head); got != "⚠️" {
		t.Fatalf("head plain=%q want ⚠️; ansi=%q", got, head)
	}
	if got := stripANSI(tail); got != "x" {
		t.Fatalf("tail plain=%q want x; ansi=%q", got, tail)
	}
}

func TestRenderPrewrappedCardDoesNotOverpadEmojiVariationSelector(t *testing.T) {
	ApplyTheme(DefaultTheme())
	style := lipgloss.NewStyle().Padding(0, 0).Background(lipgloss.Color(currentTheme.AssistantCardBg))
	out := renderPrewrappedCard(style, 2, []string{"▶️"}, currentTheme.AssistantCardBg, "")
	if len(out) != 1 {
		t.Fatalf("renderPrewrappedCard lines=%d want 1", len(out))
	}
	plain := stripANSI(out[0])
	if plain != "▶️" {
		t.Fatalf("renderPrewrappedCard overpadded viewport-full emoji line: plain=%q ansi=%q", plain, out[0])
	}
}

func TestRenderPrewrappedCardAppliesRailToPaddingRows(t *testing.T) {
	ApplyTheme(DefaultTheme())
	style := lipgloss.NewStyle().Padding(1, 1).MarginLeft(1).Background(lipgloss.Color(currentTheme.AssistantCardBg))
	out := renderPrewrappedCard(style, 8, []string{"  hello"}, currentTheme.AssistantCardBg, railANSISeq("assistant", false))
	if len(out) < 3 {
		t.Fatalf("renderPrewrappedCard lines=%d want >=3", len(out))
	}
	contentLines := 0
	for i, line := range out {
		plain := stripANSI(line)
		trimmed := strings.TrimSpace(plain)
		if trimmed == "" {
			continue
		}
		contentLines++
		if !strings.HasPrefix(strings.TrimLeft(plain, " "), "│") {
			t.Fatalf("line %d missing rail prefix: plain=%q ansi=%q", i, plain, line)
		}
	}
	if contentLines < 3 {
		t.Fatalf("expected rail on top padding/body/bottom padding rows, got contentLines=%d output=%q", contentLines, strings.Join(out, "\n"))
	}
}

func TestFocusedAssistantCardKeepsBaseBackground(t *testing.T) {
	ApplyTheme(DefaultTheme())
	msg := "focused rail only should keep card background unchanged"
	block := &Block{Type: BlockAssistant, Content: msg, Focused: true}
	lines := block.Render(96, "")
	var target string
	for _, line := range lines {
		if strings.Contains(stripANSI(line), msg) {
			target = line
			break
		}
	}
	if target == "" {
		t.Fatalf("failed to find target line in render output: %q", strings.Join(lines, "\n"))
	}
	buf := newScreenBuffer(ansi.StringWidth(target), 1)
	uv.NewStyledString(target).Draw(buf, buf.Bounds())
	cells := buf.Line(0)
	if len(cells) == 0 {
		t.Fatal("expected rendered cells")
	}
	baseBg := lipgloss.Color(currentTheme.AssistantCardBg)
	var checked int
	for _, cell := range cells {
		if cell.IsZero() || cell.Content != " " {
			continue
		}
		checked++
		if !testColorsEqual(cell.Style.Bg, baseBg) {
			t.Fatalf("focused assistant pad background=%v want base=%v", cell.Style.Bg, baseBg)
		}
	}
	if checked == 0 {
		t.Fatal("expected to inspect at least one padding cell")
	}
}

func TestFocusedStatusCardUsesFocusedRailColor(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{Type: BlockStatus, StatusTitle: "LOOP", Content: "body", Focused: true}
	lines := block.Render(96, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered lines")
	}
	focusSeq := railANSISeq("thinking", true)
	if focusSeq == "" {
		t.Fatal("expected focused thinking rail ANSI sequence")
	}
	found := false
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.TrimSpace(plain) == "" {
			continue
		}
		if strings.Contains(line, focusSeq) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("focused BlockStatus should include focused rail sequence %q", focusSeq)
	}
}

func TestFocusedCompactionSummaryCardUsesFocusedRailColor(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{Type: BlockCompactionSummary, Content: "summary line", Focused: true}
	lines := block.Render(96, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered lines")
	}
	focusSeq := railANSISeq("assistant", true)
	if focusSeq == "" {
		t.Fatal("expected focused assistant rail ANSI sequence")
	}
	found := false
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.TrimSpace(plain) == "" {
			continue
		}
		if strings.Contains(line, focusSeq) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("focused BlockCompactionSummary should include focused rail sequence %q", focusSeq)
	}
}

func testColorsEqual(a, b interface{ RGBA() (r, g, b, a uint32) }) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
