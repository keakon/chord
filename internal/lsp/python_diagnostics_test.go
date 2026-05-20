package lsp

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestSelectPythonDiagnosticBackend(t *testing.T) {
	cfg := config.DefaultDiagnosticsConfig().Python
	small := fileMetrics{Lines: 10, Bytes: 100}
	large := fileMetrics{Lines: 6000, Bytes: 100}

	cases := []struct {
		name          string
		metrics       fileMetrics
		availability  diagnosticBackendAvailability
		forceSemantic bool
		want          pythonDiagnosticBackend
		wantReason    string
	}{
		{"both small", small, diagnosticBackendAvailability{Semantic: true, Quick: true}, false, pythonDiagnosticBackendSemantic, "semantic"},
		{"both large", large, diagnosticBackendAvailability{Semantic: true, Quick: true}, false, pythonDiagnosticBackendQuick, "large-file"},
		{"semantic only small", small, diagnosticBackendAvailability{Semantic: true}, false, pythonDiagnosticBackendSemantic, "semantic"},
		{"semantic only large skipped", large, diagnosticBackendAvailability{Semantic: true}, false, pythonDiagnosticBackendNone, "large-file-quick-unavailable"},
		{"semantic only large forced", large, diagnosticBackendAvailability{Semantic: true}, true, pythonDiagnosticBackendSemantic, "large-file-forced-semantic"},
		{"quick only", small, diagnosticBackendAvailability{Quick: true}, false, pythonDiagnosticBackendQuick, "semantic-unavailable"},
		{"none", small, diagnosticBackendAvailability{}, false, pythonDiagnosticBackendNone, "no-backend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg.LargeFile.RunSemanticWhenQuickUnavailable = tc.forceSemantic
			got := selectPythonDiagnosticBackend(cfg, tc.metrics, tc.availability)
			if got.Backend != tc.want || got.Reason != tc.wantReason {
				t.Fatalf("selection = %+v, want backend=%s reason=%s", got, tc.want, tc.wantReason)
			}
		})
	}
}

func TestParseRuffDiagnostics(t *testing.T) {
	out := []byte(`[{"code":"F821","message":"Undefined name ` + "`missing`" + `","location":{"row":3,"column":5}}]`)
	diags, err := parseRuffDiagnostics(out)
	if err != nil {
		t.Fatalf("parseRuffDiagnostics: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1", len(diags))
	}
	got := diags[0]
	if got.Severity != 1 || got.Line != 2 || got.Col != 4 || got.Source != "ruff" || got.Code != "F821" || got.Message != "Undefined name `missing`" {
		t.Fatalf("diag = %+v", got)
	}
}

func TestParseRuffDiagnosticsMapsBugBearToWarning(t *testing.T) {
	out := []byte(`[{"code":"B006","message":"Do not use mutable defaults","location":{"row":4,"column":2}}]`)
	diags, err := parseRuffDiagnostics(out)
	if err != nil {
		t.Fatalf("parseRuffDiagnostics: %v", err)
	}
	if len(diags) != 1 || diags[0].Severity != 2 || diags[0].Code != "B006" {
		t.Fatalf("diags = %+v, want B006 warning", diags)
	}
}

func TestRunRuffDiagnosticsAcceptsExitErrorWithJSON(t *testing.T) {
	origRun := runCommandContext
	t.Cleanup(func() { runCommandContext = origRun })
	var gotName string
	var gotArgs []string
	runCommandContext = func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		exitErr := exec.Command("sh", "-c", "exit 1").Run()
		return []byte(`[{"code":"E999","message":"SyntaxError","location":{"row":1,"column":1}}]`), exitErr
	}
	cfg := config.DefaultDiagnosticsConfig().Python
	cfg.QuickBackend.Command = "ruff-test"
	diags, err := runRuffDiagnostics(context.Background(), "/tmp/x.py", cfg)
	if err != nil {
		t.Fatalf("runRuffDiagnostics: %v", err)
	}
	if len(diags) != 1 || diags[0].Code != "E999" {
		t.Fatalf("diags = %+v, want parsed E999 diagnostic", diags)
	}
	if gotName != "ruff-test" || len(gotArgs) == 0 || gotArgs[1] != "/tmp/x.py" {
		t.Fatalf("command = %q args=%v", gotName, gotArgs)
	}
}

func TestRunRuffDiagnosticsRejectsGenericCommandError(t *testing.T) {
	origRun := runCommandContext
	t.Cleanup(func() { runCommandContext = origRun })
	var gotName string
	var gotArgs []string
	runCommandContext = func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return []byte(`[{"code":"E999","message":"SyntaxError","location":{"row":1,"column":1}}]`), errors.New("exit status 1")
	}
	cfg := config.DefaultDiagnosticsConfig().Python
	cfg.QuickBackend.Command = "ruff-test"
	diags, err := runRuffDiagnostics(context.Background(), "/tmp/x.py", cfg)
	if err == nil {
		t.Fatalf("expected non-exit error from generic error")
	}
	if len(diags) != 0 {
		t.Fatalf("diags = %+v, want none on generic error", diags)
	}
	if gotName != "ruff-test" || len(gotArgs) == 0 || gotArgs[1] != "/tmp/x.py" {
		t.Fatalf("command = %q args=%v", gotName, gotArgs)
	}
}

func TestRunRuffDiagnosticsRejectsNonJSONOutput(t *testing.T) {
	origRun := runCommandContext
	t.Cleanup(func() { runCommandContext = origRun })
	runCommandContext = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		exitErr := exec.Command("sh", "-c", "exit 1").Run()
		return []byte("not json"), exitErr
	}
	_, err := runRuffDiagnostics(context.Background(), "/tmp/x.py", config.DefaultDiagnosticsConfig().Python)
	if err == nil || !strings.Contains(err.Error(), "parse Ruff JSON diagnostics") {
		t.Fatalf("err = %v, want parse error", err)
	}
}

func TestRunRuffDiagnosticsReturnsContextError(t *testing.T) {
	origRun := runCommandContext
	t.Cleanup(func() { runCommandContext = origRun })
	runCommandContext = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runRuffDiagnostics(ctx, "/tmp/x.py", config.DefaultDiagnosticsConfig().Python)
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestIsPythonPathIncludesStubs(t *testing.T) {
	if !isPythonPath("x.py") || !isPythonPath("x.PYI") {
		t.Fatal("expected .py and .pyi to be Python paths")
	}
	if isPythonPath("x.txt") {
		t.Fatal("did not expect .txt to be a Python path")
	}
}

func TestPythonQuickBackendAvailableResolvesEveryCall(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	calls := 0
	lookPath = func(string) (string, error) {
		calls++
		return "/bin/ruff", nil
	}
	mgr := NewManager(&config.Config{}, t.TempDir(), nil)
	cfg := config.DefaultDiagnosticsConfig().Python
	if !mgr.pythonQuickBackendAvailable(cfg) {
		t.Fatal("expected quick backend available on first check")
	}
	if !mgr.pythonQuickBackendAvailable(cfg) {
		t.Fatal("expected quick backend available on second check")
	}
	// No cache: each call must re-resolve so a binary installed (or removed)
	// mid-session is reflected without restarting Chord.
	if calls != 2 {
		t.Fatalf("lookPath calls = %d, want 2 (no permanent cache)", calls)
	}
}

func TestPythonQuickBackendAvailablePicksUpLateInstall(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	available := false
	lookPath = func(string) (string, error) {
		if available {
			return "/bin/ruff", nil
		}
		return "", exec.ErrNotFound
	}
	mgr := NewManager(&config.Config{}, t.TempDir(), nil)
	cfg := config.DefaultDiagnosticsConfig().Python
	if mgr.pythonQuickBackendAvailable(cfg) {
		t.Fatal("expected quick backend unavailable before install")
	}
	available = true
	if !mgr.pythonQuickBackendAvailable(cfg) {
		t.Fatal("expected quick backend available after install without cache invalidation")
	}
}
