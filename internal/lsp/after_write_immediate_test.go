package lsp

import (
	"context"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/keakon/chord/internal/config"
)

func TestAwaitFreshWaiterReturnsOnVersionedPublishFromImmediateServer(t *testing.T) {
	mgr := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	ch := mgr.PrepareWaiter(path)
	ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "type error"}}, serverID: "gopls", version: 2, receivedAt: time.Now()}

	done := make(chan diagnosticsWaitResult, 1)
	go func() {
		done <- mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
			serverVersions:   map[string]int32{"gopls": 2},
			settle:           time.Hour,
			immediateServers: map[string]bool{"gopls": true},
		}, time.Hour)
	}()
	select {
	case got := <-done:
		if !got.notified || len(got.diagnostics) != 1 || got.diagnostics[0].Message != "type error" {
			t.Fatalf("AwaitFreshWaiter = %+v, want the versioned publish", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a versioned publish from an immediate server still waited for the settle window")
	}
}

func TestAwaitFreshWaiterSettlesWhenAnotherServerIsNotImmediate(t *testing.T) {
	mgr := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	ch := mgr.PrepareWaiter(path)

	go func() {
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "first"}}, serverID: "gopls", version: 2, receivedAt: time.Now()}
		time.Sleep(10 * time.Millisecond)
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "second"}}, serverID: "other", version: 3, receivedAt: time.Now()}
	}()

	got := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions:   map[string]int32{"gopls": 2, "other": 3},
		settle:           200 * time.Millisecond,
		immediateServers: map[string]bool{"gopls": true},
	}, 5*time.Second)
	if !got.notified || len(got.missingServers) != 0 || len(got.diagnostics) != 2 ||
		got.diagnostics[0].Message != "first" || got.diagnostics[1].Message != "second" {
		t.Fatalf("AwaitFreshWaiter = %+v, want both servers' matching publishes", got)
	}
}

func TestAwaitFreshWaiterKeepsAcceptedPublishesOnCancellationDuringSettle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(&config.Config{}, resolvedTempDir(t), nil)
		path := filepath.Join(m.projectRootPath(), "main.go")
		ch := m.PrepareWaiter(path)
		ch <- diagnosticsEvent{serverID: "sample", version: 2, seq: 1, diagnostics: []Diagnostic{{Severity: 1, Message: "transient"}}}
		ch <- diagnosticsEvent{serverID: "sample", version: 2, seq: 2}
		ch <- diagnosticsEvent{serverID: "other", version: 2, seq: 3, diagnostics: []Diagnostic{{Severity: 1, Message: "current"}}}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			time.Sleep(time.Millisecond)
			cancel()
		}()
		got := m.AwaitFreshWaiter(ctx, path, ch, diagnosticsWaitRequest{
			serverVersions: map[string]int32{"sample": 2, "other": 2},
			settle:         time.Hour,
		}, time.Hour)
		if !got.notified || got.lastSeq != 3 || len(got.missingServers) != 0 ||
			len(got.diagnostics) != 1 || got.diagnostics[0].Message != "current" {
			t.Fatalf("wait result = %+v; want accepted publishes, including the clean replacement from sample", got)
		}
	})
}

// TestAwaitFreshWaiterReturnsOnUnversionedPublishFromImmediateServer pins the
// publishes-once contract for servers that never echo document versions
// (typescript-language-server): their first publish after the sync is the
// version check, so it ends the wait instead of paying the settle window.
func TestAwaitFreshWaiterReturnsOnUnversionedPublishFromImmediateServer(t *testing.T) {
	mgr := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	ch := mgr.PrepareWaiter(path)
	after := time.Now()

	go func() {
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "first"}}, serverID: "tsserver", receivedAt: time.Now()}
		time.Sleep(10 * time.Millisecond)
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "second"}}, serverID: "tsserver", receivedAt: time.Now()}
	}()

	got := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions:     map[string]int32{"tsserver": 2},
		after:              after,
		settle:             time.Hour,
		immediateServers:   map[string]bool{"tsserver": true},
		unversionedServers: map[string]bool{"tsserver": true},
	}, 5*time.Second)
	if !got.notified || len(got.diagnostics) != 1 || got.diagnostics[0].Message != "first" {
		t.Fatalf("AwaitFreshWaiter = %+v, want the first publish to end the wait", got)
	}
}

// TestAwaitFreshWaiterSettlesUnversionedPublishFromOtherServers keeps the
// settle window for a server that is not known to publish once per version:
// its unversioned publish may be the first of several passes.
func TestAwaitFreshWaiterSettlesUnversionedPublishFromOtherServers(t *testing.T) {
	mgr := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	ch := mgr.PrepareWaiter(path)
	after := time.Now()

	go func() {
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "first"}}, serverID: "other", receivedAt: time.Now()}
		time.Sleep(10 * time.Millisecond)
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "second"}}, serverID: "other", receivedAt: time.Now()}
	}()

	got := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions:   map[string]int32{"other": 2},
		after:            after,
		settle:           200 * time.Millisecond,
		immediateServers: map[string]bool{"gopls": true},
	}, 5*time.Second)
	if !got.notified || len(got.diagnostics) != 1 || got.diagnostics[0].Message != "second" {
		t.Fatalf("AwaitFreshWaiter = %+v, want an unversioned publish to keep the settle window", got)
	}
}

// TestAwaitFreshWaiterSettlesUnversionedPublishFromVersioningImmediateServer
// pins the gopls half of the unversioned rule: gopls publishes without a
// version while the workspace is still loading and the document is not open
// yet, so consuming that publish as the synced version's result would report
// a stale snapshot as fresh. Only servers that never version their publishes
// may confirm an immediate wait on one.
func TestAwaitFreshWaiterSettlesUnversionedPublishFromVersioningImmediateServer(t *testing.T) {
	mgr := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	ch := mgr.PrepareWaiter(path)
	after := time.Now()

	go func() {
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "workspace load snapshot"}}, serverID: "gopls", receivedAt: time.Now()}
		time.Sleep(10 * time.Millisecond)
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "synced version"}}, serverID: "gopls", version: 2, receivedAt: time.Now()}
	}()

	got := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions:   map[string]int32{"gopls": 2},
		after:            after,
		settle:           200 * time.Millisecond,
		immediateServers: map[string]bool{"gopls": true},
	}, 5*time.Second)
	if !got.notified || len(got.diagnostics) != 1 || got.diagnostics[0].Message != "synced version" {
		t.Fatalf("AwaitFreshWaiter = %+v, want the unversioned snapshot to keep the wait open", got)
	}
}

func TestImmediateDiagnosticsServersSelectsPublishesOnceServers(t *testing.T) {
	mgr := NewManager(&config.Config{LSP: config.LSPConfig{
		"go":      {Command: "/usr/local/bin/gopls"},
		"pyright": {Command: "pyright-langserver"},
		"ts":      {Command: "typescript-language-server"},
	}}, resolvedTempDir(t), nil)

	got := mgr.immediateDiagnosticsServers(map[string]int32{"go": 1, "pyright": 4, "ts": 2, "unknown": 3})
	if len(got) != 1 || !got["go"] {
		t.Fatalf("immediateDiagnosticsServers = %v, want the publishes-once servers only", got)
	}
}
