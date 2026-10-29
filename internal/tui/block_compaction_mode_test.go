package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

// TestCompactionSummaryCardNamesTheMode pins that the card says how the
// archived history was preserved. All four modes rendered an identical
// "CONTEXT SUMMARY" card, so a truncate-only checkpoint - which carries no
// summary at all - was indistinguishable from a model-driven one.
func TestCompactionSummaryCardNamesTheMode(t *testing.T) {
	ApplyTheme(uitheme.Default())
	cases := []struct {
		mode   string
		manual bool
		want   string
	}{
		{message.CompactionSummaryModeModelDriven, false, "CONTEXT SUMMARY #2 · MODEL-DRIVEN"},
		{message.CompactionSummaryModeModelDriven, true, "CONTEXT SUMMARY #2 · MODEL-DRIVEN"},
		{message.CompactionSummaryModeModelSummary, false, "CONTEXT SUMMARY #2 · AUTO"},
		{message.CompactionSummaryModeModelSummary, true, "CONTEXT SUMMARY #2 · MANUAL"},
		{message.CompactionSummaryModeStructuredFallback, false, "CONTEXT SUMMARY #2 · FALLBACK"},
		{message.CompactionSummaryModeTruncateOnly, false, "CONTEXT SUMMARY #2 · TRUNCATED"},
	}
	for _, c := range cases {
		block := &Block{
			ID:                    1,
			Type:                  BlockCompactionSummary,
			Content:               "[Context Summary]\nEarlier work was compacted.",
			CompactionSummaryRaw:  "[Context Summary]\nEarlier work was compacted.",
			CompactionSummaryMode: c.mode,
			CompactionManual:      c.manual,
			MsgIndex:              -1,
		}
		plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
		if !strings.Contains(plain, c.want) {
			t.Errorf("mode %q: expected label %q, got:\n%s", c.mode, c.want, plain)
		}
	}
}

// TestCompactionSummaryDegradedModeKeepsOneBadge pins the header surface for
// degraded modes. The fallback/truncate modes used to render their suffix past
// the badge edge, which left the separator flush against the label (its only
// gap was the badge's trailing padding) and dropped the badge background
// behind "· FALLBACK". Every mode now carries one badge, so the separator
// keeps the same inset as the plain modes, and degraded modes only swap the
// badge surface for the warning colour.
func TestCompactionSummaryDegradedModeKeepsOneBadge(t *testing.T) {
	ApplyTheme(uitheme.Default())
	cases := []struct {
		mode     string
		wantText string
		wantBg   interface{ RGBA() (r, g, b, a uint32) }
	}{
		{message.CompactionSummaryModeModelSummary, "CONTEXT SUMMARY #2 · AUTO", colorOfTheme(currentStyles.Palette.ThinkingLabelBg)},
		{message.CompactionSummaryModeStructuredFallback, "CONTEXT SUMMARY #2 · FALLBACK", colorOfTheme(currentStyles.Palette.InfoPanelDiagWarnFg)},
		{message.CompactionSummaryModeTruncateOnly, "CONTEXT SUMMARY #2 · TRUNCATED", colorOfTheme(currentStyles.Palette.InfoPanelDiagWarnFg)},
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
		header := ""
		for _, line := range block.Render(100, "") {
			if strings.Contains(terminaltext.StripANSI(line), "CONTEXT SUMMARY") {
				header = line
				break
			}
		}
		if header == "" {
			t.Fatalf("mode %q: header line not found", c.mode)
		}
		assertRenderedTextBackground(t, header, c.wantText, c.wantBg)
		assertRenderedTextBackground(t, header, "·", c.wantBg)
	}
}

// TestCompactionSummaryCardOmitsUnknownMode pins the other direction: a
// checkpoint whose mode was never recorded and can no longer be recovered
// claims nothing instead of guessing a mode.
func TestCompactionSummaryCardOmitsUnknownMode(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                   1,
		Type:                 BlockCompactionSummary,
		Content:              "[Context Summary]\nEarlier work was compacted.",
		CompactionSummaryRaw: "[Context Summary]\nEarlier work was compacted.",
		MsgIndex:             -1,
	}
	plain := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "CONTEXT SUMMARY #2") {
		t.Fatalf("expected the plain label, got:\n%s", plain)
	}
	if strings.Contains(plain, " · ") {
		t.Fatalf("expected no mode suffix for an unrecorded mode, got:\n%s", plain)
	}
}
