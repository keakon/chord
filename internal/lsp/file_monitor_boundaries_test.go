package lsp

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestFileMaintenanceVerifiesWriteWithUnchangedMetadata(t *testing.T) {
	for _, content := range []string{"package sample\n", "package change\n"} {
		t.Run(content, func(t *testing.T) {
			m, c, fake, path := monitoredTestFile(t)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil || !sameDiskFile(before, after) {
				t.Fatalf("metadata changed: %v", err)
			}
			cursor := 0
			m.reconcileFiles(context.Background(), map[string]os.FileInfo{path: before}, map[string]fsnotify.Op{path: fsnotify.Write}, &cursor, false)
			changed := content != "package sample\n"
			if (len(c.openFiles) == 0) != changed || (len(fake.didCloseURIs) == 1) != changed {
				t.Fatalf("open=%v close=%v changed=%v", c.openFiles, fake.didCloseURIs, changed)
			}
			if changed && (len(c.GetDiagnostics(path)) != 0 || len(m.diagByServer) != 0) {
				t.Fatal("stale diagnostics retained")
			}
		})
	}
}

func TestMonitoredDiskDigestBudgetAndErrors(t *testing.T) {
	_, _, _, path := monitoredTestFile(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	budget := info.Size()
	if _, ok := monitoredDiskDigest(path, info, &budget); ok {
		t.Fatal("exceeded read budget")
	}
	budget = fileDigestBudget
	if _, ok := monitoredDiskDigest(path, info, &budget); !ok || budget != fileDigestBudget-info.Size() {
		t.Fatalf("digest budget=%d ok=%v", budget, ok)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := monitoredDiskDigest(path, info, &budget); ok {
		t.Fatal("missing file verified")
	}
}

type stalledShutdownClient struct {
	fakePowernapClient
	deadline time.Time
	kills    atomic.Int32
}

func (f *stalledShutdownClient) Shutdown(ctx context.Context) error {
	f.deadline, _ = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}
func (f *stalledShutdownClient) Kill() { f.kills.Add(1) }

func TestIdleReclamationSharesShutdownDeadline(t *testing.T) {
	m, c, _, _ := monitoredTestFile(t)
	fake := &stalledShutdownClient{}
	c.client = fake
	c.openFiles = nil
	c.lastUsed.Store(time.Now().Add(-workspaceIdleTimeout - time.Second).UnixNano())
	second := &stalledShutdownClient{}
	other := newResyncTestClient(&second.fakePowernapClient)
	other.client = second
	other.cwd = c.cwd
	other.lastUsed.Store(c.lastUsed.Load())
	m.clients[clientKey{name: "other", root: c.cwd}] = other
	started := time.Now()
	m.pruneMissingRoots(context.Background())
	elapsed := time.Since(started)
	if len(m.clients) != 0 || fake.kills.Load() != 1 || second.kills.Load() != 1 {
		t.Fatal("idle clients were not terminated")
	}
	// Exactly one server can consume the shared budget; the other is killed
	// without waiting for another shutdown RPC.
	for _, deadline := range []time.Time{fake.deadline, second.deadline} {
		if !deadline.IsZero() && (deadline.Before(started) || deadline.After(started.Add(workspaceCloseTimeout+100*time.Millisecond))) {
			t.Fatalf("unbounded deadline: %v", deadline)
		}
	}
	if fake.deadline.IsZero() && second.deadline.IsZero() {
		t.Fatal("no graceful shutdown attempted")
	}
	if elapsed > 2*workspaceCloseTimeout {
		t.Fatalf("batch took %v", elapsed)
	}
}

func TestClientCloseDeadlineIncludesLifecycleLock(t *testing.T) {
	fake := &stalledShutdownClient{}
	c := newResyncTestClient(&fake.fakePowernapClient)
	c.client = fake
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close: %v", err)
	}
	if fake.kills.Load() != 1 {
		t.Fatal("blocked writer transport was not killed")
	}
}

type stalledExitClient struct {
	fakePowernapClient
	killed chan struct{}
}

func (f *stalledExitClient) Exit() error { <-f.killed; return context.DeadlineExceeded }
func (f *stalledExitClient) Kill()       { close(f.killed) }

func TestClientCloseDeadlineInterruptsExit(t *testing.T) {
	fake := &stalledExitClient{killed: make(chan struct{})}
	c := &Client{client: fake}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close: %v", err)
	}
	if !c.closed || fake.shutdowns != 1 {
		t.Fatal("client did not finish close")
	}
}
