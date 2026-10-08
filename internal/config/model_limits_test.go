package config

import "testing"

func TestModelLimitCompactionBudget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit ModelLimit
		want  int
	}{
		{"independent input", ModelLimit{Context: 400000, Input: 300000, Output: 128000}, 300000},
		{"reserve model output", ModelLimit{Context: 400000, Output: 128000}, 272000},
		{"whole window output", ModelLimit{Context: 400000, Output: 400000}, 400000},
		{"output beyond window", ModelLimit{Context: 400000, Output: 500000}, 400000},
		{"unknown output", ModelLimit{Context: 400000}, 400000},
		{"unknown context", ModelLimit{Output: 128000}, 0},
		{"independent input without context", ModelLimit{Input: 300000}, 300000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.limit.CompactionBudget(); got != tc.want {
				t.Fatalf("CompactionBudget() = %d, want %d", got, tc.want)
			}
		})
	}
}
