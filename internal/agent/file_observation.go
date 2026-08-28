package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// verifiedCurrentFileHash returns the content hash of an existing regular file
// plus its modification time captured at the same moment. modTime is non-zero
// whenever the path exists and is stat-able, even when the contents cannot be
// read (hash is then "" with err set), so callers can still report how recently
// an unreadable file changed.
func verifiedCurrentFileHash(path string) (hash string, exists bool, modTime time.Time, err error) {
	info, lerr := os.Lstat(path)
	if lerr != nil {
		if os.IsNotExist(lerr) {
			return "", false, time.Time{}, nil
		}
		return "", false, time.Time{}, lerr
	}
	hash = computeFileHash(path)
	if hash == "" {
		return "", true, info.ModTime(), fmt.Errorf("current content hash cannot be verified")
	}
	return hash, true, info.ModTime(), nil
}

// requireCurrentFileObservation reports whether a destructive tool may proceed
// against path. A file that was observed but has changed since returns stale=true
// so the caller can back up the current content and continue instead of forcing a
// redundant re-read round trip. A whole-file write treats an existing file it has
// never observed the same way (NameWrite): write replaces everything anyway and
// must never refuse, so the caller backs up the current contents and continues
// instead of demanding a now-redundant full re-read. Any other tool acting on a
// file it has never seen stays a hard refusal: the caller has no baseline to judge
// a replacement against. unobserved is true only when the existing file was never
// seen by this agent, so the caller can word the reminder accurately.
func requireCurrentFileObservation(track *filelock.FileTracker, agentID, path, currentHash, action string, externalChanged bool) (stale, unobserved bool, err error) {
	if track == nil {
		return false, false, nil
	}
	observation := track.Observation(path, agentID, currentHash)
	if observation.Current && !externalChanged {
		return false, false, nil
	}
	if !observation.Observed {
		if action == tools.NameWrite {
			// Back up the unobserved current contents and continue instead of
			// refusing: the write itself is the only baseline the model has, and
			// replace-everything semantics make a pre-read redundant.
			return true, true, nil
		}
		return false, false, fmt.Errorf("refusing to %s existing file %s without a current read; read the complete file first, then retry", action, path)
	}
	// When nothing changed externally, a follow-up whole-file write is safe
	// against the agent's own committed state (its previous write, or the state
	// it produced with a localized edit/apply_patch). The model may not have
	// seen the exact resulting bytes (edits stay committed-only), but there is
	// nothing external to lose, so do not warn or back up. externalChanged is
	// itself defined as the tracked snapshot hash disagreeing with the current
	// one, so reaching here without it already means the on-disk content is
	// what this agent committed.
	if !externalChanged {
		return false, false, nil
	}
	return true, false, nil
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
