package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func BenchmarkGrepWalkRootParallel(b *testing.B) {
	dir := b.TempDir()
	content := "package sample\n// common searchable content\n" + strings.Repeat("var filler = 1\n", 256)
	for i := range 256 {
		path := filepath.Join(dir, fmt.Sprintf("pkg-%03d", i), "sample.go")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	for _, pattern := range []string{"common", "not-present"} {
		b.Run(pattern, func(b *testing.B) {
			re := regexp.MustCompile(pattern)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, _, _, _, err := grepWalkRoot(context.Background(), dir, re, []string{"**/*.go"}, dir, maxGrepMatches, maxGrepOutputBytes); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
