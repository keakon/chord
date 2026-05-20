package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDiagnosticsConfig(t *testing.T) {
	cfg := DefaultConfig()
	if !DiagnosticsEnabled(cfg) {
		t.Fatal("diagnostics should be enabled by default")
	}
	py := cfg.Diagnostics.Python
	if py.SemanticBackend.Type != DiagnosticBackendTypeLSP || py.SemanticBackend.Server != "pyright" {
		t.Fatalf("semantic backend = %+v, want pyright LSP", py.SemanticBackend)
	}
	if py.QuickBackend.Command != "ruff" {
		t.Fatalf("quick command = %q, want ruff", py.QuickBackend.Command)
	}
	if got := py.LargeFile.LineThreshold; got != 5000 {
		t.Fatalf("line threshold = %d, want 5000", got)
	}
	if got := py.LargeFile.ByteThreshold; got != 250000 {
		t.Fatalf("byte threshold = %d, want 250000", got)
	}
	if got := py.Output.MaxTotalDiagnostics; got != 20 {
		t.Fatalf("max total diagnostics = %d, want 20", got)
	}
}

func TestLoadConfigFromPathParsesDiagnosticsOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`diagnostics:
  enabled: false
  python:
    semantic_backend:
      type: lsp
      server: basedpyright
    quick_backend:
      type: command
      command: uvx
      args: [ruff, check, "{file}", --output-format, json]
    large_file:
      line_threshold: 100
      byte_threshold: 2000
      strategy: quick
      run_semantic_when_quick_unavailable: true
    output:
      max_total_diagnostics: 7
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if DiagnosticsEnabled(cfg) {
		t.Fatal("diagnostics should be disabled by override")
	}
	py := cfg.Diagnostics.Python
	if py.SemanticBackend.Server != "basedpyright" {
		t.Fatalf("semantic server = %q", py.SemanticBackend.Server)
	}
	if py.QuickBackend.Command != "uvx" || len(py.QuickBackend.Args) != 5 {
		t.Fatalf("quick backend = %+v", py.QuickBackend)
	}
	if py.LargeFile.LineThreshold != 100 || py.LargeFile.ByteThreshold != 2000 {
		t.Fatalf("large file thresholds = %+v", py.LargeFile)
	}
	if !py.LargeFile.RunSemanticWhenQuickUnavailable {
		t.Fatal("run_semantic_when_quick_unavailable should be true")
	}
	if py.Output.MaxTotalDiagnostics != 7 {
		t.Fatalf("max_total_diagnostics = %d", py.Output.MaxTotalDiagnostics)
	}
}

func TestLoadConfigFromPathRejectsInvalidDiagnosticsConfig(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "semantic backend type",
			yaml: "diagnostics:\n  python:\n    semantic_backend:\n      type: command\n",
			want: "semantic_backend.type",
		},
		{
			name: "quick backend type",
			yaml: "diagnostics:\n  python:\n    quick_backend:\n      type: lsp\n",
			want: "quick_backend.type",
		},
		{
			name: "large strategy",
			yaml: "diagnostics:\n  python:\n    large_file:\n      strategy: semantic\n",
			want: "large_file.strategy",
		},
		{
			name: "negative line threshold",
			yaml: "diagnostics:\n  python:\n    large_file:\n      line_threshold: -1\n",
			want: "line_threshold",
		},
		{
			name: "negative output limit",
			yaml: "diagnostics:\n  python:\n    output:\n      max_total_diagnostics: -1\n",
			want: "max_total_diagnostics",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			_, err := LoadConfigFromPath(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadConfigFromPath err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
