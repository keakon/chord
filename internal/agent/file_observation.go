package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/message"
)

func verifiedCurrentFileHash(path string) (hash string, exists bool, err error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	hash = computeFileHash(path)
	if hash == "" {
		return "", true, fmt.Errorf("current content hash cannot be verified")
	}
	return hash, true, nil
}

func requireCurrentFileObservation(track *filelock.FileTracker, agentID, path, currentHash, action string, externalChanged bool) error {
	if track == nil {
		return nil
	}
	observation := track.Observation(path, agentID, currentHash)
	if observation.Current && !externalChanged {
		return nil
	}
	if !observation.Observed {
		return fmt.Errorf("refusing to %s existing file %s without a current read; read the complete file first, then retry", action, path)
	}
	return fmt.Errorf("refusing to %s file %s because it changed after the last read; re-read the complete file before retrying", action, path)
}

func (a *MainAgent) trackObservedFileParts(parts []message.ContentPart) {
	if a == nil || a.fileTrack == nil || len(parts) == 0 {
		return
	}
	for _, part := range parts {
		if part.Type != message.ContentPartText {
			continue
		}
		ref, body, ok := message.ParseSingleFileRefContent(part.Text)
		if !ok || ref.Path == "" || ref.Lines != "" {
			continue
		}
		path := ref.Path
		if !filepath.IsAbs(path) && a.projectRoot != "" {
			path = filepath.Join(a.projectRoot, path)
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, []byte(body)) {
			continue
		}
		sum := sha256.Sum256(data)
		a.fileTrack.TrackObservedSnapshot(path, a.instanceID, hex.EncodeToString(sum[:]))
	}
}
