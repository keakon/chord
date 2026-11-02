package lsp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func TestPythonWriteStatusDistinguishesSkippedFailedAndSuccessfulBackend(t *testing.T) {
	oldLook, oldRun := lookPath, runCommandContext
	t.Cleanup(func() { lookPath, runCommandContext = oldLook, oldRun })
	root := resolvedTempDir(t)
	m := NewManager(&config.Config{Diagnostics: config.DefaultDiagnosticsConfig()}, root, nil)
	path := filepath.Join(root, "sample.py")
	for _, tc := range []struct {
		name   string
		found  bool
		err    error
		want   FileWriteStatus
		detail string
	}{
		{"skipped", false, nil, FileWriteStatusBackendUnavailable, "no Python diagnostics backend available"},
		{"failed", true, errors.New("checker failed"), FileWriteStatusBackendUnavailable, "checker failed"},
		{"success", true, nil, FileWriteStatusReported, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookPath = func(string) (string, error) {
				if !tc.found {
					return "", errors.New("checker unavailable")
				}
				return "sample-checker", nil
			}
			runCommandContext = func(context.Context, string, ...string) ([]byte, error) { return []byte("[]"), tc.err }
			result := m.afterWritePythonToolResult(context.Background(), path, "x = 1", "", false, nil, WatchedFileChanged, root)
			if result.Status != tc.want || !strings.Contains(result.Detail, tc.detail) {
				t.Fatalf("result = %+v", result)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := m.afterWritePythonToolResult(ctx, path, "x = 1", "", false, nil, WatchedFileChanged, root)
	if result.Status != FileWriteStatusCancelled {
		t.Fatalf("cancelled status = %v", result.Status)
	}
}

func TestPythonWriteStatusReportsLargeFileSkip(t *testing.T) {
	oldLook := lookPath
	t.Cleanup(func() { lookPath = oldLook })
	lookPath = func(string) (string, error) { return "", errors.New("checker unavailable") }
	root := resolvedTempDir(t)
	cfg := &config.Config{Diagnostics: config.DefaultDiagnosticsConfig(), LSP: config.LSPConfig{
		"samplelsp": {Command: "samplelsp", FileTypes: []string{".py"}},
	}}
	cfg.Diagnostics.Python.SemanticBackend.Server = "samplelsp"
	m := NewManager(cfg, root, nil)
	result := m.afterWritePythonToolResult(context.Background(), filepath.Join(root, "large.py"), strings.Repeat("x = 1\n", 6000), "", false, nil, WatchedFileChanged, root)
	if result.Status != FileWriteStatusBackendUnavailable || !strings.Contains(result.Detail, "large file") {
		t.Fatalf("result = %+v", result)
	}
}

func TestPythonSemanticTimeoutFallbackStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
		err    error
		want   FileWriteStatus
	}{
		{"success", false, nil, FileWriteStatusReported},
		{"failed", false, errors.New("checker failed"), FileWriteStatusBackendUnavailable},
		{"cancelled", true, nil, FileWriteStatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldLook, oldRun := lookPath, runCommandContext
			oldReady, oldStart, oldAwait := afterWriteHasReadyClient, afterWriteStart, afterWriteAwaitWaiter
			oldWait := afterWriteWaitForClient
			t.Cleanup(func() {
				lookPath, runCommandContext = oldLook, oldRun
				afterWriteHasReadyClient, afterWriteStart, afterWriteAwaitWaiter = oldReady, oldStart, oldAwait
				afterWriteWaitForClient = oldWait
			})
			root := resolvedTempDir(t)
			cfg := &config.Config{Diagnostics: config.DefaultDiagnosticsConfig(), LSP: config.LSPConfig{
				"samplelsp": {Command: "samplelsp", FileTypes: []string{".py"}},
			}}
			cfg.Diagnostics.Python.SemanticBackend.Server = "samplelsp"
			m := NewManager(cfg, root, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lookPath = func(string) (string, error) { return "sample-checker", nil }
			runCommandContext = func(context.Context, string, ...string) ([]byte, error) { return []byte("[]"), tc.err }
			afterWriteHasReadyClient = func(*Manager, string) bool { return true }
			afterWriteStart = func(*Manager, context.Context, string) {}
			afterWriteWaitForClient = func(*Manager, context.Context, string, time.Duration) (*Client, bool) { return &Client{}, true }
			afterWriteAwaitWaiter = func(m *Manager, _ context.Context, p string, ch chan diagnosticsEvent, _ diagnosticsWaitRequest, _ time.Duration) ([]Diagnostic, bool, uint64, map[diagnosticIdentity]struct{}) {
				m.waitersMu.Lock()
				m.removeWaiter(p, ch)
				m.waitersMu.Unlock()
				if tc.cancel {
					cancel()
				}
				return nil, false, 0, nil
			}
			result := m.afterWritePythonToolResult(ctx, filepath.Join(root, "sample.py"), "x = 1", "", false, nil, WatchedFileChanged, root)
			if result.Status != tc.want {
				t.Fatalf("result = %+v, want %v", result, tc.want)
			}
			if tc.err != nil && !strings.Contains(result.Detail, tc.err.Error()) {
				t.Fatalf("failure detail = %q", result.Detail)
			}
		})
	}
}
