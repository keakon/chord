package requesttrim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func reductionContextForArchive(t *testing.T, toolName, content, archiveDir string) Context {
	t.Helper()
	policy := DefaultPolicy()
	return Context{
		ToolName:    toolName,
		Content:     content,
		ToolStatus:  message.ToolStatusSuccess,
		Age:         policy.StaleAgeTurns + 2,
		Policy:      policy,
		ArchiveDir:  archiveDir,
		ToolResults: policy.MinToolResultsPrune + 1,
	}
}

// A one-shot tool output (a background job result) cannot be rebuilt or
// re-fetched, so the generic reduction must archive the full payload with a
// stable address the model can read back, not a marker that loses the content.
func TestArchiveIrreducibleToolOutputJobOutputPreservesFullPayload(t *testing.T) {
	archiveDir := t.TempDir()
	content := strings.Repeat("background job log line\n", 120)
	ctx := reductionContextForArchive(t, tools.NameJobOutput, content, archiveDir)
	reduced, rule, ok := Render(requestReductionGeneric, ctx)
	if !ok || rule != "archived" {
		t.Fatalf("reduction = (%q, %q, %v), want archived", reduced, rule, ok)
	}
	if !strings.Contains(reduced, "[Older job_output output archived at ") || !strings.Contains(reduced, "read it back") {
		t.Fatalf("archived marker missing stable address: %q", reduced)
	}
	// The marker embeds the absolute archive path.
	if !strings.HasPrefix(reduced, "[Older job_output output archived at "+archiveDir) {
		t.Fatalf("archived marker must embed the archive path under %q: %q", archiveDir, reduced)
	}
	before, _, ok := strings.Cut(reduced, "; read it back")
	if !ok {
		t.Fatalf("archived marker missing the read-back hint: %q", reduced)
	}
	path := strings.TrimPrefix(before, "[Older job_output output archived at ")
	rel := strings.TrimPrefix(path, archiveDir+string(filepath.Separator))
	if !strings.HasPrefix(rel, "reduced-artifacts"+string(filepath.Separator)) {
		t.Fatalf("archive path not under reduced-artifacts: %q", rel)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("archived payload unreadable: %v", err)
	}
	if string(data) != content {
		t.Fatalf("archived payload differs: got %d bytes want %d", len(data), len(content))
	}
}

// A rebuildable output (shell) that falls to the generic route must keep its
// ordinary summary; it is not archived.
func TestArchiveIrreducibleToolOutputRebuildableStaysSummarized(t *testing.T) {
	archiveDir := t.TempDir()
	ctx := reductionContextForArchive(t, tools.NameShell, strings.Repeat("log line\n", 60), archiveDir)
	reduced, rule, ok := Render(requestReductionGeneric, ctx)
	if !ok {
		t.Fatalf("expected generic summary, got no reduction")
	}
	if rule == "archived" || strings.Contains(reduced, "archived at") {
		t.Fatalf("rebuildable shell output must not be archived: rule=%q reduced=%q", rule, reduced)
	}
}

// Without an archive dir (e.g. scratch agent without sessionDir) the archive
// path is skipped and the ordinary summary is returned.
func TestArchiveIrreducibleToolOutputNoArchiveDirFallsBack(t *testing.T) {
	ctx := reductionContextForArchive(t, tools.NameJobOutput, strings.Repeat("x\n", 80), "")
	reduced, rule, ok := Render(requestReductionGeneric, ctx)
	if !ok || rule == "archived" {
		t.Fatalf("no-archive-dir job_output reduction = (%q, %q, %v), want non-archived fallback", reduced, rule, ok)
	}
	if strings.Contains(reduced, "archived at") {
		t.Fatalf("no-archive-dir marker must not claim an archive: %q", reduced)
	}
}

// WebFetch summary keeps a content hash so the model can re-fetch and compare
// whether the page changed (B4: re-fetchable but mutating outputs keep
// URL + hash + excerpt).
func TestWebFetchSummaryKeepsContentHash(t *testing.T) {
	policy := DefaultPolicy()
	ctx := Context{
		ToolName:    tools.NameWebFetch,
		Content:     "WEB_FETCH_RESULT\nurl=https://example.invalid/doc\n\npage body\n",
		Meta:        ToolCall{Args: `{"url":"https://example.invalid/doc"}`},
		ToolStatus:  message.ToolStatusSuccess,
		Age:         policy.StaleAgeTurns + 1,
		Policy:      policy,
		ToolResults: policy.MinToolResultsPrune + 1,
	}
	reduced, rule, ok := Render(ReadLike, ctx)
	if !ok || rule != "read_like" {
		t.Fatalf("reduction = (%q, %q, %v), want read_like summary", reduced, rule, ok)
	}
	if !strings.Contains(reduced, "content_fnv1a64=") {
		t.Fatalf("web fetch summary missing content hash: %q", reduced)
	}
	if !strings.Contains(reduced, "https://example.invalid/doc") {
		t.Fatalf("web fetch summary missing url: %q", reduced)
	}
}

// The archive path must be stable across passes: re-reducing the same content
// writes the same file name (no duplicate archives).
func TestArchiveIrreducibleToolOutputStableFileName(t *testing.T) {
	archiveDir := t.TempDir()
	content := strings.Repeat("job output line\n", 90)
	ctx := reductionContextForArchive(t, tools.NameJobOutput, content, archiveDir)
	first, _, ok := Render(requestReductionGeneric, ctx)
	if !ok {
		t.Fatal("first reduction failed")
	}
	second, _, ok := Render(requestReductionGeneric, ctx)
	if !ok {
		t.Fatal("second reduction failed")
	}
	if first != second {
		t.Fatalf("archive marker not stable across passes:\nfirst=%q\nsecond=%q", first, second)
	}
}
