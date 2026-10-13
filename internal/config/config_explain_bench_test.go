package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkLoadResolvedConfigExplanation(b *testing.B) {
	for _, count := range []int{10, 100} {
		b.Run(fmt.Sprintf("models_%d", count), func(b *testing.B) {
			var data strings.Builder
			data.WriteString("providers:\n  sample:\n    type: responses\n    models:\n")
			for i := range count {
				fmt.Fprintf(&data, "      model-%d:\n        reasoning: {effort: high}\n        limit: {input: 100, output: 20}\n", i)
			}
			path := filepath.Join(b.TempDir(), "global.yaml")
			if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				rc, err := LoadResolvedConfig(path, "")
				if err != nil {
					b.Fatal(err)
				}
				if _, err := ExplainModelField(rc, "sample", "model-0", ExplainLimitContext); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
