package markdownrender

import (
	"regexp"
	"strings"
	"testing"

	"github.com/keakon/glamour/v2"

	"github.com/keakon/chord/internal/tui/markdownutil"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

var cjkSGR = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// cjkBoldText returns the visible characters that were inside a bold SGR span.
// It tracks the bold attribute across the whole rendered string so a test can
// assert both that the target text is bold and that bold was switched off
// afterwards, instead of merely counting escape sequences.
func cjkBoldText(s string) string {
	var out strings.Builder
	bold := false
	last := 0
	for _, loc := range cjkSGR.FindAllStringSubmatchIndex(s, -1) {
		if bold {
			out.WriteString(terminaltext.StripANSI(s[last:loc[0]]))
		}
		for param := range strings.SplitSeq(s[loc[2]:loc[3]], ";") {
			switch param {
			case "1", "01":
				bold = true
			case "", "0", "22":
				bold = false
			}
		}
		last = loc[1]
	}
	if bold {
		out.WriteString(terminaltext.StripANSI(s[last:]))
	}
	return out.String()
}

func renderCJK(t *testing.T, content string) (joined, plain, bold string) {
	t.Helper()
	r := New(uitheme.Default())
	joined = strings.Join(r.Render(content, 120), "\n")
	return joined, terminaltext.StripANSI(joined), cjkBoldText(joined)
}

// renderWithoutCJKExtension mirrors Renderer.Render's pipeline but omits only
// the CJK-friendly extender. Comparing full rendered strings (not just the
// presence of a strong node) is what pins the pure-ASCII boundary.
func renderWithoutCJKExtension(t *testing.T, content string, width int) string {
	t.Helper()
	r := New(uitheme.Default())
	md, err := glamour.NewTermRenderer(
		glamour.WithStyles(r.style),
		glamour.WithWordWrap(width),
		glamour.WithInlineTableLinks(true),
	)
	if err != nil {
		t.Fatalf("NewTermRenderer: %v", err)
	}
	normalized := markdownutil.NormalizeAdjacentStrong(
		markdownutil.RepairForDisplay(terminaltext.Sanitize(content)),
	)
	out, err := md.Render(normalized)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	out = strings.TrimLeft(out, "\n\r")
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return ""
	}
	return strings.Join(normalizeRenderedMarkdownIndent(strings.Split(out, "\n")), "\n")
}

func TestRendererCJKFriendlyStrongEmphasis(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantBold    string
		wantNotBold string
	}{
		{
			name:        "label with fullwidth colon",
			in:          "**说明：**这里是正文。",
			wantBold:    "说明：",
			wantNotBold: "这里是正文",
		},
		{
			name:        "CJK corner brackets",
			in:          "这是**「重要」**的内容。",
			wantBold:    "「重要」",
			wantNotBold: "的内容",
		},
		{
			name:        "CJK book title marks",
			in:          "请参考**《设计原则》**一书",
			wantBold:    "《设计原则》",
			wantNotBold: "一书",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, plain, bold := renderCJK(t, tc.in)
			if !strings.Contains(bold, tc.wantBold) {
				t.Fatalf("target text not inside a bold SGR span: bold=%q plain=%q", bold, plain)
			}
			if strings.Contains(bold, tc.wantNotBold) {
				t.Fatalf("bold was not restored after the strong span: bold=%q", bold)
			}
			if strings.Contains(plain, "**") {
				t.Fatalf("literal asterisks remain: %q", plain)
			}
			if !strings.Contains(plain, tc.wantNotBold) {
				t.Fatalf("trailing text missing: %q", plain)
			}
		})
	}
}

func TestRendererCJKFriendlyCardKeepsOtherStrongSpans(t *testing.T) {
	content := "示例编号：**`abc123`**，供参考。\n\n**说明：**这里是正文；完成比例 **81.8%**。"
	_, plain, bold := renderCJK(t, content)
	if strings.Contains(plain, "**") {
		t.Fatalf("literal asterisks remain in the card: %q", plain)
	}
	for _, want := range []string{"说明：", "81.8%"} {
		if !strings.Contains(bold, want) {
			t.Fatalf("%q missing from bold spans: bold=%q", want, bold)
		}
	}
	// A code span carries its own container style rather than inheriting the
	// surrounding strong span, so it is checked on the visible-text surface.
	if !strings.Contains(plain, "abc123") {
		t.Fatalf("code span text missing: %q", plain)
	}
	if strings.Contains(bold, "完成比例") {
		t.Fatalf("bold leaked into surrounding prose: bold=%q", bold)
	}
}

// The extension takes over * and _ delimiter parsing and relaxes CJK-adjacent
// ASCII punctuation too. These cases pin the accepted wider scope so a future
// change is not mistaken for a regression.
func TestRendererCJKFriendlyExtraScopeIsPinned(t *testing.T) {
	cases := []struct {
		in       string
		wantBold string
		wantGone string
	}{
		{in: `中**"foo"**文`, wantBold: `"foo"`, wantGone: "**"},
		{in: `**Note:**中文`, wantBold: "Note:", wantGone: "**"},
		{in: `a**「foo」**b`, wantBold: "「foo」", wantGone: "**"},
		{in: `中***「foo」***文`, wantBold: "「foo」", wantGone: "***"},
		{in: `这是*「重要」*的内容`, wantGone: "*"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, plain, bold := renderCJK(t, tc.in)
			if strings.Contains(plain, tc.wantGone) {
				t.Fatalf("delimiter still literal: %q", plain)
			}
			if tc.wantBold != "" && !strings.Contains(bold, tc.wantBold) {
				t.Fatalf("%q missing from bold spans: bold=%q plain=%q", tc.wantBold, bold, plain)
			}
		})
	}
}

func TestRendererCJKFriendlyUnderscoreEmphasis(t *testing.T) {
	// U+FE01 makes the preceding quotation mark CJK punctuation. Ordinary
	// intraword underscores retain CommonMark's stricter opening rules.
	for _, delimiter := range []string{"_", "__"} {
		t.Run(delimiter, func(t *testing.T) {
			const prefix = "“\ufe01Example”\ufe01"
			input := prefix + delimiter + "Hub" + delimiter + " tail"
			_, plain, bold := renderCJK(t, input)
			if got := strings.TrimSpace(plain); got != prefix+"Hub tail" {
				t.Fatalf("rendered text = %q", got)
			}
			wantBold := ""
			if delimiter == "__" {
				wantBold = "Hub"
			}
			if bold != wantBold {
				t.Fatalf("bold text = %q, want %q", bold, wantBold)
			}
			without := terminaltext.StripANSI(renderWithoutCJKExtension(t, input, 120))
			if !strings.Contains(without, delimiter+"Hub"+delimiter) {
				t.Fatalf("sample must exercise the extension: %q", without)
			}
		})
	}
}

// These unaffected inputs must produce byte-identical output with and without
// the extender, including literal delimiters and existing Markdown constructs.
func TestRendererCJKFriendlyZeroRegression(t *testing.T) {
	cases := []string{
		"a**\"foo\"**b",
		"**Note:**text",
		"**(**foo)",
		"**b.**a",
		`a*"foo"*b`,
		`a***"foo"***b`,
		`a_"foo"_b`,
		`a__"foo"__b`,
		"完成比例 **81.8%**。",
		"**`abc123`**，供参考。",
		"~~删除线~~中文",
		"这是 **正常** 的加粗。",
		"这是_「重要」_的内容",
		"中__「foo」__文",
		"`**not bold**`",
		`\**not bold**`,
		"****",
		"**a****b**",
		"| Account ID | Email |\n|------------|-------|\n| sample-1 | reader@example.invalid |",
		"```text\n**a****b**\n```",
	}
	const width = 120
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got := strings.Join(New(uitheme.Default()).Render(in, width), "\n")
			want := renderWithoutCJKExtension(t, in, width)
			if got != want {
				t.Fatalf("output changed with CJK extender enabled:\n got=%q\nwant=%q", got, want)
			}
		})
	}
}

// The CJK-friendly parser must compose with the existing adjacent-strong
// normalization, including CJK payloads.
func TestRendererCJKFriendlyAdjacentStrongWithCJK(t *testing.T) {
	_, plain, bold := renderCJK(t, "**中文甲****中文乙**")
	if strings.Contains(plain, "****") {
		t.Fatalf("adjacent strong delimiter survived: %q", plain)
	}
	if !strings.Contains(plain, "中文甲中文乙") {
		t.Fatalf("adjacent strong text missing: %q", plain)
	}
	for _, want := range []string{"中文甲", "中文乙"} {
		if !strings.Contains(bold, want) {
			t.Fatalf("%q missing from bold spans: %q", want, bold)
		}
	}
}

// Source data and the fallback surface are unchanged by a parser-level fix.
func TestRendererCJKFriendlyFallbackShowsOriginalDelimiters(t *testing.T) {
	r := New(uitheme.Default())
	original := r.render
	r.render = func(_ *glamour.TermRenderer, _ string) (string, error) {
		panic("render failed")
	}
	t.Cleanup(func() {
		r.render = original
		r.reset()
	})
	const content = "**说明：**这里是正文"
	joined := terminaltext.StripANSI(strings.Join(r.Render(content, 80), "\n"))
	if !strings.Contains(joined, content) {
		t.Fatalf("fallback must show the original text verbatim: %q", joined)
	}
	if strings.Contains(joined, "chord-adjacent-strong") {
		t.Fatalf("fallback leaked an internal marker: %q", joined)
	}
}
