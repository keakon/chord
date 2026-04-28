package tui

import (
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"

	"github.com/keakon/chord/internal/tui/markdownutil"
)

// Markdown body uses transparent backgrounds so assistant/thinking/tool cards
// provide the block-level surface. Only inline/code-block specific styles opt
// into their own background when needed.
var (
	glamourContentRenderer      *glamour.TermRenderer
	glamourContentRendererWidth int
)

func resetMarkdownRenderer() {
	glamourContentRenderer = nil
	glamourContentRendererWidth = 0
}

// contentMarkdownStyleConfig returns a glamour StyleConfig that keeps normal
// markdown text transparent so the surrounding card background remains the only
// block-level surface.
func contentMarkdownStyleConfig() ansi.StyleConfig {
	// Keep markdown text transparent so the outer card owns the background.
	fg := stringPtr(currentTheme.ToolResultExpandedFg)
	fgCode := stringPtr(currentTheme.ParamValFg)
	bgCode := stringPtr(currentTheme.InlineCodeBg)
	fgH1 := stringPtr(currentTheme.HeaderFg)
	fgH2 := stringPtr(currentTheme.ConfirmToolFg)
	fgH3 := stringPtr(currentTheme.ModeSearchFg)
	fgH4 := stringPtr(currentTheme.ToolCallFg)
	fgH5 := stringPtr(currentTheme.DimFg)
	fgH6 := stringPtr(currentTheme.DimFg)
	fgStrong := stringPtr(currentTheme.HeaderFg)
	block := func() ansi.StyleBlock {
		return ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{BackgroundColor: nil, Color: fg}}
	}
	prim := func() ansi.StylePrimitive {
		return ansi.StylePrimitive{BackgroundColor: nil, Color: fg}
	}
	headingBlock := func(color *string, underline bool) ansi.StyleBlock {
		return ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			BackgroundColor: nil,
			Color:           color,
			Bold:            boolPtr(true),
			Underline:       boolPtr(underline),
			BlockSuffix:     "\n",
		}}
	}
	strongPrim := ansi.StylePrimitive{BackgroundColor: nil, Color: fgStrong, Bold: boolPtr(true)}
	codePrim := ansi.StylePrimitive{BackgroundColor: bgCode, Color: fgCode}
	return ansi.StyleConfig{
		Document:              block(),
		BlockQuote:            ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{BackgroundColor: nil, Color: fg}, Indent: uintPtr(1), IndentToken: stringPtr("│ ")},
		Paragraph:             block(),
		Heading:               headingBlock(fgH2, false),
		H1:                    headingBlock(fgH1, true),
		H2:                    headingBlock(fgH2, false),
		H3:                    headingBlock(fgH3, false),
		H4:                    headingBlock(fgH4, false),
		H5:                    headingBlock(fgH5, false),
		H6:                    headingBlock(fgH6, false),
		Text:                  prim(),
		Strikethrough:         prim(),
		Emph:                  prim(),
		Strong:                strongPrim,
		HorizontalRule:        prim(),
		Item:                  ansi.StylePrimitive{BackgroundColor: nil, Color: fg, BlockPrefix: "• "},
		Enumeration:           ansi.StylePrimitive{BackgroundColor: nil, Color: fg, BlockPrefix: ". "},
		List:                  ansi.StyleList{StyleBlock: block(), LevelIndent: 2},
		Task:                  ansi.StyleTask{StylePrimitive: prim(), Ticked: "[x] ", Unticked: "[ ] "},
		Link:                  prim(),
		LinkText:              prim(),
		Image:                 prim(),
		ImageText:             prim(),
		Code:                  ansi.StyleBlock{StylePrimitive: codePrim},
		CodeBlock:             ansi.StyleCodeBlock{StyleBlock: ansi.StyleBlock{StylePrimitive: codePrim, Margin: uintPtr(1)}},
		Table:                 ansi.StyleTable{StyleBlock: block()},
		DefinitionTerm:        prim(),
		DefinitionDescription: prim(),
	}
}

func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }

// renderMarkdownContent renders settled markdown with transparent block-level
// backgrounds; fenced code is handled separately in assistant code paths.
func renderMarkdownContent(content string, width int) []string {
	if width <= 0 {
		width = 80
	}
	content = markdownutil.RepairForDisplay(content)
	if glamourContentRenderer == nil || glamourContentRendererWidth != width {
		r, err := glamour.NewTermRenderer(
			glamour.WithStyles(contentMarkdownStyleConfig()),
			glamour.WithWordWrap(width),
			glamour.WithInlineTableLinks(true),
		)
		if err != nil {
			return wrapText(content, width)
		}
		glamourContentRenderer = r
		glamourContentRendererWidth = width
	}
	out, err := glamourContentRenderer.Render(content)
	if err != nil {
		return wrapText(content, width)
	}
	out = strings.TrimLeft(out, "\n\r")
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return []string{""}
	}
	return normalizeRenderedMarkdownIndent(strings.Split(out, "\n"))
}
