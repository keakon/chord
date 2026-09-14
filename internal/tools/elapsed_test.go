package tools

import (
	"testing"
	"time"
)

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "sub-second", in: 300 * time.Millisecond, want: "0s"},
		{name: "under a minute", in: 45 * time.Second, want: "45s"},
		{name: "partial second is truncated", in: 1900 * time.Millisecond, want: "1s"},
		{name: "minute boundary", in: time.Minute, want: "1m00s"},
		{name: "minutes and seconds", in: 3*time.Minute + 5*time.Second, want: "3m05s"},
		{name: "hour boundary", in: time.Hour, want: "1h00m00s"},
		{name: "hours keep minutes instead of counting past 60", in: 62*time.Minute + 3*time.Second, want: "1h02m03s"},
		{name: "hours minutes and seconds", in: time.Hour + 2*time.Minute + 5*time.Second, want: "1h02m05s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatElapsed(tt.in); got != tt.want {
				t.Fatalf("FormatElapsed(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
