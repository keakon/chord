package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkDiagnosticTraceTail(b *testing.B) {
	path := filepath.Join(b.TempDir(), "trace.jsonl")
	content := strings.Repeat("{\"event\":\"request completed\"}\n", 300000)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		tail, ok := readTextTail(path, diagnosticLLMTraceTailBytes, diagnosticLLMTraceTailLines)
		if !ok || len(tail) == 0 {
			b.Fatal("missing trace tail")
		}
	}
}
