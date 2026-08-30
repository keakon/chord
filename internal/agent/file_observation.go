package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

// externalReadLazyMemo caches the disk-verification verdict for historical
// current reads. Keyed by normalized absolute path + expected hash, it
// stores the stat (mtime/size) at verification time so a later pass can reuse
// the verdict without re-reading the file; the content hash is recomputed only
// when the stat changes.
//
// The memo is bounded: overflowing resets it wholesale, because it is only a
// stat-comparison shortcut and the next pass re-verifies stat-first (cheap).
type externalReadLazyMemo struct {
	mu       sync.Mutex
	verdicts map[string]externalReadLazyVerdict
}

// lazyReadMemoMaxEntries bounds the lazy read-verification memo. Each entry
// is a (path, expected-hash) verdict; a long session reading many distinct
// files would otherwise grow the map without limit.
const lazyReadMemoMaxEntries = 2048

type externalReadLazyVerdict struct {
	mtimeNano int64
	size      int64
	changed   bool
}

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

// externalReadsInvalidatedLazy extends the disk-verification of historical
// reads beyond the mutating-shell trigger: every successful read result
// still trusted by in-conversation evidence is lazily checked against the
// disk. stat-first — the content hash is recomputed only when the file's
// mtime/size changed since the last verification, so an external editor (or
// any non-tool process) editing a file the model still trusts marks the read
// stale and triggers a re-read hint, without a filesystem watcher.
//
// The memo is keyed by (normalized absolute path, expected hash) and stores
// the stat at verification time: a later pass with an unchanged stat reuses
// the cached verdict without re-reading the file. A transient stat/hash error
// leaves the verdict unknown — the read is neither asserted stale nor asserted
// valid, and the pair is not cached so the next pass retries. A file that no
// longer exists is stale by definition: the read recorded content that cannot
// be current.
func (a *MainAgent) externalReadsInvalidatedLazy(messages []message.Message, scan *reductionHistoryScan) map[int]bool {
	if a == nil || a.tools == nil || len(messages) == 0 {
		return nil
	}
	callMeta := scan.callMeta()
	a.lazyReadMemo.mu.Lock()
	defer a.lazyReadMemo.mu.Unlock()
	if a.lazyReadMemo.verdicts == nil {
		a.lazyReadMemo.verdicts = make(map[string]externalReadLazyVerdict)
	}
	verdicts := a.lazyReadMemo.verdicts
	var invalidated map[int]bool
	for i := range messages {
		msg := &messages[i]
		if msg.Role != message.RoleTool || msg.FileState == nil || isToolResultUnsuccessfulStatus(msg.ToolStatus) {
			continue
		}
		if toolname.Normalize(callMeta[msg.ToolCallID].Name) != tools.NameRead {
			continue
		}
		for _, read := range msg.FileState.Reads {
			path := strings.TrimSpace(read.Path)
			expected := strings.TrimSpace(read.SHA256)
			if path == "" || expected == "" || !read.Exists {
				continue
			}
			if !filepath.IsAbs(path) && a.projectRoot != "" {
				path = filepath.Join(a.projectRoot, path)
			}
			stale := a.lazyReadStatCheck(path, expected, verdicts)
			if stale {
				if invalidated == nil {
					invalidated = make(map[int]bool)
				}
				invalidated[i] = true
				break
			}
		}
	}
	return invalidated
}

// lazyReadStatCheck returns true when the file at path no longer matches the
// hash captured at read time. The memo fast path compares the current stat
// with the cached one and reuses the verdict; otherwise the content hash is
// recomputed and the cache updated.
//
// The stat follows symlinks, matching the hash it guards: read records the
// symlink path it was given, so keying the memo on the link's own mtime/size
// would pin the verdict to metadata that does not change when the target is
// edited, and the stale read would stay marked current forever.
func (a *MainAgent) lazyReadStatCheck(path, expected string, verdicts map[string]externalReadLazyVerdict) bool {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Recorded content cannot be current: the read is stale. The
			// non-existence is not memoized, so a later pass re-stats (and
			// recovers if the file reappears).
			return true
		}
		// Transient stat error: unknown, do not assert stale or valid.
		return false
	}
	key := path + "\x00" + expected
	mtimeNano := info.ModTime().UnixNano()
	size := info.Size()
	if verdict, ok := verdicts[key]; ok && verdict.mtimeNano == mtimeNano && verdict.size == size {
		return verdict.changed
	}
	hash, exists, _, err := verifiedCurrentFileHash(path)
	if err != nil {
		// Content cannot be verified: keep the verdict unknown and let the
		// next pass retry.
		return false
	}
	changed := !exists || hash != expected
	if len(verdicts) >= lazyReadMemoMaxEntries {
		// The memo is a stat shortcut, not a source of truth: resetting it
		// costs at most one stat-only re-verification per entry on the next
		// pass, so a wholesale reset is cheaper than eviction bookkeeping and
		// keeps the map bounded in long sessions.
		clear(verdicts)
	}
	verdicts[key] = externalReadLazyVerdict{
		mtimeNano: mtimeNano,
		size:      size,
		changed:   changed,
	}
	return changed
}
