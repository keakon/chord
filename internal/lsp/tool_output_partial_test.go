package lsp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestBatchDiagnosticsReportsBackendFailuresWithoutServerCoverage(t *testing.T) {
	root := resolvedTempDir(t)
	m := NewManager(&config.Config{}, root, nil)
	first, second := filepath.Join(root, "first.py"), filepath.Join(root, "second.py")
	states := map[string]FileWriteDiagnosticsState{
		first:  {Status: FileWriteStatusBackendUnavailable, Detail: "checker unavailable"},
		second: {Status: FileWriteStatusBackendUnavailable, Detail: "checker failed"},
	}
	out := m.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{first, second}, true, nil, nil, nil, states, root)
	for _, want := range []string{"first.py: diagnostics unavailable (checker unavailable)", "second.py: diagnostics unavailable (checker failed)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want %q", out, want)
		}
	}
	single := m.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{first}, true, nil, nil, nil, states, root)
	if single != "Applied patch\n\nDiagnostics:\ndiagnostics unavailable (checker unavailable)." {
		t.Fatalf("single-file output = %q, want the unnamed unverified line", single)
	}
}

func TestBatchDiagnosticsPartialResultDoesNotLabelFreshDiagnosticsAsOld(t *testing.T) {
	root := resolvedTempDir(t)
	m := NewManager(&config.Config{}, root, nil)
	first, second := filepath.Join(root, "first.go"), filepath.Join(root, "second.go")
	states := map[string]FileWriteDiagnosticsState{first: {Status: FileWriteStatusPartial, Detail: "samplelsp synchronization failed"}}
	extras := map[string][]Diagnostic{first: {{Severity: 1, Message: "current diagnostic"}}}
	out := m.AppendLSPDiagnosticsToToolOutputForPaths("Applied patch", []string{first, second}, false, map[string][]Diagnostic{}, nil, extras, states, root)
	if !strings.Contains(out, "first.go: diagnostics incomplete") || !strings.Contains(out, "current diagnostic") || strings.Contains(out, "last known") || strings.Contains(out, "new,") {
		t.Fatalf("output = %q", out)
	}
}
