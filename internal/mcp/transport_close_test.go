package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStdioTransportCloseKillsAfterShortGrace(t *testing.T) {
	// Per-test deadlock guard: this test launches a child shell that
	// deliberately traps SIGTERM, so any regression in Close() can hang the
	// whole package. Dump goroutines and panic well before the package-level
	// timeout so the failure is actionable rather than opaque.
	timer := time.AfterFunc(10*time.Second, func() {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		panic("TestStdioTransportCloseKillsAfterShortGrace deadlocked:\n" + string(buf[:n]))
	})
	defer timer.Stop()

	script := filepath.Join(t.TempDir(), "slow-exit.sh")
	content := "#!/bin/sh\ntrap 'sleep 5' TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	tr, err := NewStdioTransport(context.Background(), script, nil, nil)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}

	began := time.Now()
	err = tr.Close()
	elapsed := time.Since(began)
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Close() error = %v", err)
	}
	if elapsed > stdioKillGrace+800*time.Millisecond {
		t.Fatalf("Close() took too long: %v (grace %v)", elapsed, stdioKillGrace)
	}
}
