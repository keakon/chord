package tui

import "testing"

// The "#" marker is what keeps an ad-hoc handle from being read as a plan
// task's own numbering: a plan reference is a bare number and must stay one.
func TestExtractReadableTarget(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"ad-hoc handle", "adhoc-8", "#8"},
		{"ad-hoc handle with empty suffix", "adhoc-", ""},
		{"plan task reference stays bare", "3", "3"},
		{"unrecognized form unchanged", "task-foo", "task-foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractReadableTarget(tt.in); got != tt.want {
				t.Fatalf("extractReadableTarget(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
