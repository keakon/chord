package agent

import (
	"github.com/keakon/chord/internal/message"
)

func (a *MainAgent) trackObservedFileParts(parts []message.ContentPart) {
	if a == nil || a.fileTrack == nil || len(parts) == 0 {
		return
	}
	for _, part := range parts {
		if part.Type != "text" || !message.IsFileRefContent(part.Text) {
			continue
		}
		path, _ := message.FirstFileRefPath(part.Text)
		if path == "" {
			continue
		}
		if hash := computeFileHash(path); hash != "" {
			a.fileTrack.TrackSnapshot(path, a.instanceID, hash)
		}
	}
}
