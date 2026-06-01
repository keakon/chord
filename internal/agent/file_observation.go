package agent

import (
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/message"
)

var fileRefPathPattern = regexp.MustCompile(`^<file path=("([^"\\]|\\.)*"|'([^'\\]|\\.)*')>`)

func (a *MainAgent) trackObservedFileParts(parts []message.ContentPart) {
	if a == nil || a.fileTrack == nil || len(parts) == 0 {
		return
	}
	for _, part := range parts {
		if part.Type != "text" || !message.IsFileRefContent(part.Text) {
			continue
		}
		path := fileRefPathFromContent(part.Text)
		if path == "" {
			continue
		}
		if hash := computeFileHash(path); hash != "" {
			a.fileTrack.TrackRead(path, a.instanceID, hash)
		}
	}
}

func fileRefPathFromContent(text string) string {
	text = strings.TrimSpace(text)
	match := fileRefPathPattern.FindString(text)
	if match == "" {
		return ""
	}
	value := strings.TrimPrefix(match, "<file path=")
	value = strings.TrimSuffix(value, ">")
	value = html.UnescapeString(value)
	if unquoted, err := strconv.Unquote(value); err == nil {
		return strings.TrimSpace(unquoted)
	}
	return strings.TrimSpace(strings.Trim(value, "\"'"))
}
