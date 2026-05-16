package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteConfigFileAtomicallyCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("providers:\n  openai:\n    type: responses\n")
	if err := WriteConfigFileAtomically(path, data, 0o644); err != nil {
		t.Fatalf("WriteConfigFileAtomically: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("file content = %q, want %q", got, data)
	}
}

func TestWriteConfigFileAtomicallyRejectsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := WriteConfigFileAtomically(path, []byte("new"), 0o644); err == nil {
		t.Fatal("expected existing file write to fail")
	} else if !os.IsExist(err) {
		t.Fatalf("expected os.ErrExist, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("existing file changed: %q", got)
	}
}

func TestLockConfigMutationSerializesWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	firstLock, err := LockConfigMutation(path)
	if err != nil {
		t.Fatalf("LockConfigMutation first: %v", err)
	}
	defer func() { _ = firstLock.Close() }()

	acquired := make(chan struct{})
	released := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lock, err := LockConfigMutation(path)
		if err != nil {
			t.Errorf("LockConfigMutation second: %v", err)
			close(acquired)
			close(released)
			return
		}
		close(acquired)
		_ = lock.Close()
		close(released)
	}()

	select {
	case <-acquired:
		t.Fatal("second lock acquired before first was released")
	default:
	}

	if err := firstLock.Close(); err != nil {
		t.Fatalf("Close first lock: %v", err)
	}

	<-acquired
	<-released
	wg.Wait()
}
