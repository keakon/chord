package filectx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildFilePartsWrapsResolvedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	parts := BuildFileParts([]string{"a.txt"}, func(ref string) string {
		return filepath.Join(root, ref)
	})
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1", len(parts))
	}
	if got := parts[0].Text; !strings.Contains(got, `<file path="a.txt">`) || !strings.Contains(got, "alpha\nbeta") {
		t.Fatalf("wrapped part = %q", got)
	}
}

func TestBuildFilePartsSkipsUnreadableFiles(t *testing.T) {
	root := t.TempDir()
	parts := BuildFileParts([]string{"missing.txt"}, func(ref string) string {
		return filepath.Join(root, ref)
	})
	if len(parts) != 0 {
		t.Fatalf("len(parts) = %d, want 0", len(parts))
	}
}

func TestBuildFilePartsTruncatesLargeFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "big.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("0123456789\n", MaxFileBytes/5)), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	parts := BuildFileParts([]string{"big.txt"}, func(ref string) string {
		return filepath.Join(root, ref)
	})
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d, want 1", len(parts))
	}
	if !strings.Contains(parts[0].Text, "[...truncated, showing first 40 KB only]") {
		t.Fatalf("expected truncation marker, got %q", parts[0].Text)
	}
}

func TestBuildFilePartsWithOptionsHonorsTotalBudget(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		path := filepath.Join(root, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(path, []byte(strings.Repeat("abcdefghij\n", 2048)), 0o644); err != nil {
			t.Fatalf("WriteFile(%d): %v", i, err)
		}
	}

	result := BuildFilePartsWithOptions(
		[]string{"f0.txt", "f1.txt", "f2.txt", "f3.txt", "f4.txt"},
		func(ref string) string { return filepath.Join(root, ref) },
		BuildFilePartsOptions{
			MaxFileBytes:  12 * 1024,
			MaxTotalBytes: 48 * 1024,
		},
	)
	if result.LoadedFiles != 4 {
		t.Fatalf("LoadedFiles = %d, want 4", result.LoadedFiles)
	}
	if result.OmittedFiles != 1 {
		t.Fatalf("OmittedFiles = %d, want 1", result.OmittedFiles)
	}
	if result.TotalBytes > 48*1024 {
		t.Fatalf("TotalBytes = %d, want <= %d", result.TotalBytes, 48*1024)
	}
	if len(result.Parts) != 5 {
		t.Fatalf("len(Parts) = %d, want 5 (4 files + 1 omission note)", len(result.Parts))
	}
	if !strings.Contains(result.Parts[len(result.Parts)-1].Text, "additional files omitted") {
		t.Fatalf("expected omission note, got %q", result.Parts[len(result.Parts)-1].Text)
	}
}
