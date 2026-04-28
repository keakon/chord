package recovery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func writeStaleLock(t *testing.T, dir string, info sessionLockInfo) {
	t.Helper()
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal stale lock: %v", err)
	}
	data = append(data, '\n')
	lockPath := lockFilePath(dir)
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}
}

func lockFilePath(dir string) string {
	return filepath.Join(dir, sessionLockFile)
}

func guardFilePath(dir string) string {
	return filepath.Join(dir, sessionGuardFile)
}

func TestSessionLockHelpers(t *testing.T) {
	dir := t.TempDir()
	lockPath := lockFilePath(dir)
	when := mustParseTestTime(t, "2026-04-28T12:00:00Z")
	info := sessionLockInfo{PID: 0, OwnerID: "owner", Hostname: "host", AcquiredAt: when}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLockFile(lockPath, append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	got, err := readLockFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerID != "owner" || got.Hostname != "host" || !got.AcquiredAt.Equal(when) {
		t.Fatalf("readLockFile = %+v", got)
	}

	lockedErr := currentSessionLockedError(dir, lockPath)
	var locked *SessionLockedError
	if !errors.As(lockedErr, &locked) || locked.OwnerID != "owner" || locked.Hostname != "host" {
		t.Fatalf("currentSessionLockedError = %#v", lockedErr)
	}
	if !errors.Is((&SessionLockCorruptError{SessionDir: dir, Err: os.ErrInvalid}), os.ErrInvalid) {
		t.Fatal("SessionLockCorruptError should unwrap")
	}
	if !isProcessAlive(os.Getpid()) || isProcessAlive(0) || isProcessAlive(-1) {
		t.Fatal("isProcessAlive returned unexpected values")
	}
	if id := newOwnerID(); len(id) != 32 {
		t.Fatalf("newOwnerID length = %d, want 32", len(id))
	}
}

func mustParseTestTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAcquireSessionLock_FirstAcquire(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	defer func() { _ = lock.Release() }()

	if _, err := os.Stat(lockFilePath(dir)); err != nil {
		t.Fatalf("lock file should exist: %v", err)
	}
	if _, err := os.Stat(guardFilePath(dir)); err != nil {
		t.Fatalf("guard file should exist: %v", err)
	}
	info, err := readLockFile(lockFilePath(dir))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("PID mismatch: got %d, want %d", info.PID, os.Getpid())
	}
	if info.OwnerID == "" {
		t.Error("owner_id should not be empty")
	}
}

func TestAcquireSessionLock_AlreadyHeld(t *testing.T) {
	dir := t.TempDir()
	lock1, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = lock1.Release() }()

	_, err = AcquireSessionLock(dir)
	if err == nil {
		t.Fatal("expected error on second acquire of live-process lock")
	}
	var locked *SessionLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("expected SessionLockedError, got %T: %v", err, err)
	}
}

func TestAcquireSessionLock_StaleMetadataTakeover(t *testing.T) {
	dir := t.TempDir()
	writeStaleLock(t, dir, sessionLockInfo{PID: 99999999, OwnerID: "stale-owner"})

	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("expected stale lock to be taken over, got: %v", err)
	}
	defer func() { _ = lock.Release() }()

	info, err := readLockFile(lockFilePath(dir))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if info.OwnerID == "stale-owner" {
		t.Error("expected new owner after stale takeover")
	}
}

func TestAcquireSessionLock_CorruptLockFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(lockFilePath(dir), []byte("not-json\n"), 0o600); err != nil {
		t.Fatalf("write corrupt lock: %v", err)
	}
	_, err := AcquireSessionLock(dir)
	if err == nil {
		t.Fatal("expected error for corrupt lock file")
	}
	var corruptErr *SessionLockCorruptError
	if !errors.As(err, &corruptErr) {
		t.Fatalf("expected SessionLockCorruptError, got %T: %v", err, err)
	}
}

func TestRelease_OnlyDeletesOwnLock(t *testing.T) {
	dir := t.TempDir()
	lock1, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}

	// Simulate another process taking over the metadata file while we still own the guard.
	writeStaleLock(t, dir, sessionLockInfo{PID: os.Getpid(), OwnerID: "other-owner"})

	if err := lock1.Release(); err != nil {
		t.Fatalf("release error: %v", err)
	}
	if _, err := os.Stat(lockFilePath(dir)); err != nil {
		t.Error("lock file should still exist after mismatched release")
	}
}

func TestRelease_DeletesOwnLock(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(lockFilePath(dir)); !os.IsNotExist(err) {
		t.Error("lock file should be deleted after release")
	}
}

func TestRelease_Idempotent(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Errorf("second release should not error, got %v", err)
	}
}

func TestAcquireSessionLock_ConcurrentSameProcess(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer func() { _ = lock.Release() }()

	var wg sync.WaitGroup
	errorCount := 0
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := AcquireSessionLock(dir)
			if err != nil {
				mu.Lock()
				errorCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if errorCount != 8 {
		t.Errorf("expected all 8 concurrent acquires to fail, got %d failures", errorCount)
	}
}

func TestSessionDirLockedByLiveOwner_UsesGuardProbe(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	if err := os.Remove(lockFilePath(dir)); err != nil {
		t.Fatalf("remove lock metadata: %v", err)
	}
	locked, err := sessionDirLockedByLiveOwner(dir)
	if err != nil {
		t.Fatalf("sessionDirLockedByLiveOwner: %v", err)
	}
	if !locked {
		t.Fatal("expected guard probe to report locked even without metadata file")
	}
}

func TestAcquireSessionLock_GuardLockWithoutMetadataStillBlocks(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireSessionLock(dir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	defer func() { _ = lock.Release() }()

	if err := os.Remove(lockFilePath(dir)); err != nil {
		t.Fatalf("remove lock metadata: %v", err)
	}
	_, err = AcquireSessionLock(dir)
	if err == nil {
		t.Fatal("expected guard lock to block second acquire even without metadata file")
	}
	var locked *SessionLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("expected SessionLockedError, got %T: %v", err, err)
	}
}
