package recovery

import (
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type sessionLockReaderFunc func([]byte) (int, error)

func (f sessionLockReaderFunc) Read(p []byte) (int, error) { return f(p) }

func TestAcquireExistingSessionLockRejectsRenamedDirectory(t *testing.T) {
	dir := writeSessionWithTranscript(t, t.TempDir(), "session")
	original := rand.Reader
	defer func() { rand.Reader = original }()
	rand.Reader = sessionLockReaderFunc(func(p []byte) (int, error) {
		if err := os.Rename(dir, dir+SessionDeletingSuffix); err != nil {
			return 0, err
		}
		return io.ReadFull(original, p)
	})
	lock, err := AcquireExistingSessionLock(dir)
	if lock != nil {
		_ = lock.Release()
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acquire renamed directory: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recreated original directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir+SessionDeletingSuffix, sessionLockFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed ownership after rename: %v", err)
	}
}

func TestSessionLockReleaseUsesOriginalDirectory(t *testing.T) {
	dir := writeSessionWithTranscript(t, t.TempDir(), "session")
	lock, err := AcquireExistingSessionLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err = os.Rename(dir, dir+SessionDeletingSuffix); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement, err := AcquireExistingSessionLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Release()
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir+SessionDeletingSuffix, sessionLockFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old ownership metadata retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, sessionLockFile)); err != nil {
		t.Fatalf("removed replacement ownership metadata: %v", err)
	}
}

func TestAcquireExistingSessionLockRejectsTombstonePath(t *testing.T) {
	dir := writeSessionWithTranscript(t, t.TempDir(), "session"+SessionDeletingSuffix)
	for _, path := range []string{dir, dir + string(filepath.Separator), filepath.Join(dir, ".")} {
		lock, err := AcquireExistingSessionLock(path)
		if lock != nil {
			_ = lock.Release()
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("acquire tombstone %q: %v", path, err)
		}
	}
}

func TestSessionDirectoryLockRejectsUnlinkedGuardAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("CHORD_TEST_PREOPENED_SESSION"); dir != "" {
		lock, err := openSessionDirectoryLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		// stdout and stdin form explicit barriers with the parent; no sleeps
		// decide whether deletion wins before the guard is acquired.
		if _, err := os.Stdout.WriteString("ready\n"); err != nil {
			t.Fatal(err)
		}
		released := make(chan error, 1)
		go func() {
			var signal [1]byte
			_, err := io.ReadFull(os.Stdin, signal[:])
			released <- err
		}()
		select {
		case err := <-released:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("parent did not release the acquisition barrier")
		}
		if err := lockSessionGuardFile(lock.guard); err != nil {
			_ = lock.guard.Close()
			_ = lock.root.Close()
			t.Fatal(err)
		}
		defer lock.Close()
		if err := lock.verify(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("accepted detached guard: %v", err)
		}
		return
	}
	dir := writeSessionWithTranscript(t, t.TempDir(), "session")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSessionDirectoryLockRejectsUnlinkedGuardAcrossProcesses$", "-test.timeout=15s")
	cmd.Env = append(os.Environ(), "CHORD_TEST_PREOPENED_SESSION="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	ready := make(chan error, 1)
	go func() {
		var signal [6]byte
		_, err := io.ReadFull(stdout, signal[:])
		if err == nil && string(signal[:]) != "ready\n" {
			err = errors.New("unexpected child readiness signal")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not open session before timeout")
	}
	if err := DeleteSessionDir(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _ = io.Copy(io.Discard, stdout)
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child guard verification failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not finish before timeout")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted directory recreated: %v", err)
	}
}
