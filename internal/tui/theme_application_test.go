package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tui/markdownrender"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestApplyThemePublishesStylesAndInvalidatesMarkdown(t *testing.T) {
	previous := currentStyles.Palette
	t.Cleanup(func() { ApplyTheme(previous) })
	ApplyTheme(uitheme.Default())
	beforeRenderer := contentMarkdown
	beforeOutput := strings.Join(contentMarkdown.Render("sample", 40), "\n")
	captured := markdownrender.StyleConfig(currentStyles.Palette)
	oldColor := *captured.Text.Color
	before := currentStyles
	version := appliedThemeVersion
	next := before.Palette
	next.ToolResultExpandedFg = "196"
	next.ParamValFg = "197"
	ApplyTheme(next)
	if currentStyles == before || currentStyles.Palette != next || appliedThemeVersion != version+1 {
		t.Fatal("theme change did not publish a new versioned style snapshot")
	}
	if contentMarkdown == nil || contentMarkdown == beforeRenderer {
		t.Fatal("theme change left a stale Markdown renderer")
	}
	if *captured.Text.Color != oldColor {
		t.Fatal("theme change mutated a captured file-viewer style config")
	}
	style := markdownrender.StyleConfig(currentStyles.Palette)
	if *style.Text.Color != next.ToolResultExpandedFg || style.Text.BackgroundColor != nil {
		t.Fatal("new Markdown style does not use the new transparent body palette")
	}
	if *style.Code.Color != next.ParamValFg {
		t.Fatal("new inline-code style did not use the new palette")
	}
	afterOutput := strings.Join(contentMarkdown.Render("sample", 40), "\n")
	if afterOutput == beforeOutput {
		t.Fatal("theme change did not update rendered Markdown styling")
	}
	if terminaltext.StripANSI(afterOutput) != terminaltext.StripANSI(beforeOutput) {
		t.Fatal("theme change altered Markdown text")
	}
}
