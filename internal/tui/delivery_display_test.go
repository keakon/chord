package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestDeliveryDisplayPathsUsesCommonRoot(t *testing.T) {
	files := []message.DeliveredFile{
		deliveryTestFile("f1", "notes.md", "", "/project/docs/notes.md"),
		deliveryTestFile("f2", "report.md", "", "/project/docs/sub/report.md"),
	}
	root, paths := deliveryDisplayPaths(files, "/project")
	if root != "docs" {
		t.Fatalf("root = %q, want docs", root)
	}
	want := []string{"notes.md", "sub/report.md"}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
	if got := deliveryRootLabel(root); got != "docs" {
		t.Fatalf("root label = %q, want docs", got)
	}
}

func TestDeliveryDisplayPathsKeepsSinglePath(t *testing.T) {
	files := []message.DeliveredFile{deliveryTestFile("f1", "notes.md", "", "/project/docs/notes.md")}
	root, paths := deliveryDisplayPaths(files, "/project")
	if root != "" {
		t.Fatalf("root = %q, want empty", root)
	}
	if len(paths) != 1 || paths[0] != "docs/notes.md" {
		t.Fatalf("paths = %#v, want docs/notes.md", paths)
	}
}

func TestDeliveryTypeLabelHidesTextTypes(t *testing.T) {
	for _, mime := range []string{"text/plain; charset=utf-8", "text/markdown", ""} {
		if got := deliveryTypeLabel(mime); got != "" {
			t.Errorf("deliveryTypeLabel(%q) = %q, want empty", mime, got)
		}
	}
	if got := deliveryTypeLabel("image/png"); got != "image/png" {
		t.Errorf("deliveryTypeLabel(image/png) = %q", got)
	}
}

func TestDeliveryCardDistinguishesNamesFromDescriptions(t *testing.T) {
	block := deliveryTestBlock("/project", deliveryTestFile("f1", "notes.md", "session note", "/project/notes.md"))
	rendered := strings.Join(block.Render(100, ""), "\n")
	if !strings.Contains(rendered, currentStyles.ToolFileNameStyle.Render("notes.md")) {
		t.Fatalf("file name does not use the file-name style:\n%q", rendered)
	}
	if !strings.Contains(rendered, currentStyles.DimStyle.Render(toolFieldBodyIndent+"session note")) {
		t.Fatalf("description does not use the dim style:\n%q", rendered)
	}
}

func TestDeliveryFileListUsesOneRowPerFile(t *testing.T) {
	files := []message.DeliveredFile{
		deliveryTestFile("f1", "notes.md", "", "/project/docs/notes.md"),
		deliveryTestFile("f2", "report.md", "", "/project/docs/sub/report.md"),
	}
	m := &Model{width: 80, height: 24, workingDir: "/project"}
	m.fileViewer = fileViewerState{open: true, files: files, selected: 1}

	joined := terminaltext.StripANSI(strings.Join(m.fileViewerLayout().rows, "\n"))
	for _, unwanted := range []string{"text/markdown", "/project/docs/notes.md", "/project/docs/sub/report.md"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("file list still contains %q:\n%s", unwanted, joined)
		}
	}
	for _, want := range []string{"  docs/", "notes.md", "sub/", "▸ report.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("file list is missing %q:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "notes.md") != 1 || strings.Count(joined, "report.md") != 1 {
		t.Errorf("file list repeats a file across rows:\n%s", joined)
	}
}

func TestFileViewerPreviewOmitsCurrentFileLabel(t *testing.T) {
	workspace := deliveryTestFile("f1", "notes.md", "", "/project/notes.md")
	m := &Model{width: 80, height: 24}
	m.fileViewer = fileViewerState{open: true, showing: true, files: []message.DeliveredFile{workspace}, lines: []string{"hello"}}
	joined := terminaltext.StripANSI(strings.Join(m.fileViewerLayout().rows, "\n"))
	if strings.Contains(joined, "Current file") {
		t.Fatalf("workspace preview still says Current file:\n%s", joined)
	}
	if !strings.Contains(joined, "notes.md · source") {
		t.Fatalf("workspace preview header = %q", joined)
	}

	artifact := message.DeliveredFile{
		ID: "f2", DisplayName: "report.md", MIME: "text/markdown",
		Locator: message.FileLocator{Kind: message.FileLocatorArtifact, Artifact: &message.ArtifactFileLocator{RelPath: "artifacts/report.md"}},
	}
	m.fileViewer = fileViewerState{open: true, showing: true, markdown: true, files: []message.DeliveredFile{artifact}, lines: []string{"hello"}}
	joined = terminaltext.StripANSI(strings.Join(m.fileViewerLayout().rows, "\n"))
	if !strings.Contains(joined, "report.md · Declared version · Markdown") {
		t.Fatalf("artifact preview header = %q", joined)
	}
}

func TestDeliveryCardPathLineFitsWidth(t *testing.T) {
	for _, width := range []int{1, 8, 20, 80} {
		line := deliveryCardPathLine("/project/very/long/name.go", "text/plain", width)
		if got := terminaltext.Width(line); got > width {
			t.Errorf("width %d: path line is %d cells wide: %q", width, got, line)
		}
	}
	line := deliveryCardPathLine("preview.png", "image/png", 30)
	if !strings.Contains(terminaltext.StripANSI(line), "image/png") {
		t.Fatalf("image type label missing: %q", line)
	}
}
