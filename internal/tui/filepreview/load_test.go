package filepreview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/filedelivery"
	"github.com/keakon/chord/internal/tui/syntax"
)

func TestFileViewerSourceSafetyAndRawCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	raw := "\tfirst\n\x1b]52;c;attack\aSECOND\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	loc, _ := filedelivery.WorkspaceLocator(dir, path)
	file, err := filedelivery.Inspect(context.Background(), loc, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := (*Cache)(nil).Load(context.Background(), Request{File: file, Width: 80, Highlighter: syntax.New(path, "", "")})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if string(result.Raw) != raw {
		t.Fatal("original bytes changed")
	}
	if strings.Contains(strings.Join(result.Plain, "\n"), "attack") || strings.Contains(strings.Join(result.Plain, "\n"), "\x1b") {
		t.Fatal("terminal controls leaked")
	}
}
func TestFileViewerMarkdownBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	raw := strings.Repeat("# heading\n", MarkdownLimit/5)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	loc, _ := filedelivery.WorkspaceLocator(dir, path)
	file, err := filedelivery.Inspect(context.Background(), loc, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := (*Cache)(nil).Load(context.Background(), Request{File: file, Width: 80, Markdown: true})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "source") {
		t.Fatal("oversized Markdown rendered")
	}
	if len(result.Raw) > fileViewerReadLimit {
		t.Fatal("read budget exceeded")
	}
}
func TestFileViewerCacheDoesNotRetainDeclarationWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(path, []byte("current\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loc, _ := filedelivery.WorkspaceLocator(dir, path)
	file, err := filedelivery.Inspect(context.Background(), loc, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	cache := &Cache{}
	req := Request{File: file, Width: 80, Highlighter: syntax.New(path, "", "")}
	req.File.SHA256 = "old"
	first := cache.Load(context.Background(), req)
	if first.Err != nil || !strings.Contains(first.Note, "changed") {
		t.Fatalf("missing change warning: %+v", first)
	}
	req.File = file
	second := cache.Load(context.Background(), req)
	if second.Err != nil || strings.Contains(second.Note, "changed") {
		t.Fatalf("stale declaration warning: %+v", second)
	}
}

func TestFileViewerCacheBudgetAndKeys(t *testing.T) {
	c := &Cache{}
	for i := range 20 {
		key := string(rune('a' + i))
		c.put(key, Result{Raw: make([]byte, 1<<20)})
	}
	if c.bytes > fileViewerCacheBytes || len(c.entries) >= 20 {
		t.Fatal("cache budget not enforced")
	}
	if _, ok := c.get("a"); ok {
		t.Fatal("oldest item not evicted")
	}
}
func TestFileViewerRemovesGeneratedHyperlinks(t *testing.T) {
	got := stripFileHyperlinks("a\x1b]8;;file:///execute\x1b\\label\x1b]8;;\x1b\\b")
	if got != "alabelb" {
		t.Fatalf("OSC hyperlink retained: %q", got)
	}
}

func TestFileViewerSourceRenderBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(strings.Repeat("var x = 1\n", 15000)), 0600); err != nil {
		t.Fatal(err)
	}
	loc, err := filedelivery.WorkspaceLocator(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := filedelivery.Inspect(context.Background(), loc, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := (*Cache)(nil).Load(context.Background(), Request{File: f, Width: 80, Highlighter: syntax.New(path, "", "")})
	if r.Err != nil || len(r.Lines) > fileViewerRenderLines || !strings.Contains(r.Note, "truncated") {
		t.Fatalf("lines=%d note=%q err=%v", len(r.Lines), r.Note, r.Err)
	}
}
