package tui

import (
	"github.com/keakon/chord/internal/tui/markdownrender"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

var appliedThemeVersion uint64

var contentMarkdown *markdownrender.Renderer

// The zero snapshot preserves pre-init reads by package variable initializers.
// Only ApplyTheme publishes a replacement, on the TUI owner goroutine.
var currentStyles = new(uitheme.Styles)

func init() {
	ApplyTheme(uitheme.Default())
}

// ApplyTheme publishes a complete style snapshot and invalidates the Markdown
// renderer. Versioned block caches observe the increment on their next render.
func ApplyTheme(t uitheme.Palette) {
	appliedThemeVersion++
	currentStyles = uitheme.Build(t)
	contentMarkdown = markdownrender.New(currentStyles.Palette)
}
