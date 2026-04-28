package lsp

import "testing"

func TestFormatDiagLine(t *testing.T) {
	s := formatDiagLine(Diagnostic{Line: 0, Col: 2, Message: "x", Source: "compiler", Severity: 1})
	if s != "[E] 1:3 x" {
		t.Fatalf("unexpected format: %q", s)
	}
}
