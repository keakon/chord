package runtimecache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keakon/chord/internal/config"
)

const (
	sessionCacheDirName = "session-cache"
	activeLockFileName  = "active.lock"
	metaFileName        = "meta.json"
	viewportDirName     = "viewport"
	imageOpenDirName    = "image-open"
	startupCleanupLimit = 20
)

type Manager struct {
	root string
}

type SessionHandle struct {
	mu         sync.Mutex
	dir        string
	projectDir string
	sessionID  string
	lockPath   string
	lockFile   *os.File
}

type sessionMeta struct {
	ProjectRoot string    `json:"project_root"`
	SessionID   string    `json:"session_id"`
	CreatedAt   time.Time `json:"created_at"`
	PID         int       `json:"pid"`
}

type cleanupCandidate struct {
	projectDir string
	sessionDir string
	sessionID  string
	modTime    time.Time
	hasModTime bool
}

func NewManager(cacheDir string) (*Manager, error) {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		return nil, fmt.Errorf("runtime cache dir is empty")
	}
	root := filepath.Join(cacheDir, "runtime", sessionCacheDirName)
	return &Manager{root: root}, nil
}

func (m *Manager) Root() string {
	if m == nil {
		return ""
	}
	return m.root
}

func (m *Manager) OpenSession(projectRoot, sessionID string) (*SessionHandle, error) {
	if m == nil {
		return nil, fmt.Errorf("runtime cache manager is nil")
	}
	sessionDir, err := m.sessionDir(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(sessionDir), 0o700); err != nil {
		return nil, fmt.Errorf("create runtime cache project dir: %w", err)
	}
	if err := m.removeSessionDirIfInactive(sessionDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, viewportDirName), 0o700); err != nil {
		return nil, fmt.Errorf("create viewport cache dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, imageOpenDirName), 0o700); err != nil {
		return nil, fmt.Errorf("create image-open cache dir: %w", err)
	}
	lockPath := filepath.Join(sessionDir, activeLockFileName)
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open runtime cache lock: %w", err)
	}
	if err := lockFileExclusiveNonBlocking(lockFile); err != nil {
		_ = lockFile.Close()
		if isWouldBlock(err) {
			return nil, fmt.Errorf("runtime cache for session %s is already active", sessionID)
		}
		return nil, fmt.Errorf("lock runtime cache: %w", err)
	}
	if err := writeSessionMeta(filepath.Join(sessionDir, metaFileName), sessionMeta{
		ProjectRoot: projectRoot,
		SessionID:   strings.TrimSpace(sessionID),
		CreatedAt:   time.Now(),
		PID:         os.Getpid(),
	}); err != nil {
		_ = unlockAndClose(lockFile)
		return nil, err
	}
	return &SessionHandle{
		dir:        sessionDir,
		projectDir: projectRoot,
		sessionID:  strings.TrimSpace(sessionID),
		lockPath:   lockPath,
		lockFile:   lockFile,
	}, nil
}

func (m *Manager) CleanupStaleSessions(ctx context.Context) error {
	if m == nil || strings.TrimSpace(m.root) == "" {
		return nil
	}
	var errs []error
	candidates, err := m.cleanupCandidates(startupCleanupLimit)
	if err != nil {
		return err
	}
	visitedProjects := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if err := m.removeSessionDirIfInactive(candidate.sessionDir); err != nil {
			errs = append(errs, fmt.Errorf("cleanup runtime cache %s: %w", candidate.sessionDir, err))
		}
		visitedProjects[candidate.projectDir] = struct{}{}
	}
	for projectDir := range visitedProjects {
		_ = removeDirIfEmpty(projectDir)
	}
	return errors.Join(errs...)
}

func (m *Manager) cleanupCandidates(limit int) ([]cleanupCandidate, error) {
	if m == nil || strings.TrimSpace(m.root) == "" {
		return nil, nil
	}
	projectEntries, err := os.ReadDir(m.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read runtime cache root: %w", err)
	}
	candidates := make([]cleanupCandidate, 0, len(projectEntries))
	for _, projectEntry := range projectEntries {
		if !projectEntry.IsDir() {
			continue
		}
		projectDir := filepath.Join(m.root, projectEntry.Name())
		sessionEntries, err := os.ReadDir(projectDir)
		if err != nil {
			return nil, fmt.Errorf("read runtime cache project dir %s: %w", projectDir, err)
		}
		for _, sessionEntry := range sessionEntries {
			if !sessionEntry.IsDir() {
				continue
			}
			sessionDir := filepath.Join(projectDir, sessionEntry.Name())
			candidate := cleanupCandidate{
				projectDir: projectDir,
				sessionDir: sessionDir,
				sessionID:  sessionEntry.Name(),
			}
			if info, err := os.Stat(sessionDir); err == nil {
				candidate.modTime = info.ModTime()
				candidate.hasModTime = !candidate.modTime.IsZero()
			}
			candidates = append(candidates, candidate)
		}
	}
	slices.SortFunc(candidates, compareCleanupCandidate)
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}

func compareCleanupCandidate(a, b cleanupCandidate) int {
	switch {
	case a.hasModTime && b.hasModTime:
		if !a.modTime.Equal(b.modTime) {
			if a.modTime.After(b.modTime) {
				return -1
			}
			return 1
		}
	case a.hasModTime != b.hasModTime:
		if a.hasModTime {
			return -1
		}
		return 1
	}
	if a.sessionID != b.sessionID {
		if a.sessionID > b.sessionID {
			return -1
		}
		return 1
	}
	if a.projectDir < b.projectDir {
		return -1
	}
	if a.projectDir > b.projectDir {
		return 1
	}
	return 0
}

func (m *Manager) sessionDir(projectRoot, sessionID string) (string, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	sessionID = strings.TrimSpace(sessionID)
	if projectRoot == "" {
		return "", fmt.Errorf("runtime cache project root is empty")
	}
	if sessionID == "" {
		return "", fmt.Errorf("runtime cache session id is empty")
	}
	projectKey, err := projectKey(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(m.root, projectKey, sessionID), nil
}

func (m *Manager) removeSessionDirIfInactive(sessionDir string) error {
	info, err := os.Stat(sessionDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat runtime cache dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime cache path is not a directory: %s", sessionDir)
	}
	lockPath := filepath.Join(sessionDir, activeLockFileName)
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open runtime cache lock: %w", err)
	}
	defer func() {
		_ = unlockAndClose(lockFile)
	}()
	if err := lockFileExclusiveNonBlocking(lockFile); err != nil {
		if isWouldBlock(err) {
			return nil
		}
		return fmt.Errorf("probe runtime cache lock: %w", err)
	}
	if err := os.RemoveAll(sessionDir); err != nil {
		return fmt.Errorf("remove runtime cache dir: %w", err)
	}
	return nil
}

func projectKey(projectRoot string) (string, error) {
	canonical, err := config.CanonicalProjectRoot(projectRoot)
	if err != nil {
		return "", err
	}
	logical, _ := config.LogicalProjectRoot(canonical)
	return config.SanitizeProjectKey(logical), nil
}

func writeSessionMeta(path string, meta sessionMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime cache meta: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write runtime cache meta tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename runtime cache meta: %w", err)
	}
	return nil
}

func isWouldBlock(err error) bool {
	return isWouldBlockLock(err)
}

func unlockAndClose(f *os.File) error {
	if f == nil {
		return nil
	}
	var firstErr error
	if err := unlockFile(f); err != nil && !errors.Is(err, fs.ErrClosed) {
		firstErr = err
	}
	if err := f.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func removeDirIfEmpty(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
		return err
	}
	return nil
}

func (h *SessionHandle) Dir() string {
	if h == nil {
		return ""
	}
	return h.dir
}

func (h *SessionHandle) SessionID() string {
	if h == nil {
		return ""
	}
	return h.sessionID
}

func (h *SessionHandle) ViewportDir() string {
	if h == nil {
		return ""
	}
	return filepath.Join(h.dir, viewportDirName)
}

func (h *SessionHandle) ViewportSpillPath() string {
	if h == nil {
		return ""
	}
	return filepath.Join(h.ViewportDir(), "spill.log")
}

func (h *SessionHandle) ImageOpenDir() string {
	if h == nil {
		return ""
	}
	return filepath.Join(h.dir, imageOpenDirName)
}

func (h *SessionHandle) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lockFile == nil {
		return nil
	}
	err := unlockAndClose(h.lockFile)
	h.lockFile = nil
	return err
}

func (h *SessionHandle) Remove() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	dir := h.dir
	lockFile := h.lockFile
	h.lockFile = nil
	h.mu.Unlock()

	if lockFile != nil {
		if err := unlockAndClose(lockFile); err != nil {
			return err
		}
	}
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove runtime cache dir %s: %w", dir, err)
	}
	_ = removeDirIfEmpty(filepath.Dir(dir))
	return nil
}
