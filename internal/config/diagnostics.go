package config

import "fmt"

const (
	DiagnosticBackendTypeLSP     = "lsp"
	DiagnosticBackendTypeCommand = "command"

	DiagnosticLargeFileStrategyQuick = "quick"
)

// DiagnosticsConfig controls diagnostics appended after mutating tool calls.
type DiagnosticsConfig struct {
	Enabled *bool                   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Python  PythonDiagnosticsConfig `json:"python" yaml:"python"`
}

// PythonDiagnosticsConfig controls Python-specific post-tool diagnostics.
type PythonDiagnosticsConfig struct {
	SemanticBackend DiagnosticBackendConfig          `json:"semantic_backend" yaml:"semantic_backend"`
	QuickBackend    DiagnosticBackendConfig          `json:"quick_backend" yaml:"quick_backend"`
	LargeFile       PythonLargeFileDiagnosticsConfig `json:"large_file" yaml:"large_file"`
	Output          DiagnosticOutputConfig           `json:"output" yaml:"output"`
}

// DiagnosticBackendConfig declares either an LSP backend or a one-shot command backend.
type DiagnosticBackendConfig struct {
	Type    string   `json:"type,omitempty" yaml:"type,omitempty"`
	Server  string   `json:"server,omitempty" yaml:"server,omitempty"`
	Command string   `json:"command,omitempty" yaml:"command,omitempty"`
	Args    []string `json:"args,omitempty" yaml:"args,omitempty"`
}

// PythonLargeFileDiagnosticsConfig controls when Python diagnostics use a quick backend.
type PythonLargeFileDiagnosticsConfig struct {
	LineThreshold                   int    `json:"line_threshold,omitempty" yaml:"line_threshold,omitempty"`
	ByteThreshold                   int    `json:"byte_threshold,omitempty" yaml:"byte_threshold,omitempty"`
	Strategy                        string `json:"strategy,omitempty" yaml:"strategy,omitempty"`
	RunSemanticWhenQuickUnavailable bool   `json:"run_semantic_when_quick_unavailable,omitempty" yaml:"run_semantic_when_quick_unavailable,omitempty"`
}

// DiagnosticOutputConfig limits diagnostic text appended to tool results.
type DiagnosticOutputConfig struct {
	MaxNearDiagnostics    int `json:"max_near_diagnostics,omitempty" yaml:"max_near_diagnostics,omitempty"`
	MaxOutsideDiagnostics int `json:"max_outside_diagnostics,omitempty" yaml:"max_outside_diagnostics,omitempty"`
	MaxTotalDiagnostics   int `json:"max_total_diagnostics,omitempty" yaml:"max_total_diagnostics,omitempty"`
	NearRangeBeforeLines  int `json:"near_range_before_lines,omitempty" yaml:"near_range_before_lines,omitempty"`
	NearRangeAfterLines   int `json:"near_range_after_lines,omitempty" yaml:"near_range_after_lines,omitempty"`
}

func DefaultDiagnosticsConfig() DiagnosticsConfig {
	enabled := true
	return DiagnosticsConfig{
		Enabled: &enabled,
		Python: PythonDiagnosticsConfig{
			SemanticBackend: DiagnosticBackendConfig{Type: DiagnosticBackendTypeLSP, Server: "pyright"},
			QuickBackend: DiagnosticBackendConfig{
				Type:    DiagnosticBackendTypeCommand,
				Command: "ruff",
				Args:    []string{"check", "{file}", "--select", "E9,F821,F822,F823,B,PLE", "--output-format", "json"},
			},
			LargeFile: PythonLargeFileDiagnosticsConfig{
				LineThreshold: 5000,
				ByteThreshold: 250000,
				Strategy:      DiagnosticLargeFileStrategyQuick,
			},
			Output: DiagnosticOutputConfig{
				MaxNearDiagnostics:    10,
				MaxOutsideDiagnostics: 5,
				MaxTotalDiagnostics:   10,
				NearRangeBeforeLines:  20,
				NearRangeAfterLines:   80,
			},
		},
	}
}

func DiagnosticsEnabled(cfg *Config) bool {
	if cfg == nil || cfg.Diagnostics.Enabled == nil {
		return true
	}
	return *cfg.Diagnostics.Enabled
}

func ValidateDiagnosticsConfig(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	py := cfg.Diagnostics.Python
	if err := validateDiagnosticBackend("diagnostics.python.semantic_backend", py.SemanticBackend, DiagnosticBackendTypeLSP); err != nil {
		return err
	}
	if err := validateDiagnosticBackend("diagnostics.python.quick_backend", py.QuickBackend, DiagnosticBackendTypeCommand); err != nil {
		return err
	}
	lf := py.LargeFile
	if lf.LineThreshold < 0 {
		return fmt.Errorf("diagnostics.python.large_file.line_threshold must be >= 0")
	}
	if lf.ByteThreshold < 0 {
		return fmt.Errorf("diagnostics.python.large_file.byte_threshold must be >= 0")
	}
	if lf.Strategy != "" && lf.Strategy != DiagnosticLargeFileStrategyQuick {
		return fmt.Errorf("diagnostics.python.large_file.strategy must be %q", DiagnosticLargeFileStrategyQuick)
	}
	out := py.Output
	if out.MaxNearDiagnostics < 0 {
		return fmt.Errorf("diagnostics.python.output.max_near_diagnostics must be >= 0")
	}
	if out.MaxOutsideDiagnostics < 0 {
		return fmt.Errorf("diagnostics.python.output.max_outside_diagnostics must be >= 0")
	}
	if out.MaxTotalDiagnostics < 0 {
		return fmt.Errorf("diagnostics.python.output.max_total_diagnostics must be >= 0")
	}
	if out.NearRangeBeforeLines < 0 {
		return fmt.Errorf("diagnostics.python.output.near_range_before_lines must be >= 0")
	}
	if out.NearRangeAfterLines < 0 {
		return fmt.Errorf("diagnostics.python.output.near_range_after_lines must be >= 0")
	}
	return nil
}

func validateDiagnosticBackend(path string, backend DiagnosticBackendConfig, allowedType string) error {
	if backend.Type != "" && backend.Type != allowedType {
		return fmt.Errorf("%s.type must be %q", path, allowedType)
	}
	return nil
}
