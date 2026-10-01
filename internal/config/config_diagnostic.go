package config

import "fmt"

// DiagnosticSeverity classifies how a config problem affects execution.
type DiagnosticSeverity string

const (
	// DiagnosticSeverityError marks a problem that blocks the action relying
	// on the affected value. Whether an error-level diagnostic blocks startup,
	// a pool switch, or another specific action is decided by the caller that
	// knows the action scope; the config layer never upgrades a single broken
	// reference into a global startup failure on its own.
	DiagnosticSeverityError DiagnosticSeverity = "error"
	// DiagnosticSeverityWarning marks a recoverable problem: loading
	// continues and the offending value falls back per the reported rule.
	DiagnosticSeverityWarning DiagnosticSeverity = "warning"
)

// Diagnostic is one structured config problem. Loader surfaces (startup
// notice, doctor, config show) share it so they never re-run divergent
// validation passes. Free-form issue strings collected by the existing load
// path remain the user-visible log; Diagnostic is the structured sidecar.
type Diagnostic struct {
	Severity DiagnosticSeverity `json:"severity"`
	// File is the config file the problem came from. Empty for problems not
	// tied to a user file.
	File string `json:"file,omitempty"`
	// Path is the dotted YAML path of the offending field, e.g.
	// "providers.sample.models.model-1.thinking". Empty when no single field
	// is at fault.
	Path string `json:"path,omitempty"`
	// Line and Col locate the offending node (1-based, as reported by the
	// YAML parser). Zero when the problem has no single location.
	Line int `json:"line,omitempty"`
	Col  int `json:"col,omitempty"`
	// Message states what is wrong in user-facing wording.
	Message string `json:"message"`
	// Continues reports whether config loading proceeds despite the problem.
	// Action-scope blocking (e.g. refusing to start a pool with broken
	// references) is layered on top by callers.
	Continues bool `json:"continues"`
	// Fallback describes the value actually used instead when the offending
	// value was dropped or replaced. Empty when nothing was substituted.
	Fallback string `json:"fallback,omitempty"`
	// Scope names the subsystem the problem restricts, e.g. the model pool a
	// broken reference belongs to. Empty when the problem is global.
	Scope string `json:"scope,omitempty"`
}

func (d Diagnostic) String() string {
	out := ""
	if d.File != "" {
		out += d.File
		if d.Line > 0 {
			out += fmt.Sprintf(":%d", d.Line)
		}
		out += ": "
	}
	out += d.Message
	return out
}

// appendLoadDiagnostic preserves the tolerant loader's findings for all callers.
func appendLoadDiagnostic(diagnostics *[]Diagnostic, file, path, message, fallback string) {
	if diagnostics == nil {
		return
	}
	*diagnostics = append(*diagnostics, Diagnostic{
		Severity: DiagnosticSeverityWarning,
		File:     file, Path: path, Message: message,
		Continues: true, Fallback: fallback,
	})
}
