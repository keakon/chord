package inputhistory

import (
	"context"
	"strings"
	"testing"
)

type historyCancelAfterChecks struct {
	context.Context
	remaining int
}

func (c *historyCancelAfterChecks) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestInputHistorySearchChunkBoundaryAndCancellation(t *testing.T) {
	text := strings.Repeat("a", searchScanBytes-2) + "needle" + strings.Repeat("b", searchScanBytes*4)
	if !containsHistoryText(context.Background(), text, "needle") {
		t.Fatal("lost cross-chunk match")
	}
	ctx := &historyCancelAfterChecks{Context: context.Background(), remaining: 3}
	if containsHistoryText(ctx, text, "missing") || ctx.remaining != 0 {
		t.Fatal("scan failed to check cancellation between chunks")
	}
	if historyRuneLimit(context.Background(), strings.Repeat("a", 8193), 8192) {
		t.Fatal("fuzzy candidate limit exceeded")
	}
	snapshot := Snapshot{Entries: []Entry{{Record: historyRecord("one", text)}}}
	ctx = &historyCancelAfterChecks{Context: context.Background(), remaining: 3}
	if _, err := Match(ctx, snapshot, Query{Text: "missing", ProjectKey: "project"}); err != context.Canceled {
		t.Fatal("cancelled match reported success", err)
	}
}
func TestInputHistoryQueryNormalization(t *testing.T) {
	query := strings.Repeat("word ", 16) + "ignored"
	normalized := NormalizeQuery(query)
	if len(strings.Fields(normalized)) != 16 {
		t.Fatal(normalized)
	}
	snap := Snapshot{Entries: []Entry{{Record: historyRecord("one", normalized)}}}
	result, err := Match(context.Background(), snap, Query{Text: query, ProjectKey: "project"})
	if err != nil || len(result.Hits) != 1 || result.Hits[0].Rank != 0 {
		t.Fatal("query ranks used different normalized inputs", result, err)
	}
}
