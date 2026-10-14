package agent

import (
	"testing"
)

func TestChangedPreEditLineRange(t *testing.T) {
	tests := []struct {
		name   string
		before string
		after  string
		start  int
		end    int
		delta  int
	}{
		{name: "replace", before: "a\nb\nc\n", after: "a\nx\nc\n", start: 2, end: 2, delta: 0},
		{name: "delete", before: "a\nb\nc\n", after: "a\nc\n", start: 2, end: 2, delta: -1},
		{name: "insert", before: "a\nc\n", after: "a\nb\nc\n", start: 2, end: 2, delta: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, delta := changedPreEditLineRange(tt.before, tt.after)
			if start != tt.start || end != tt.end || delta != tt.delta {
				t.Fatalf("range = %d-%d delta=%d, want %d-%d delta=%d", start, end, delta, tt.start, tt.end, tt.delta)
			}
		})
	}
}
