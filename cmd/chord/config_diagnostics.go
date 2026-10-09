package main

import "github.com/keakon/chord/internal/config"

// hasConfigErrors reports whether any diagnostic blocks the action using the
// affected model or pool.
func hasConfigErrors(diagnostics []config.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == config.DiagnosticSeverityError {
			return true
		}
	}
	return false
}
