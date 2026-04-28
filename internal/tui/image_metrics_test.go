package tui

import "testing"

func TestReadKittyTerminalMetricsIgnoresNonKittyBackend(t *testing.T) {
	got := readKittyTerminalMetrics(ImageBackendITerm2)
	if got.Valid {
		t.Fatalf("readKittyTerminalMetrics(non-kitty) = %#v, want invalid metrics", got)
	}
}
