package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

type fileBackupOutcome struct {
	Records []fileBackupRecord
	Warning string
}

func newFileBackupManager(sessionDir string) *fileBackupManager {
	return &fileBackupManager{sessionDir: strings.TrimSpace(sessionDir), byPath: make(map[string][]string)}
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
}

func (m *fileBackupManager) Backup(path, toolName string, data []byte) (fileBackupRecord, error) {
	if m == nil || strings.TrimSpace(path) == "" || len(data) == 0 {
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
	key := normalizeAgentFilePath(path)
	if key == "" {
		return fileBackupRecord{}, nil
	}
	if err := m.ensureSessionLimitsLocked(key, int64(len(data))); err != nil {
		return fileBackupRecord{}, err
	}
	m.seq++
	name := backupFileName(m.seq, key, toolName)
	dir := filepath.Join(m.sessionDir, "backups", shortPathHash(key))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fileBackupRecord{}, fmt.Errorf("create backup directory: %w", err)
	}
	backupPath := filepath.Join(dir, name)
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
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

func appendBackupNotes(result string, stale bool, stalePaths int, outcome fileBackupOutcome) string {
	var notes []string
	if stale {
		if stalePaths > 1 {
			notes = append(notes, "Warning: one or more files changed on disk since their last tracked snapshot; the tool validated current contents before writing and continued.")
		} else {
			notes = append(notes, "Warning: the file changed on disk since its last tracked snapshot; the tool validated current contents before writing and continued.")
		}
	}
	for _, backup := range outcome.Records {
		if strings.TrimSpace(backup.Path) != "" {
			notes = append(notes, "Backup saved to: "+backup.Path)
		}
	}
	if strings.TrimSpace(outcome.Warning) != "" {
		notes = append(notes, "No backup was created: "+outcome.Warning+".")
	}
	if len(notes) == 0 {
		return result
	}
	if strings.TrimSpace(result) == "" {
		return strings.Join(notes, "\n")
	}
	return result + "\n" + strings.Join(notes, "\n")
}

func staleWritePathCount(trackedFilePath string, deleteLocks *deleteLockSet) int {
	if deleteLocks != nil {
		return len(deleteLocks.paths)
	}
	if strings.TrimSpace(trackedFilePath) != "" {
		return 1
	}
	return 0
}

func readPreWriteBytes(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

func isTrackedFileMutationTool(name string) bool {
	switch name {
	case tools.NameEdit, tools.NameWrite, tools.NameDelete:
		return true
	default:
		return false
	}
}
