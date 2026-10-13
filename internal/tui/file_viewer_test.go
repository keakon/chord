package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/filedelivery"
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
	result := loadFileViewer(fileViewerRequest{ctx: context.Background(), file: file, width: 80, highlighter: newCodeHighlighterWithLanguage(path, "", "")})
	if result.err != nil {
		t.Fatal(result.err)
	}
	if string(result.raw) != raw {
		t.Fatal("original bytes changed")
	}
	if strings.Contains(strings.Join(result.plain, "\n"), "attack") || strings.Contains(strings.Join(result.plain, "\n"), "\x1b") {
		t.Fatal("terminal controls leaked")
	}
}
func TestFileViewerMarkdownBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	raw := strings.Repeat("# heading\n", fileViewerMarkdownLimit/5)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	loc, _ := filedelivery.WorkspaceLocator(dir, path)
	file, err := filedelivery.Inspect(context.Background(), loc, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	result := loadFileViewer(fileViewerRequest{ctx: context.Background(), file: file, width: 80, markdown: true, style: contentMarkdownStyleConfig()})
	if result.err == nil || !strings.Contains(result.err.Error(), "source") {
		t.Fatal("oversized Markdown rendered")
	}
	if len(result.raw) > fileViewerReadLimit {
		t.Fatal("read budget exceeded")
	}
}
func TestFileViewerDiscardsOldGeneration(t *testing.T) {
	m := Model{}
	m.fileViewer = fileViewerState{open: true, generation: 2, owner: m.imageViewerOwner()}
	m.handleFileViewerLoaded(fileViewerLoadedMsg{request: fileViewerRequest{generation: 1, owner: m.imageViewerOwner()}, raw: []byte("old"), lines: []string{"old"}})
	if len(m.fileViewer.raw) > 0 || len(m.fileViewer.lines) > 0 {
		t.Fatal("stale generation installed")
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
	req := fileViewerRequest{ctx: context.Background(), cache: &fileViewerCache{}, file: file, width: 80, highlighter: newCodeHighlighterWithLanguage(path, "", "")}
	req.file.SHA256 = "old"
	first := loadFileViewer(req)
	if first.err != nil || !strings.Contains(first.note, "changed") {
		t.Fatalf("missing change warning: %+v", first)
	}
	req.file = file
	second := loadFileViewer(req)
	if second.err != nil || strings.Contains(second.note, "changed") {
		t.Fatalf("stale declaration warning: %+v", second)
	}
}

func TestFileViewerLinkRequestsShareLatestWorkerSlot(t *testing.T) {
	m := Model{fileViewerBusy: true}
	if cmd := m.openLocalFileLinks([]localFileLink{{path: "/first"}}); cmd != nil {
		t.Fatal("started concurrent worker")
	}
	if cmd := m.openLocalFileLinks([]localFileLink{{path: "/latest"}}); cmd != nil {
		t.Fatal("started concurrent worker")
	}
	if m.fileViewerNext == nil || m.fileViewerNext.links[0].path != "/latest" {
		t.Fatal("waiting slot did not retain latest request")
	}
}

func TestFileViewerMenuKeepsSelectionVisible(t *testing.T) {
	for _, rows := range []int{1, 2} {
		start := fileMenuStart(15, 7, rows)
		if start > 15 || (15-start)*rows+1 >= 7 {
			t.Fatal("selection outside viewport")
		}
	}
	links := localFileLinks("![local](images/plot.png) ![remote](https://example.org/x.png)", "/report")
	if len(links) != 1 || links[0].path != "/report/images/plot.png" {
		t.Fatalf("image references: %#v", links)
	}
}
