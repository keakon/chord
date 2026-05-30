package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteInitialConfigFileWritesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("providers:\n  openai:\n    type: responses\n")
	if err := writeInitialConfigFile(path, data); err != nil {
		t.Fatalf("writeInitialConfigFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("file content = %q, want %q", got, data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Fatalf("config mode = %o, want 644", mode)
	}
	if err := writeInitialConfigFile(path, []byte("other")); err == nil {
		t.Fatal("expected existing config write to fail")
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after second write: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("file content changed: %q", got)
	}
}

func TestWriteInitialConfigFileConcurrentSingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	payloads := [][]byte{
		[]byte("providers:\n  p1:\n    type: responses\n"),
		[]byte("providers:\n  p2:\n    type: responses\n"),
	}
	var wg sync.WaitGroup
	errCh := make(chan error, len(payloads))
	for _, payload := range payloads {
		wg.Go(func() {
			errCh <- writeInitialConfigFile(path, payload)
		})
	}
	wg.Wait()
	close(errCh)

	successes := 0
	failures := 0
	for err := range errCh {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("successes=%d failures=%d, want 1/1", successes, failures)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(payloads[0]) && string(got) != string(payloads[1]) {
		t.Fatalf("unexpected final content: %q", got)
	}
}
