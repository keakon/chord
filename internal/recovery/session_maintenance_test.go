package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWithInactiveSessionExcludesResumeAndDeletion(t *testing.T) {
	dir := t.TempDir()
	callbackErr := errors.New("maintenance failed")
	err := WithInactiveSession(dir, func(root *os.Root) error {
		lock, err := AcquireExistingSessionLock(dir)
		if lock != nil {
			_ = lock.Release()
		}
		if _, ok := errors.AsType[*SessionLockedError](err); !ok {
			t.Fatalf("resume during maintenance = %v, want locked", err)
		}
		if _, ok := errors.AsType[*SessionLockedError](DeleteSessionDir(dir, nil)); !ok {
			t.Fatal("deletion must not overlap maintenance")
		}
		if _, err := root.Stat(sessionLockFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("maintenance wrote ownership metadata: %v", err)
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("maintenance error = %v, want callback error", err)
	}
	lock, err := AcquireExistingSessionLock(dir)
	if err != nil {
		t.Fatalf("resume after maintenance failure: %v", err)
	}
	defer lock.Release()
	called := false
	err = WithInactiveSession(dir, func(*os.Root) error {
		called = true
		return nil
	})
	if _, ok := errors.AsType[*SessionLockedError](err); !ok || called {
		t.Fatalf("maintenance on resumed session = %v, callback called = %v", err, called)
	}
}

func TestWithInactiveSessionRejectsLiveOrCorruptMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(*testing.T, string)
	}{
		{"live owner", func(t *testing.T, dir string) {
			writeStaleLock(t, dir, sessionLockInfo{PID: os.Getpid(), OwnerID: "owner"})
		}},
		{"corrupt owner", func(t *testing.T, dir string) {
			if err := os.WriteFile(lockFilePath(dir), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.write(t, dir)
			called := false
			err := WithInactiveSession(dir, func(*os.Root) error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatalf("maintenance = %v, callback called = %v", err, called)
			}
			if err := os.Remove(lockFilePath(dir)); err != nil {
				t.Fatal(err)
			}
			if err := WithInactiveSession(dir, func(*os.Root) error { return nil }); err != nil {
				t.Fatalf("guard leaked after refusing maintenance: %v", err)
			}
		})
	}
}

func TestWithInactiveSessionDoesNotCreateMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	err := WithInactiveSession(dir, func(*os.Root) error {
		t.Fatal("callback called for missing directory")
		return nil
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance = %v, want missing directory", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance created missing directory: %v", err)
	}
}
