package filelock

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/keakon/golog/log"
)

// ConflictError is returned when a file write conflicts with another agent's
// concurrent write.
type ConflictError struct {
	Path       string // file path that caused the conflict
	ModifiedBy string // agent ID that holds the conflicting lock (write-write only)
	Message    string // human-readable conflict description
}

func (e *ConflictError) Error() string { return e.Message }

// WriteStatus reports non-blocking risk detected while acquiring a write lock.
type WriteStatus struct {
	ExternalChanged bool
}

// ObservationStatus describes whether an agent has actually observed the
// current content of a file. Localized mutations (edit/patch) update optimistic
// concurrency state but deliberately do not count as observations: the model
// has not seen the resulting full file. Whole-file writes do count — the
// committed content is exactly the bytes the model supplied.
type ObservationStatus struct {
	Observed bool
	Current  bool
}

// WriteLease captures the tracker key used for one acquired write. It lets a
// caller release a path after deleting it without re-resolving missing symlinks.
type WriteLease struct {
	tracker *FileTracker
	path    string
	agentID string
}

// readDiskHash computes the SHA-256 hash of the file at path.
// Returns "" if the file does not exist or cannot be read.
func readDiskHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warnf("filelock: failed to open file for hashing path=%v err=%v", path, err)
		}
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		log.Warnf("filelock: failed to hash file path=%v err=%v", path, err)
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func normalizeTrackedPath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if eval, err := filepath.EvalSymlinks(path); err == nil {
		path = eval
	}
	path = filepath.Clean(path)
	if path == "." {
		return ""
	}
	return path
}

// FileTracker provides in-process optimistic concurrency control for file
// access. It prevents write-write conflicts (two agents writing the same file
// simultaneously) and reports when on-disk content no longer matches the hash
// last recorded for this agent (another writer, external editor, etc.).
//
// All methods are goroutine-safe.
type FileTracker struct {
	mu sync.Mutex
	// file path → agent ID currently holding write permission
	writers map[string]string
	// file path → agent ID → content hash recorded by in-process agent at snapshot time
	snapshotHashes map[string]map[string]string
	// file path → agent ID → actual disk hash recorded when the snapshot was taken
	diskSnapshotHashes map[string]map[string]string
	// file path → agent ID → content hash actually shown to the agent through a
	// successful read, durable restored read, or complete <file> injection.
	observedHashes map[string]map[string]string
}

// NewFileTracker creates a new FileTracker with empty state.
func NewFileTracker() *FileTracker {
	return &FileTracker{
		writers:            make(map[string]string),
		snapshotHashes:     make(map[string]map[string]string),
		diskSnapshotHashes: make(map[string]map[string]string),
		observedHashes:     make(map[string]map[string]string),
	}
}

// TrackSnapshot records the content hash that an agent observed for a file. This
// forms the basis for optimistic stale/external modification detection.
func (t *FileTracker) TrackSnapshot(path, agentID, contentHash string) {
	t.trackSnapshot(path, agentID, contentHash, true)
}

// TrackCommittedSnapshot advances optimistic concurrency state after a
// successful mutation without claiming the resulting whole-file content was
// shown to the model.
func (t *FileTracker) TrackCommittedSnapshot(path, agentID, contentHash string) {
	t.trackSnapshot(path, agentID, contentHash, false)
}

// TrackObservedSnapshot records content that was actually shown in full to an
// agent. It is named separately at call sites so partial reads can advance only
// committed concurrency state without authorizing destructive whole-file work.
func (t *FileTracker) TrackObservedSnapshot(path, agentID, contentHash string) {
	t.trackSnapshot(path, agentID, contentHash, true)
}

func (t *FileTracker) trackSnapshot(path, agentID, contentHash string, observed bool) {
	path = normalizeTrackedPath(path)
	if path == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.snapshotHashes[path] == nil {
		t.snapshotHashes[path] = make(map[string]string)
	}
	t.snapshotHashes[path][agentID] = contentHash

	if t.diskSnapshotHashes[path] == nil {
		t.diskSnapshotHashes[path] = make(map[string]string)
	}
	t.diskSnapshotHashes[path][agentID] = contentHash

	if observed {
		if t.observedHashes[path] == nil {
			t.observedHashes[path] = make(map[string]string)
		}
		t.observedHashes[path][agentID] = contentHash
	}
}

// Observation reports whether agentID has observed path and whether that
// observation still matches currentHash. It performs no disk I/O; callers
// should pass the hash captured immediately before their mutation.
func (t *FileTracker) Observation(path, agentID, currentHash string) ObservationStatus {
	if t == nil {
		return ObservationStatus{}
	}
	path = normalizeTrackedPath(path)
	if path == "" {
		return ObservationStatus{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	hashes, ok := t.observedHashes[path]
	if !ok {
		return ObservationStatus{}
	}
	hash, observed := hashes[agentID]
	return ObservationStatus{Observed: observed, Current: observed && hash != "" && hash == currentHash}
}

// HasSnapshot reports whether the given agent has a recorded snapshot for path in
// the current tracker state. It returns true even if the snapshot hash was later
// invalidated to a stale sentinel ("").
func (t *FileTracker) HasSnapshot(path, agentID string) bool {
	if t == nil {
		return false
	}
	path = normalizeTrackedPath(path)
	if path == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if hashes, ok := t.snapshotHashes[path]; ok {
		_, tracked := hashes[agentID]
		return tracked
	}
	return false
}

// AcquireWrite attempts to acquire write permission for a file.
//
// It checks conditions in order:
//  1. Write-write conflict: another goroutine (even from the same agent)
//     currently holds write permission.
//  2. Stale/external changes are returned as [WriteStatus] by
//     [AcquireWriteStatus], not rejected.
//
// currentHash is the hash of the file as computed immediately before calling
// AcquireWrite (i.e. the hash the caller already has).
func (t *FileTracker) AcquireWrite(path, agentID, currentHash string) error {
	_, err := t.AcquireWriteStatus(path, agentID, currentHash)
	return err
}

// AcquireWriteLease acquires a write and returns a lease carrying the exact
// canonical key used by the tracker.
func (t *FileTracker) AcquireWriteLease(path, agentID, currentHash string) (WriteStatus, *WriteLease, error) {
	canonical := normalizeTrackedPath(path)
	status, err := t.acquireWriteStatusCanonical(canonical, agentID, currentHash)
	if err != nil {
		return status, nil, err
	}
	if canonical == "" {
		return status, nil, nil
	}
	return status, &WriteLease{tracker: t, path: canonical, agentID: agentID}, nil
}

// AcquireWriteStatus acquires write permission and reports stale/external-change
// risk without rejecting it. Concurrent write-write conflicts are still rejected.
func (t *FileTracker) AcquireWriteStatus(path, agentID, currentHash string) (WriteStatus, error) {
	path = normalizeTrackedPath(path)
	return t.acquireWriteStatusCanonical(path, agentID, currentHash)
}

func (t *FileTracker) acquireWriteStatusCanonical(path, agentID, currentHash string) (WriteStatus, error) {
	var status WriteStatus
	if path == "" {
		return status, nil
	}
	// Read disk hash outside the lock to avoid holding the mutex during I/O.
	// We capture the diskReadHash under the lock first, then do the I/O.
	t.mu.Lock()
	var diskReadHash string
	var checkExternal bool
	if dh, ok := t.diskSnapshotHashes[path]; ok {
		if h, tracked := dh[agentID]; tracked && h != "" {
			diskReadHash = h
			checkExternal = true
		}
	}
	t.mu.Unlock()

	// Perform disk I/O outside the lock.
	var actualDiskHash string
	if checkExternal {
		actualDiskHash = readDiskHash(path)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Write-write conflict.
	if owner, ok := t.writers[path]; ok {
		return status, &ConflictError{
			Path:       path,
			ModifiedBy: owner,
			Message:    fmt.Sprintf("file %s is being written by %s", path, owner),
		}
	}

	// Stale snapshot: tracked content hash for this agent does not match the hash
	// the caller sees now (typically the current on-disk file).
	if hashes, ok := t.snapshotHashes[path]; ok {
		if snapshotHash, tracked := hashes[agentID]; tracked && currentHash != snapshotHash {
			status.ExternalChanged = true
		}
	}

	// External modification detection.
	// Re-check diskReadHash under the new lock acquisition (state may have changed).
	if checkExternal {
		if dh, ok := t.diskSnapshotHashes[path]; ok {
			if currentStoredHash, tracked := dh[agentID]; tracked {
				diskReadHash = currentStoredHash
			}
		}
		if diskReadHash != "" && actualDiskHash != "" && actualDiskHash != diskReadHash {
			status.ExternalChanged = true
		}
	}

	t.writers[path] = agentID
	return status, nil
}

// Abort releases the write without committing an on-disk change.
func (l *WriteLease) Abort() {
	if l == nil || l.tracker == nil || l.path == "" {
		return
	}
	l.tracker.abortWriteCanonical(l.path, l.agentID)
}

// CommitDelete releases the write after the leased path was deleted.
func (l *WriteLease) CommitDelete() {
	if l == nil || l.tracker == nil || l.path == "" {
		return
	}
	l.tracker.releaseDeleteCanonical(l.path, l.agentID)
}

// AbortWrite releases write permission for a single file without updating read
// hashes or invalidating other agents. Use this when a writer acquired the
// lock but did not commit any on-disk change.
func (t *FileTracker) AbortWrite(path, agentID string) {
	path = normalizeTrackedPath(path)
	t.abortWriteCanonical(path, agentID)
}

func (t *FileTracker) abortWriteCanonical(path, agentID string) {
	if path == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.writers[path] == agentID {
		delete(t.writers, path)
	}
}

// ReleaseWrite releases write permission for a single file and invalidates
// other agents' snapshots for that file so later writes report stale risk.
func (t *FileTracker) ReleaseWrite(path, agentID, newHash string) {
	path = normalizeTrackedPath(path)
	if path == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.writers[path] == agentID {
		delete(t.writers, path)
	}

	// Update the writer's own snapshot to reflect the new content.
	if hashes, ok := t.snapshotHashes[path]; ok {
		hashes[agentID] = newHash
		// Invalidate other agents' snapshots by setting them to an empty
		// sentinel value. This ensures AcquireWrite detects stale snapshots
		// (empty sentinel never matches any real content hash) while
		// preserving evidence that the other agent had a snapshot.
		for otherAgent := range hashes {
			if otherAgent != agentID {
				hashes[otherAgent] = ""
			}
		}
	}

	// Update disk hashes similarly.
	if dh, ok := t.diskSnapshotHashes[path]; ok {
		dh[agentID] = newHash
		for otherAgent := range dh {
			if otherAgent != agentID {
				dh[otherAgent] = ""
			}
		}
	}
}

func (t *FileTracker) releaseDeleteCanonical(path, agentID string) {
	if path == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.writers[path] != agentID {
		return
	}
	delete(t.writers, path)
	if hashes, ok := t.snapshotHashes[path]; ok {
		delete(hashes, agentID)
		for otherAgent := range hashes {
			hashes[otherAgent] = ""
		}
		if len(hashes) == 0 {
			delete(t.snapshotHashes, path)
		}
	}
	if hashes, ok := t.diskSnapshotHashes[path]; ok {
		delete(hashes, agentID)
		for otherAgent := range hashes {
			hashes[otherAgent] = ""
		}
		if len(hashes) == 0 {
			delete(t.diskSnapshotHashes, path)
		}
	}
	if hashes, ok := t.observedHashes[path]; ok {
		delete(hashes, agentID)
		if len(hashes) == 0 {
			delete(t.observedHashes, path)
		}
	}
}

// ReleaseAll releases all write permissions and snapshot tracking for the given
// agent. This should be called when an agent completes or errors out.
func (t *FileTracker) ReleaseAll(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for path, owner := range t.writers {
		if owner == agentID {
			delete(t.writers, path)
		}
	}

	for _, hashes := range t.snapshotHashes {
		delete(hashes, agentID)
	}

	for _, dh := range t.diskSnapshotHashes {
		delete(dh, agentID)
	}

	for _, hashes := range t.observedHashes {
		delete(hashes, agentID)
	}
}
