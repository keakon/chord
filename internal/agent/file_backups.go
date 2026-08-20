package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/tools"
)

const (
	maxToolBackupsPerPath        = 10
	maxToolBackupsPerSession     = 200
	maxToolBackupBytesPerSession = 50 << 20
	maxSingleToolBackupBytes     = 10 << 20
)

type fileBackupManager struct {
	mu         sync.Mutex
	sessionDir string
	seq        int
	byPath     map[string][]string
}

type fileBackupRecord struct {
	Path string
	Size int64
}

// fileBackupSource is one pre-execution file snapshot eligible for backup.
// Multi-file mutations carry one entry per touched path so every execution
// path backs up the same set.
type fileBackupSource struct {
	Path string
	Data []byte
}

type fileBackupOutcome struct {
	Records []fileBackupRecord
}

func newFileBackupManager(sessionDir string) *fileBackupManager {
	m := &fileBackupManager{sessionDir: strings.TrimSpace(sessionDir), byPath: make(map[string][]string)}
	m.scanExistingBackupsLocked()
	return m
}

func (m *fileBackupManager) SetSessionDir(sessionDir string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionDir == sessionDir {
		return
	}
	m.sessionDir = strings.TrimSpace(sessionDir)
	m.seq = 0
	m.byPath = make(map[string][]string)
	m.scanExistingBackupsLocked()
}

// scanExistingBackupsLocked rebuilds byPath, the per-path pruning order, and
// the sequence counter from the backup files already on disk. Each hash
// directory holds the backups of one source path and each file name carries an
// incrementing sequence prefix, so a fresh manager (new process, resumed
// session, or session switch) observes the same per-session quotas and pruning
// order the previous owner enforced instead of silently starting from zero and
// letting the backup directory grow without bound.
func (m *fileBackupManager) scanExistingBackupsLocked() {
	if m == nil || m.sessionDir == "" {
		return
	}
	backupsRoot := filepath.Join(m.sessionDir, "backups")
	hashDirs, err := os.ReadDir(backupsRoot)
	if err != nil {
		return
	}
	for _, dirEntry := range hashDirs {
		if !dirEntry.IsDir() {
			continue
		}
		hashDir := filepath.Join(backupsRoot, dirEntry.Name())
		fileEntries, err := os.ReadDir(hashDir)
		if err != nil {
			continue
		}
		paths := make([]string, 0, len(fileEntries))
		for _, fileEntry := range fileEntries {
			if fileEntry.IsDir() {
				continue
			}
			backupPath := filepath.Join(hashDir, fileEntry.Name())
			if seq, ok := backupSequenceFromName(fileEntry.Name()); ok && seq > m.seq {
				m.seq = seq
			}
			paths = append(paths, backupPath)
		}
		// ReadDir order is not guaranteed; sort by the sequence prefix so
		// byPath stays in creation order and pruning removes the oldest first.
		sort.Slice(paths, func(i, j int) bool {
			si, _ := backupSequenceFromName(filepath.Base(paths[i]))
			sj, _ := backupSequenceFromName(filepath.Base(paths[j]))
			return si < sj
		})
		m.byPath[dirEntry.Name()] = paths
		m.pruneLocked(dirEntry.Name())
	}
}

// backupSequenceFromName extracts the leading sequence digits of a backup file
// name (e.g. "000000000003-before-Edit-target.txt" -> 3).
func backupSequenceFromName(name string) (int, bool) {
	digits := 0
	for digits < len(name) && name[digits] >= '0' && name[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0, false
	}
	seq, err := strconv.Atoi(name[:digits])
	if err != nil {
		return 0, false
	}
	return seq, true
}

func (m *fileBackupManager) Backup(path, toolName string, data []byte) (fileBackupRecord, error) {
	if m == nil || strings.TrimSpace(path) == "" {
		return fileBackupRecord{}, nil
	}
	if len(data) > maxSingleToolBackupBytes {
		return fileBackupRecord{}, fmt.Errorf("the file exceeds the backup size limit (%d bytes > %d bytes)", len(data), maxSingleToolBackupBytes)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionDir == "" {
		return fileBackupRecord{}, nil
	}
	rawKey := normalizeAgentFilePath(path)
	if rawKey == "" {
		return fileBackupRecord{}, nil
	}
	// byPath is keyed by the same short hash used for the on-disk directory
	// name, so backups reindexed from disk by scanExistingBackupsLocked and
	// new backups land in the same per-path group and prune together.
	key := shortPathHash(rawKey)
	if err := m.ensureSessionLimitsLocked(key, int64(len(data))); err != nil {
		return fileBackupRecord{}, err
	}
	m.seq++
	name := backupFileName(m.seq, rawKey, toolName)
	dir := filepath.Join(m.sessionDir, "backups", key)
	backupPath := filepath.Join(dir, name)
	if err := privatefs.WriteFile(m.sessionDir, backupPath, data); err != nil {
		return fileBackupRecord{}, fmt.Errorf("write backup: %w", err)
	}
	m.byPath[key] = append(m.byPath[key], backupPath)
	m.pruneLocked(key)
	return fileBackupRecord{Path: backupPath, Size: int64(len(data))}, nil
}

func (m *fileBackupManager) pruneLocked(key string) {
	paths := m.byPath[key]
	if len(paths) <= maxToolBackupsPerPath {
		return
	}
	removeCount := len(paths) - maxToolBackupsPerPath
	for _, path := range paths[:removeCount] {
		_ = os.Remove(path)
	}
	m.byPath[key] = paths[removeCount:]
}

func (m *fileBackupManager) ensureSessionLimitsLocked(key string, nextBytes int64) error {
	count, totalBytes := m.sessionBackupUsageLocked()
	if paths := m.byPath[key]; len(paths) >= maxToolBackupsPerPath {
		for _, path := range paths[:len(paths)-maxToolBackupsPerPath+1] {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			count--
			totalBytes -= info.Size()
		}
	}
	if count+1 > maxToolBackupsPerSession {
		return fmt.Errorf("the session backup file limit has been reached (%d files)", maxToolBackupsPerSession)
	}
	if totalBytes+nextBytes > maxToolBackupBytesPerSession {
		return fmt.Errorf("the session backup size limit has been reached (%d bytes + %d bytes > %d bytes)", totalBytes, nextBytes, maxToolBackupBytesPerSession)
	}
	return nil
}

func (m *fileBackupManager) sessionBackupUsageLocked() (count int, totalBytes int64) {
	for _, paths := range m.byPath {
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			count++
			totalBytes += info.Size()
		}
	}
	return count, totalBytes
}

func backupFileName(seq int, path, toolName string) string {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || strings.TrimSpace(base) == "" {
		base = "file"
	}
	base = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '_'
		default:
			return r
		}
	}, base)
	return fmt.Sprintf("%012d-before-%s-%s", seq, strings.TrimSpace(toolName), base)
}

func shortPathHash(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:12]
}

// appendBackupNotes reports what happened to a file whose on-disk contents had
// drifted from the model's last observation. The model and the user see exactly
// the same text: a backup is best effort, so a claim that one exists must never
// be made to one audience and withheld from the other.
//
// Nothing is appended when a backup could not be created. The absence of the
// "Backup saved to" line is the honest signal — stating a reason would still be
// telling the reader a safety net was expected. The failure and its cause are
// logged locally by the caller.
func appendBackupNotes(result, toolName string, stale bool, stalePaths int, outcome fileBackupOutcome) string {
	var notes []string
	if stale {
		switch {
		case toolName == tools.NameWrite:
			// write replaces the whole file; unlike edit and apply_patch there
			// are no anchors to re-validate, so do not imply anything was checked.
			notes = append(notes, "Warning: the file changed on disk after your last read, and those contents were replaced by this write.")
		case stalePaths > 1:
			notes = append(notes, "Warning: one or more files changed on disk since their last tracked snapshot; the tool validated current contents before writing and continued.")
		default:
			notes = append(notes, "Warning: the file changed on disk since its last tracked snapshot; the tool validated current contents before writing and continued.")
		}
	}
	for _, backup := range outcome.Records {
		if strings.TrimSpace(backup.Path) != "" {
			notes = append(notes, "Backup saved to: "+backup.Path)
		}
	}
	if len(notes) == 0 {
		return result
	}
	result = strings.TrimRight(result, "\n")
	if result == "" {
		return strings.Join(notes, "\n")
	}
	return result + "\n" + strings.Join(notes, "\n")
}

// readPreWriteBytes loads the current bytes of a file about to be mutated.
//
// Only regular files are read. os.ReadFile follows symlinks, so a symlinked
// path would copy its target — possibly outside the project — into the session
// backup directory, and write refuses to follow symlinks anyway, so that copy
// would be orphaned by a call that never mutates anything. A non-regular path
// reports as absent, which every caller already treats as nothing to back up.
func readPreWriteBytes(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}
