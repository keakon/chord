package tui

import (
	"strings"

	xiterm2 "github.com/keakon/x/ansi/iterm2"
)

func iterm2InlineSequence(part BlockImagePart, cols, rows int) (string, error) {
	return iterm2Sequence(part, cols, rows, true)
}

func iterm2Sequence(part BlockImagePart, cols, rows int, preview bool) (string, error) {
	entry, err := imageRuntimeEntryForVariant(part, preview)
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
	header := (xiterm2.File{
		Name:            name,
		Size:            int64(size),
		Width:           xiterm2.Cells(cols),
		Height:          xiterm2.Cells(rows),
		Inline:          true,
		DoNotMoveCursor: false,
	}).String()
	var seq strings.Builder
	seq.Grow(len(header) + len(content) + 9)
	seq.WriteString("\x1b]1337;")
	seq.WriteString(header)
	if content != "" {
		seq.WriteByte(':')
		seq.WriteString(content)
	}
	seq.WriteByte('\x07')
	return seq.String(), nil
}

func iterm2ViewerSequence(part BlockImagePart, cols, rows int) (string, error) {
	return iterm2Sequence(part, cols, rows, false)
}
