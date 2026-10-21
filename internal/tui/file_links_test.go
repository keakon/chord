package tui

import (
	"strings"
	"testing"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestLocalFileLinksRequireExplicitASTAndBoundBase(t *testing.T) {
	source := "[source](src/main.go#L12) `secret.txt` naked.txt [web](https://example.org/x) [bad](command:open) [absolute](/tmp/report.md)"
	links := localFileLinks(source, "/project")
	if len(links) != 2 || links[0].path != "/project/src/main.go" || links[0].line != 12 {
		t.Fatalf("unexpected links: %#v", links)
	}
	old := localFileLinks(source, "")
	if len(old) != 1 || old[0].path != "/tmp/report.md" {
		t.Fatalf("old relative link inferred cwd: %#v", old)
	}
}

// A selected name longer than the list width must be truncated before lipgloss
// applies Width: lipgloss wraps overflowing content, and the extra visual row
// desynchronizes every following file row's click target.
func TestFileViewerSelectedLongNameStaysOnOneRow(t *testing.T) {
	longName := strings.Repeat("long-name-", 12) + "report.md"
	m := Model{width: 80, height: 20}
	m.fileViewer = fileViewerState{open: true, files: []message.DeliveredFile{
		deliveryTestFile("f1", longName, "", "/project/"+longName),
		deliveryTestFile("f2", "second.md", "", "/project/second.md"),
	}}

	p := m.fileViewerLayout()
	secondRow := -1
	for i, row := range p.rows {
		if strings.Contains(row, "\n") {
			t.Fatalf("row %d contains an embedded newline: %q", i, row)
		}
		if width := ansi.StringWidth(row); width > p.width {
			t.Fatalf("row %d width = %d > %d: %q", i, width, p.width, terminaltext.StripANSI(row))
		}
		if strings.Contains(terminaltext.StripANSI(row), "second.md") {
			secondRow = i
		}
	}
	if secondRow < 0 {
		t.Fatalf("second file has no rendered row:\n%s", strings.Join(p.rows, "\n"))
	}
	if !strings.Contains(terminaltext.StripANSI(p.rows[secondRow-1]), "…") {
		t.Fatalf("selected long name was not truncated: %q", terminaltext.StripANSI(p.rows[secondRow-1]))
	}
	for _, hit := range p.hits {
		if hit.kind == fileHitEntry && hit.index == 1 && hit.row != secondRow {
			t.Fatalf("second file hit row = %d, want visual row %d", hit.row, secondRow)
		}
	}
}

func TestDeliveryCardNarrowFileRowKeepsStableIdentity(t *testing.T) {
	block := &Block{DeliveredFiles: []message.DeliveredFile{{ID: "stable-file", DisplayName: "summary.pdf", MIME: "application/pdf", Locator: message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Root: "/project", Path: "/project/summary.pdf"}}}}}
	lines := block.renderDeliveryCard(40)
	if len(block.deliveryHits) != 1 {
		t.Fatal("missing file row target")
	}
	for _, hit := range block.deliveryHits {
		if hit.fileID != "stable-file" || hit.line >= len(lines) || hit.end > 40 {
			t.Fatalf("invalid file hit: %#v", hit)
		}
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 40 {
			t.Fatal("delivery card overflows narrow terminal")
		}
	}
}
