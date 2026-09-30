package tools

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkPlanExactReplacementsCRLF(b *testing.B) {
	var content strings.Builder
	var edits []textReplacement
	for i := range 16 {
		key := fmt.Sprintf("section-%02d", i)
		content.WriteString(key)
		content.WriteString("\r\n")
		content.WriteString(strings.Repeat("unchanged content\r\n", 4096))
		edits = append(edits, textReplacement{OldString: key + "\n", NewString: new("updated-" + key + "\n")})
	}
	source := content.String()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := planExactReplacements(source, edits); err != nil {
			b.Fatal(err)
		}
	}
}
