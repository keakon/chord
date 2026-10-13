package inputhistory

import (
	"strings"
	"testing"
)

func TestInputHistoryFuzzyRuneBoundary(t *testing.T) {
	for _, test := range []struct {
		name, body string
		want       int
	}{
		{"ascii_limit", "a" + strings.Repeat("b", 8190) + "c", 1},
		{"ascii_over_limit", "a" + strings.Repeat("b", 8191) + "c", 0},
		{"unicode_limit", "a" + strings.Repeat("界", 8190) + "c", 1},
		{"unicode_over_limit", "a" + strings.Repeat("界", 8191) + "c", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := Snapshot{Entries: []Entry{{Record: historyRecord("sample", test.body)}}}
			result, err := Match(t.Context(), snapshot, Query{Text: "ac", ProjectKey: "project"})
			if err != nil || result.TotalMatched != test.want {
				t.Fatalf("match=%+v err=%v", result, err)
			}
			if test.want > 0 && result.Hits[0].Rank != 2 {
				t.Fatal("fuzzy match lost its rank")
			}
		})
	}
}
