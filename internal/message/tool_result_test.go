package message

import "testing"

func TestToolResultElidedMarkerRoundTrip(t *testing.T) {
	for _, size := range []int{0, 1, 1219, 1 << 20} {
		marker := FormatToolResultElided(size)
		got, ok := ToolResultElidedBytes(marker)
		if !ok || got != size {
			t.Fatalf("ToolResultElidedBytes(%q) = (%d, %v), want (%d, true)", marker, got, ok, size)
		}
		if got, ok := ToolResultElidedBytes("  " + marker + "\n"); !ok || got != size {
			t.Fatalf("padded marker %q = (%d, %v), want (%d, true)", marker, got, ok, size)
		}
	}
}

func TestToolResultElidedBytesRejectsOrdinaryOutput(t *testing.T) {
	for _, content := range []string{
		"",
		"ok",
		"preamble [result elided by checkpoint: 12 bytes]",
		"[result elided by checkpoint: 12 bytes] plus trailing prose",
		"[result elided by checkpoint: twelve bytes]",
		"[result elided by checkpoint: -1 bytes]",
		"[result elided by checkpoint: 12]",
	} {
		if size, ok := ToolResultElidedBytes(content); ok {
			t.Fatalf("ToolResultElidedBytes(%q) = (%d, true), want false", content, size)
		}
	}
}

func TestToolResultSucceeded(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", true},
		{"whitespace", " \n\t ", true},
		{"success", "ok", true},
		{"cancelled", "cancelled", false},
		{"cancelled with details", "cancelled\nuser stopped", false},
		{"error prefix", "Error: failed", false},
		{"embedded error block", "before\n\nError: failed", false},
		{"model stopped", "Model stopped before completing this tool call", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToolResultSucceeded(tt.content); got != tt.want {
				t.Fatalf("ToolResultSucceeded(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}
