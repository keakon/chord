package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// TestCompactionSummaryCardNamesTheMode pins that the card says how the
// archived history was preserved. All four modes rendered an identical
// "CONTEXT SUMMARY" card, so a truncate-only checkpoint - which carries no
// summary at all - was indistinguishable from a model-driven one.
func TestCompactionSummaryCardNamesTheMode(t *testing.T) {
	ApplyTheme(DefaultTheme())
	cases := []struct {
		mode string
		want string
	}{
		{message.CompactionSummaryModeModelDriven, "CONTEXT SUMMARY #2 · MODEL-DRIVEN"},
		{message.CompactionSummaryModeModelSummary, "CONTEXT SUMMARY #2 · AUTO"},
		{message.CompactionSummaryModeStructuredFallback, "CONTEXT SUMMARY #2 · FALLBACK"},
		{message.CompactionSummaryModeTruncateOnly, "CONTEXT SUMMARY #2 · TRUNCATED"},
	}
	for _, c := range cases {
		block := &Block{
			ID:                    1,
			Type:                  BlockCompactionSummary,
			Content:               "[Context Summary]\nEarlier work was compacted.",
			CompactionSummaryRaw:  "[Context Summary]\nEarlier work was compacted.",
			CompactionSummaryMode: c.mode,
			MsgIndex:              -1,
		}
		plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
		if !strings.Contains(plain, c.want) {
			t.Errorf("mode %q: expected label %q, got:\n%s", c.mode, c.want, plain)
		}
	}
}

// TestCompactionSummaryCardOmitsUnknownMode pins the other direction: a
// checkpoint whose mode was never recorded and can no longer be recovered
// claims nothing instead of guessing a mode.
func TestCompactionSummaryCardOmitsUnknownMode(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                   1,
		Type:                 BlockCompactionSummary,
		Content:              "[Context Summary]\nEarlier work was compacted.",
		CompactionSummaryRaw: "[Context Summary]\nEarlier work was compacted.",
		MsgIndex:             -1,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "CONTEXT SUMMARY #2") {
		t.Fatalf("expected the plain label, got:\n%s", plain)
	}
	if strings.Contains(plain, " · ") {
		t.Fatalf("expected no mode suffix for an unrecorded mode, got:\n%s", plain)
	}
}
