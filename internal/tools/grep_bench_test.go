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

	cases := []struct {
		name         string
		pattern      string
		contextLines int
	}{
		{name: "common", pattern: "common"},
		{name: "not-present", pattern: "not-present"},
		// Every file has one hit, so the context window fills the output
		// budget early and the remaining files stream through bare matches.
		{name: "common-context-3", pattern: "common", contextLines: 3},
		// No hit: measures what buffering leading context costs per line.
		{name: "not-present-context-3", pattern: "not-present", contextLines: 3},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			re := regexp.MustCompile(tc.pattern)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := grepWalkRoot(context.Background(), dir, re, []string{"**/*.go"}, dir, maxGrepMatches, maxGrepOutputBytes, tc.contextLines, ""); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
