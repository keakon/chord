package inputhistory

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkInputHistoryQueryKinds(b *testing.B) {
	s := Snapshot{cache: &searchCache{maxBytes: 32 << 20}}
	text := strings.Repeat("sample text ", 270)
	for i := range 10000 {
		s.Entries = append(s.Entries, Entry{Record: historyRecord(fmt.Sprint(i), text)})
	}
	_, _ = Match(context.Background(), s, Query{Text: "sample", ProjectKey: "project"})
	for _, q := range []string{"sample", "zzzzzz", "smp txt"} {
		b.Run(q, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Match(context.Background(), s, Query{Text: q, ProjectKey: "project"}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
