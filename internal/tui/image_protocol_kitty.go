package tui

import (
	"fmt"

	xansi "github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tui/imageprotocol"
)

func kittyStyledPlaceholderLines(imageID, cols, rows int, cardBG string) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	lines := make([]string, 0, rows)
	fg := imageprotocol.KittyImageIDColor(imageID)
	bg := indexedColor(cardBG)
	for row := range rows {
		placeholder := imageprotocol.KittyPlaceholderRow(row, cols)
		style := xansi.Style{}.
			ForegroundColor(fg).
			BackgroundColor(bg)
		lines = append(lines, style.Styled("  "+placeholder))
	}
	return lines
}

func kittyRenderImageID(part BlockImagePart, cols, rows int) (int, error) {
	key, err := imageInlineCacheKey(part)
	if err != nil {
		return 0, err
	}
	return imageprotocol.KittyImageID(key, fmt.Sprintf("inline:%d:%d", cols, rows)), nil
}
