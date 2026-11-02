package lsp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func TestAwaitFreshWaiterReturnsOnVersionedPublishFromImmediateServer(t *testing.T) {
	mgr := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	ch := mgr.PrepareWaiter(path)
	ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "type error"}}, serverID: "gopls", version: 2, receivedAt: time.Now()}

	type result struct {
		diags []Diagnostic
		ok    bool
	}
	done := make(chan result, 1)
	go func() {
		diags, ok, _, _ := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
			serverVersions:   map[string]int32{"gopls": 2},
			settle:           time.Hour,
			immediateServers: map[string]bool{"gopls": true},
		}, time.Hour)
		done <- result{diags, ok}
	}()
	select {
	case got := <-done:
		if !got.ok || len(got.diags) != 1 || got.diags[0].Message != "type error" {
			t.Fatalf("AwaitFreshWaiter = (%+v, %v), want the versioned publish", got.diags, got.ok)
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

	diags, ok, _, _ := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions:   map[string]int32{"gopls": 2, "other": 3},
		settle:           200 * time.Millisecond,
		immediateServers: map[string]bool{"gopls": true},
	}, 5*time.Second)
	if !ok || len(diags) != 1 || diags[0].Message != "second" {
		t.Fatalf("AwaitFreshWaiter = (%+v, %v), want the settled second publish", diags, ok)
	}
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

	diags, ok, _, _ := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions:     map[string]int32{"tsserver": 2},
		after:              after,
		settle:             time.Hour,
		immediateServers:   map[string]bool{"tsserver": true},
		unversionedServers: map[string]bool{"tsserver": true},
	}, 5*time.Second)
	if !ok || len(diags) != 1 || diags[0].Message != "first" {
		t.Fatalf("AwaitFreshWaiter = (%+v, %v), want the first publish to end the wait", diags, ok)
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

	diags, ok, _, _ := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions:   map[string]int32{"other": 2},
		after:            after,
		settle:           200 * time.Millisecond,
		immediateServers: map[string]bool{"gopls": true},
	}, 5*time.Second)
	if !ok || len(diags) != 1 || diags[0].Message != "second" {
		t.Fatalf("AwaitFreshWaiter = (%+v, %v), want an unversioned publish to keep the settle window", diags, ok)
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

	diags, ok, _, _ := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions:   map[string]int32{"gopls": 2},
		after:            after,
		settle:           200 * time.Millisecond,
		immediateServers: map[string]bool{"gopls": true},
	}, 5*time.Second)
	if !ok || len(diags) != 1 || diags[0].Message != "synced version" {
		t.Fatalf("AwaitFreshWaiter = (%+v, %v), want the unversioned snapshot to keep the wait open", diags, ok)
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
