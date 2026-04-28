package tui

import (
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
	xiterm2 "github.com/charmbracelet/x/ansi/iterm2"
)

func iterm2InlineSequence(part BlockImagePart, cols, rows int) (string, error) {
	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		return "", err
	}
	content, size, err := entry.base64TransportPNG(part)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(part.FileName)
	if name == "" {
		name = "image.png"
	}
	return xansi.ITerm2(xiterm2.File{
		Name:            name,
		Size:            int64(size),
		Width:           xiterm2.Cells(cols),
		Height:          xiterm2.Cells(rows),
		Inline:          true,
		DoNotMoveCursor: false,
		Content:         []byte(content),
	}), nil
}

func iterm2ViewerSequence(part BlockImagePart, cols, rows int) (string, error) {
	return iterm2InlineSequence(part, cols, rows)
}

func imageViewerHintText(caps TerminalImageCapabilities, total int) string {
	switch caps.Backend {
	case ImageBackendKitty, ImageBackendITerm2:
		if total > 1 {
			return imageViewerHint
		}
		return imageViewerSingleHint
	default:
		return imageViewerFallbackHint
	}
}
