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

func TestAwaitFreshWaiterIgnoresUnversionedPublishForImmediateReturn(t *testing.T) {
	mgr := NewManager(&config.Config{}, resolvedTempDir(t), nil)
	path := filepath.Join(mgr.projectRootPath(), "main.go")
	ch := mgr.PrepareWaiter(path)
	after := time.Now()

	go func() {
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "first"}}, serverID: "gopls", receivedAt: time.Now()}
		time.Sleep(10 * time.Millisecond)
		ch <- diagnosticsEvent{diagnostics: []Diagnostic{{Severity: 1, Message: "second"}}, serverID: "gopls", receivedAt: time.Now()}
	}()

	diags, ok, _, _ := mgr.AwaitFreshWaiter(context.Background(), path, ch, diagnosticsWaitRequest{
		serverVersions:   map[string]int32{"gopls": 2},
		after:            after,
		settle:           200 * time.Millisecond,
		immediateServers: map[string]bool{"gopls": true},
	}, 5*time.Second)
	if !ok || len(diags) != 1 || diags[0].Message != "second" {
		t.Fatalf("AwaitFreshWaiter = (%+v, %v), want an unversioned publish to keep the settle window", diags, ok)
	}
}

func TestImmediateDiagnosticsServersSelectsGoplsOnly(t *testing.T) {
	mgr := NewManager(&config.Config{LSP: config.LSPConfig{
		"go":      {Command: "/usr/local/bin/gopls"},
		"pyright": {Command: "pyright-langserver"},
	}}, resolvedTempDir(t), nil)

	got := mgr.immediateDiagnosticsServers(map[string]int32{"go": 1, "pyright": 4, "unknown": 2})
	if len(got) != 1 || !got["go"] {
		t.Fatalf("immediateDiagnosticsServers = %v, want only the gopls server", got)
	}
}
