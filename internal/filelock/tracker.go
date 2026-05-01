package filelock

import (
	"crypto/sha256"
	"fmt"
	"github.com/keakon/golog/log"
	"io"
	"os"
	"sync"
)

// ConflictError is returned when a file write conflicts with another agent's
// concurrent write, or when another agent invalidated this agent's read
// sentinel (empty read hash) before a write.
type ConflictError struct {
	Path       string // file path that caused the conflict
	ModifiedBy string // agent ID that holds the conflicting lock (write-write only)
	Message    string // human-readable conflict description
}

func (e *ConflictError) Error() string { return e.Message }

// ExternalModificationError is returned when on-disk content no longer matches
// what the agent last recorded via TrackRead: another in-process writer, an
// external editor, or any other process may have changed the file.
type ExternalModificationError struct {
	Path    string
	Message string
}

func (e *ExternalModificationError) Error() string { return e.Message }

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

// FileTracker provides in-process optimistic concurrency control for file
// access. It prevents write-write conflicts (two agents writing the same file
// simultaneously), detects when another agent invalidated a read sentinel, and
// returns [ExternalModificationError] when on-disk content no longer matches
// the hash last recorded for this agent (another writer, external editor, etc.).
//
// All methods are goroutine-safe.
type FileTracker struct {
	mu sync.Mutex
	// file path → agent ID currently holding write permission
	writers map[string]string
	// file path → agent ID → content hash recorded by in-process agent at read time
	readHashes map[string]map[string]string
	// file path → agent ID → actual disk hash recorded when agent last read the file
	diskHashes map[string]map[string]string
}

// NewFileTracker creates a new FileTracker with empty state.
func NewFileTracker() *FileTracker {
	return &FileTracker{
		writers:    make(map[string]string),
		readHashes: make(map[string]map[string]string),
		diskHashes: make(map[string]map[string]string),
	}
}

// TrackRead records the content hash that an agent observed when reading a
// file. This forms the basis for optimistic lock detection and external
// modification detection.
func (t *FileTracker) TrackRead(path, agentID, contentHash string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.readHashes[path] == nil {
		t.readHashes[path] = make(map[string]string)
	}
	t.readHashes[path][agentID] = contentHash

	if t.diskHashes[path] == nil {
		t.diskHashes[path] = make(map[string]string)
	}
	t.diskHashes[path][agentID] = contentHash
}

// AcquireWrite attempts to acquire write permission for a file.
//
// It checks conditions in order:
//  1. Write-write conflict: another goroutine (even from the same agent)
//     currently holds write permission.
//  2. Stale read: this agent's tracked read hash is empty (another agent
//     wrote) or does not match currentHash (typically the current disk hash).
//  3. Disk drift: if the caller's currentHash matched the tracked read hash but
//     the file on disk changed before this method could verify (rare race).
//
// currentHash is the hash of the file as computed immediately before calling
// AcquireWrite (i.e. the hash the caller already has).
func (t *FileTracker) AcquireWrite(path, agentID, currentHash string) error {
	// Read disk hash outside the lock to avoid holding the mutex during I/O.
	// We capture the diskReadHash under the lock first, then do the I/O.
	t.mu.Lock()
	var diskReadHash string
	var checkExternal bool
	if dh, ok := t.diskHashes[path]; ok {
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
		return &ConflictError{
			Path:       path,
			ModifiedBy: owner,
			Message:    fmt.Sprintf("file %s is being written by %s", path, owner),
		}
	}

	// Stale read: tracked content hash for this agent does not match the hash
	// the caller sees now (typically the current on-disk file).
	if hashes, ok := t.readHashes[path]; ok {
		if readHash, tracked := hashes[agentID]; tracked && currentHash != readHash {
			if readHash == "" {
				// Sentinel from ReleaseWrite: another agent wrote this path.
				return &ConflictError{
					Path:    path,
					Message: fmt.Sprintf("file %s was modified by another agent; re-read to get latest content", path),
				}
			}
			return &ExternalModificationError{
				Path: path,
				Message: fmt.Sprintf(
					"file %s changed on disk since the last read; re-read to get the latest content",
					path,
				),
			}
		}
	}

	// External modification detection.
	// Re-check diskReadHash under the new lock acquisition (state may have changed).
	if checkExternal {
		if dh, ok := t.diskHashes[path]; ok {
			if currentStoredHash, tracked := dh[agentID]; tracked {
				diskReadHash = currentStoredHash
			}
		}
		if diskReadHash != "" && actualDiskHash != "" && actualDiskHash != diskReadHash {
			return &ExternalModificationError{
				Path: path,
				Message: fmt.Sprintf(
					"file %s changed on disk since the last read; re-read to get the latest content",
					path,
				),
			}
		}
	}

	t.writers[path] = agentID
	return nil
}

// AbortWrite releases write permission for a single file without updating read
// hashes or invalidating other agents. Use this when a writer acquired the
// lock but did not commit any on-disk change.
func (t *FileTracker) AbortWrite(path, agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.writers[path] == agentID {
		delete(t.writers, path)
	}
}

// ReleaseWrite releases write permission for a single file and invalidates
// other agents' read hashes for that file (so they will detect stale reads
// if they attempt to write).
func (t *FileTracker) ReleaseWrite(path, agentID, newHash string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.writers[path] == agentID {
		delete(t.writers, path)
	}

	// Update the writer's own read hash to reflect the new content.
	if hashes, ok := t.readHashes[path]; ok {
		hashes[agentID] = newHash
		// Invalidate other agents' read hashes by setting them to an empty
		// sentinel value. This ensures AcquireWrite detects stale reads
		// (empty sentinel never matches any real content hash) while
		// preserving the evidence that the other agent had read the file.
		for otherAgent := range hashes {
			if otherAgent != agentID {
				hashes[otherAgent] = ""
			}
		}
	}

	// Update disk hashes similarly.
	if dh, ok := t.diskHashes[path]; ok {
		dh[agentID] = newHash
		for otherAgent := range dh {
			if otherAgent != agentID {
				dh[otherAgent] = ""
			}
		}
	}
}

// ReleaseAll releases all write permissions and read tracking for the given
// agent. This should be called when an agent completes or errors out.
func (t *FileTracker) ReleaseAll(agentID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for path, owner := range t.writers {
		if owner == agentID {
			delete(t.writers, path)
		}
	}

	for _, hashes := range t.readHashes {
		delete(hashes, agentID)
	}

	for _, dh := range t.diskHashes {
		delete(dh, agentID)
	}
}
