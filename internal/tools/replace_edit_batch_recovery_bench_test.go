package tools

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkPlanExactReplacementsRecovery(b *testing.B) {
	b.Run("ManyMissing", func(b *testing.B) {
		var content strings.Builder
		for i := range 1024 {
			fmt.Fprintf(&content, "entry %04d stable content\n", i)
		}
		edits := make([]textReplacement, 50)
		for i := range edits {
			edits[i] = textReplacement{
				OldString: fmt.Sprintf("entry %04d stble content", i),
				NewString: new("updated content"),
			}
		}
		source := content.String()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if _, err := planExactReplacements(source, edits); err == nil {
				b.Fatal("expected missing text to reject the batch")
			}
		}
	})
	b.Run("RepeatedOverlap", func(b *testing.B) {
		source := strings.Repeat("alpha\n", 2000)
		edits := []textReplacement{
			{OldString: "alpha", NewString: new("beta"), ReplaceAll: true},
			{OldString: "alpha", NewString: new("gamma"), ReplaceAll: true},
		}
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if _, err := planExactReplacements(source, edits); err == nil {
				b.Fatal("expected overlaps to reject the batch")
			}
		}
	})
}
