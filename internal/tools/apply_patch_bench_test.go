package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkBuildApplyPatchPlanLargeFile(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "large.txt")
	var content strings.Builder
	for i := range 10_000 {
		fmt.Fprintf(&content, "line %05d\n", i)
	}
	if err := os.WriteFile(path, []byte(content.String()), 0644); err != nil {
		b.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: large.txt\n@@\n line 09997\n-line 09998\n+line changed\n line 09999\n*** End Patch"
	b.ReportAllocs()
	b.SetBytes(int64(content.Len()))
	b.ResetTimer()
	for b.Loop() {
		plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
		if err != nil {
			b.Fatal(err)
		}
		if len(plan.Mutations) != 1 {
			b.Fatalf("mutations = %d, want 1", len(plan.Mutations))
		}
	}
}

func BenchmarkBuildApplyPatchPlanMultiFile(b *testing.B) {
	dir := b.TempDir()
	var patch strings.Builder
	patch.WriteString("*** Begin Patch\n")
	for i := range 20 {
		name := fmt.Sprintf("file-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("before\n"), 0644); err != nil {
			b.Fatal(err)
		}
		fmt.Fprintf(&patch, "*** Update File: %s\n@@\n-before\n+after\n", name)
	}
	patch.WriteString("*** End Patch")
	patchText := patch.String()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		plan, err := BuildApplyPatchPlan(context.Background(), patchText, dir)
		if err != nil {
			b.Fatal(err)
		}
		if len(plan.Mutations) != 20 {
			b.Fatalf("mutations = %d, want 20", len(plan.Mutations))
		}
	}
}
