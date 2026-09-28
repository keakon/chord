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
		result, err := buildApplyPatchPlanWithOutcomes(context.Background(), patch, dir)
		if err != nil || result.HasFailures() {
			b.Fatalf("plan failed: %v %#v", err, result.Outcomes)
		}
		if len(result.Plan.Mutations) != 1 {
			b.Fatalf("mutations = %d, want 1", len(result.Plan.Mutations))
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
		result, err := buildApplyPatchPlanWithOutcomes(context.Background(), patchText, dir)
		if err != nil || result.HasFailures() {
			b.Fatalf("plan failed: %v %#v", err, result.Outcomes)
		}
		if len(result.Plan.Mutations) != 20 {
			b.Fatalf("mutations = %d, want 20", len(result.Plan.Mutations))
		}
	}
}

// applyHunksBenchData builds a large file plus ordered single-match hunks
// directly against applyApplyPatchHunks, isolating the line-replacement cost
// from file IO and patch parsing (which the end-to-end benches above cover).
// The first half of the hunks grow the file and the second half shrink it, so
// the running line delta peaks above the zero net delta: an under-sized
// pre-grow shows up as allocations inside the hunk loop. step spaces the hunks;
// a small step clusters them at the head, where every replacement moves the
// longest tail.
func applyHunksBenchData(totalLines, hunkCount, step int) (string, []applyPatchHunk) {
	var content strings.Builder
	content.Grow(totalLines * 12)
	for i := range totalLines {
		fmt.Fprintf(&content, "line %06d\n", i)
	}
	lineAt := func(i int) string { return fmt.Sprintf("line %06d", i) }
	hunks := make([]applyPatchHunk, 0, hunkCount)
	for h := range hunkCount {
		pos := 100 + h*step
		if h < hunkCount/2 {
			hunks = append(hunks, applyPatchHunk{Lines: []applyPatchLine{
				{Kind: ' ', Text: lineAt(pos)},
				{Kind: '-', Text: lineAt(pos + 1)},
				{Kind: '+', Text: lineAt(pos+1) + " changed"},
				{Kind: '+', Text: lineAt(pos+1) + " added"},
			}})
		} else {
			hunks = append(hunks, applyPatchHunk{Lines: []applyPatchLine{
				{Kind: ' ', Text: lineAt(pos)},
				{Kind: '-', Text: lineAt(pos + 1)},
				{Kind: '-', Text: lineAt(pos + 2)},
				{Kind: '+', Text: lineAt(pos+1) + " changed"},
			}})
		}
	}
	return content.String(), hunks
}

func BenchmarkApplyApplyPatchHunksManyHunks(b *testing.B) {
	const totalLines, hunkCount = 100_000, 50
	for _, tc := range []struct {
		name string
		step int
	}{
		{"head", 3},
		{"uniform", (totalLines - 200) / hunkCount},
	} {
		b.Run(tc.name, func(b *testing.B) {
			content, hunks := applyHunksBenchData(totalLines, hunkCount, tc.step)
			b.ReportAllocs()
			b.SetBytes(int64(len(content)))
			for b.Loop() {
				if _, _, _, _, err := applyApplyPatchHunks(context.Background(), content, hunks); err != nil {
					b.Fatalf("apply hunks failed: %v", err)
				}
			}
		})
	}
}
