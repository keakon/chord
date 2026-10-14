package imageprotocol

import (
	"strings"

	xiterm2 "github.com/keakon/x/ansi/iterm2"

	"github.com/keakon/chord/internal/tui/imagecache"
)

// ITerm2 prepares a complete inline file sequence using cached PNG bytes.
// Preview requests use the bounded thumbnail; other requests retain original size.
func ITerm2(cache *imagecache.Store, part imagecache.Source, name string, cols, rows int, preview bool) (string, error) {
	entry, err := cache.Lookup(part, preview)
	if err != nil {
		return "", err
	}
	content, size, err := entry.Base64PNG(part)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
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
