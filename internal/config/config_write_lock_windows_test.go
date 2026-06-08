//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWindowsLockConfigMutationSerializesConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	first, err := LockConfigMutation(path)
	if err != nil {
		t.Fatalf("LockConfigMutation first: %v", err)
	}

	acquired := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		second, err := LockConfigMutation(path)
		if err != nil {
			done <- err
			return
		}
		close(acquired)
		done <- second.Close()
	}()

	select {
	case <-acquired:
		t.Fatal("second lock acquired before first was released")
	case <-time.After(100 * time.Millisecond):
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second lock result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second lock")
	}
}

func TestWindowsAuthFileLockReentrantAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.yaml")
	lock, err := lockAuthYAMLFile(path)
	if err != nil {
		t.Fatalf("lockAuthYAMLFile first: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}
	lock, err = lockAuthYAMLFile(path)
	if err != nil {
		t.Fatalf("lockAuthYAMLFile second: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close second lock: %v", err)
	}
}

func TestWindowsConcurrentAuthSavesDoNotCorruptFile(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	path := filepath.Join(configHome, "auth.yaml")

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			provider := fmt.Sprintf("provider-%d", i)
			changed, err := UpsertAPIKeyCredentialInFile(path, provider, fmt.Sprintf("key-%d", i))
			if err != nil {
				errCh <- err
				return
			}
			if !changed {
				errCh <- fmt.Errorf("credential for %s was not inserted", provider)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent save: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth.yaml: %v", err)
	}
	loaded, err := LoadAuthConfig(path)
	if err != nil {
		t.Fatalf("load auth.yaml after concurrent saves: %v\n%s", err, data)
	}
	if len(loaded) != 8 {
		t.Fatalf("providers = %d, want 8; file:\n%s", len(loaded), data)
	}
}
