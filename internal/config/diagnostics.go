package config

import (
	"errors"
	"fmt"
	"strings"
)

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

// diagnosticIssue pairs a canonical config path with its human-readable
// problem description.
type diagnosticIssue struct {
	Path    string
	Message string
}

// collectDiagnosticIssues returns every diagnostics-config problem with its
// canonical path, so callers can either render messages (`chord doctor
// config`) or locate and drop exactly the offending override leaves.
func collectDiagnosticIssues(cfg *Config) []diagnosticIssue {
	if cfg == nil {
		return nil
	}
	var issues []diagnosticIssue
	py := cfg.Diagnostics.Python
	issues = append(issues, diagnosticBackendIssues("diagnostics.python.semantic_backend", py.SemanticBackend, DiagnosticBackendTypeLSP)...)
	issues = append(issues, diagnosticBackendIssues("diagnostics.python.quick_backend", py.QuickBackend, DiagnosticBackendTypeCommand)...)
	lf := py.LargeFile
	if lf.LineThreshold < 0 {
		issues = append(issues, diagnosticIssue{Path: "diagnostics.python.large_file.line_threshold", Message: "diagnostics.python.large_file.line_threshold must be >= 0"})
	}
	if lf.ByteThreshold < 0 {
		issues = append(issues, diagnosticIssue{Path: "diagnostics.python.large_file.byte_threshold", Message: "diagnostics.python.large_file.byte_threshold must be >= 0"})
	}
	if lf.Strategy != "" && lf.Strategy != DiagnosticLargeFileStrategyQuick {
		issues = append(issues, diagnosticIssue{Path: "diagnostics.python.large_file.strategy", Message: fmt.Sprintf("diagnostics.python.large_file.strategy must be %q", DiagnosticLargeFileStrategyQuick)})
	}
	out := py.Output
	for _, field := range []struct {
		path  string
		value int
	}{
		{"diagnostics.python.output.max_near_diagnostics", out.MaxNearDiagnostics},
		{"diagnostics.python.output.max_outside_diagnostics", out.MaxOutsideDiagnostics},
		{"diagnostics.python.output.max_total_diagnostics", out.MaxTotalDiagnostics},
		{"diagnostics.python.output.near_range_before_lines", out.NearRangeBeforeLines},
		{"diagnostics.python.output.near_range_after_lines", out.NearRangeAfterLines},
	} {
		if field.value < 0 {
			issues = append(issues, diagnosticIssue{Path: field.path, Message: field.path + " must be >= 0"})
		}
	}
	return issues
}

func diagnosticBackendIssues(path string, backend DiagnosticBackendConfig, allowedType string) []diagnosticIssue {
	if backend.Type != "" && backend.Type != allowedType {
		return []diagnosticIssue{{Path: path + ".type", Message: fmt.Sprintf("%s.type must be %q", path, allowedType)}}
	}
	return nil
}

// collectDiagnosticsConfigIssues returns human-readable descriptions of every
// invalid diagnostics setting.
func collectDiagnosticsConfigIssues(cfg *Config) []string {
	issues := collectDiagnosticIssues(cfg)
	messages := make([]string, len(issues))
	for i, issue := range issues {
		messages[i] = issue.Message
	}
	return messages
}

// resetInvalidDiagnosticsFields clears diagnostics fields that failed semantic
// validation, restoring their unset state so the runtime falls back to the
// built-in defaults instead of riding an invalid value through.
func resetInvalidDiagnosticsFields(d *DiagnosticsConfig) {
	if d == nil {
		return
	}
	py := &d.Python
	if t := strings.TrimSpace(py.SemanticBackend.Type); t != "" && t != DiagnosticBackendTypeLSP {
		py.SemanticBackend.Type = ""
	}
	if t := strings.TrimSpace(py.QuickBackend.Type); t != "" && t != DiagnosticBackendTypeCommand {
		py.QuickBackend.Type = ""
	}
	lf := &py.LargeFile
	if s := strings.TrimSpace(lf.Strategy); s != "" && s != DiagnosticLargeFileStrategyQuick {
		lf.Strategy = ""
	}
	if lf.LineThreshold < 0 {
		lf.LineThreshold = 0
	}
	if lf.ByteThreshold < 0 {
		lf.ByteThreshold = 0
	}
	o := &py.Output
	for _, field := range []*int{
		&o.MaxNearDiagnostics,
		&o.MaxOutsideDiagnostics,
		&o.MaxTotalDiagnostics,
		&o.NearRangeBeforeLines,
		&o.NearRangeAfterLines,
	} {
		if *field < 0 {
			*field = 0
		}
	}
}

func ValidateDiagnosticsConfig(cfg *Config) error {
	issues := collectDiagnosticsConfigIssues(cfg)
	if len(issues) == 0 {
		return nil
	}
	return errors.New(strings.Join(issues, "; "))
}
