package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStdioTransportCloseKillsAfterShortGrace(t *testing.T) {
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
