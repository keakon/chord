package composer

import (
	"strings"
	"testing"

	"github.com/keakon/bubbles/v2/textarea"
)

func TestSyncHeightGrowsForSoftWrappedSingleLine(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetWidth(20)
	in.SetValue(strings.Repeat("x", 80))
	in.SyncHeight()
	if got := in.textarea.Height(); got < 2 {
		t.Fatalf("textarea.Height() = %d, want >= 2 after soft wrap", got)
	}
}

func TestClampedDisplayLineCountMatchesWrapRows(t *testing.T) {
	in := New(textarea.DefaultDarkStyles(), nil)
	in.SetWidth(10)
	in.SetValue("abcdefghijklmnop")
	// 16 latin letters at width 10 → 2 visual rows minimum
	if got := in.ClampedDisplayLineCount(); got < 2 {
		t.Fatalf("ClampedDisplayLineCount() = %d, want >= 2", got)
	}
}
