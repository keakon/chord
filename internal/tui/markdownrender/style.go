package markdownrender

import (
	"github.com/keakon/glamour/v2/ansi"

	uitheme "github.com/keakon/chord/internal/tui/theme"
)

// StyleConfig returns a glamour StyleConfig that keeps normal
// markdown text transparent so the surrounding card background remains the only
// block-level surface.
func StyleConfig(palette uitheme.Palette) ansi.StyleConfig {
	// Keep markdown text transparent so the outer card owns the background.
	fg := new(palette.ToolResultExpandedFg)
	fgCode := new(palette.ParamValFg)
	bgCode := new(palette.InlineCodeBg)
	fgH1 := new(palette.HeaderFg)
	fgH2 := new(palette.ConfirmToolFg)
	fgH3 := new(palette.ModeSearchFg)
	fgH4 := new(palette.ToolCallFg)
	fgH5 := new(palette.DimFg)
	fgH6 := new(palette.DimFg)
	fgStrong := new(palette.HeaderFg)
	block := func() ansi.StyleBlock {
		return ansi.StyleBlock{BackgroundColor: nil, Color: fg}
	}
	prim := func() ansi.StylePrimitive {
		return ansi.StylePrimitive{BackgroundColor: nil, Color: fg}
	}
	headingBlock := func(color *string, underline bool) ansi.StyleBlock {
		return ansi.StyleBlock{
			BackgroundColor: nil,
			Color:           color,
			Bold:            new(true),
			Underline:       new(underline),
			BlockSuffix:     "\n",
		}
	}
	strongPrim := ansi.StylePrimitive{BackgroundColor: nil, Color: fgStrong, Bold: new(true)}
	codePrim := ansi.StylePrimitive{BackgroundColor: bgCode, Color: fgCode}
	return ansi.StyleConfig{
		Document:              block(),
		BlockQuote:            ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{BackgroundColor: nil, Color: fg}, Indent: new(uint(1)), IndentToken: new("│ ")},
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
		CodeBlock:             ansi.StyleCodeBlock{StyleBlock: ansi.StyleBlock{StylePrimitive: codePrim, Margin: new(uint(1))}},
		Table:                 ansi.StyleTable{StyleBlock: block()},
		DefinitionTerm:        prim(),
		DefinitionDescription: prim(),
	}
}
