package bytefmt

import "testing"

func TestShort(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "zero", bytes: 0, want: "0 B"},
		{name: "bytes", bytes: 847, want: "847 B"},
		{name: "kb", bytes: 1536, want: "1.5 KB"},
		{name: "mb", bytes: 276285348, want: "263.5 MB"},
		{name: "gb", bytes: 31775732436, want: "29.6 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Short(tt.bytes); got != tt.want {
				t.Fatalf("Short(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestCompact(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "zero", bytes: 0, want: "0 B"},
		{name: "bytes", bytes: 847, want: "847 B"},
		{name: "sub ten kb", bytes: 5 * 1024, want: "5.0 KB"},
		{name: "double digit kb", bytes: 18 * 1024, want: "18 KB"},
		{name: "mb", bytes: 276285348, want: "263 MB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Compact(tt.bytes); got != tt.want {
				t.Fatalf("Compact(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}
