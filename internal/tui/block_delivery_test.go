package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/desktop"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func deliveryTestBlock(workingDir string, files ...message.DeliveredFile) *Block {
	return &Block{
		ID: 6, Type: BlockToolCall, ToolName: "present", ResultDone: true,
		displayWorkingDir: workingDir,
		DeliveredFiles:    files,
	}
}

func deliveryTestFile(id, name, desc, path string) message.DeliveredFile {
	return message.DeliveredFile{
		ID: id, DisplayName: name, MIME: "text/markdown", Description: desc,
		Locator: message.FileLocator{
			Kind:      message.FileLocatorWorkspace,
			Workspace: &message.WorkspaceFileLocator{Root: "/project", Path: path},
		},
	}
}

// A delivered-files card is a message, so it has to render like one: a label
// badge with its block number, on the standard tool card surface. It used to
// render through DirectoryBorderStyle (the overlay/dialog border) with no label
// at all, which is what made it read as a floating dialog pasted into the
// transcript.
func TestDeliveryCardRendersAsLabelledToolCard(t *testing.T) {
	block := deliveryTestBlock("/project", deliveryTestFile("f1", "notes.md", "session note", "/project/notes.md"))
	lines := block.Render(80, "")
	joined := terminaltext.StripANSI(strings.Join(lines, "\n"))

	if !strings.Contains(joined, "DELIVERED #7") {
		t.Fatalf("card has no label badge with block number:\n%s", joined)
	}
	if !strings.Contains(joined, "1 file(s) delivered") {
		t.Errorf("card does not report what it delivered:\n%s", joined)
	}
	for i, line := range lines {
		if ansi.StringWidth(line) > 80 {
			t.Errorf("line %d overflows the card width: %d", i, ansi.StringWidth(line))
		}
	}
}

// The four bracketed buttons are the card's whole interaction surface, and the
// header has to say what they do — the previous first line was a bare
// "Delivered files · Enter or click for actions" string with no label.
func TestDeliveryCardExplainsItsButtons(t *testing.T) {
	block := deliveryTestBlock("/project", deliveryTestFile("f1", "notes.md", "", "/project/notes.md"))
	joined := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))

	for _, action := range fileActions {
		if !strings.Contains(joined, "["+action+"]") {
			t.Errorf("card does not offer the %s action:\n%s", action, joined)
		}
	}
	if !strings.Contains(joined, "Enter or click a button") {
		t.Errorf("card does not tell the reader the buttons are actionable:\n%s", joined)
	}
}

// A hit must land on the button it names. The card used to mix a hardcoded
// column with a border offset that no longer applied, so every hit pointed one
// line above the row it described and the buttons could not be pressed.
func TestDeliveryCardHitsPointAtTheirButtons(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		files := []message.DeliveredFile{
			deliveryTestFile("f1", "notes.md", "session note", "/project/notes.md"),
			deliveryTestFile("f2", "report.md", "second", "/project/report.md"),
		}
		block := deliveryTestBlock("/project", files...)
		lines := block.Render(width, "")
		if len(block.deliveryHits) != len(files)*len(fileActions) {
			t.Fatalf("width %d: got %d hits, want %d", width, len(block.deliveryHits), len(files)*len(fileActions))
		}
		seen := make(map[string]bool, len(block.deliveryHits))
		for _, hit := range block.deliveryHits {
			if hit.line < 0 || hit.line >= len(lines) {
				t.Fatalf("width %d: hit line %d outside card (%d lines)", width, hit.line, len(lines))
			}
			if hit.start >= hit.end {
				t.Fatalf("width %d: empty hit range %d..%d", width, hit.start, hit.end)
			}
			plain := terminaltext.StripANSI(lines[hit.line])
			runes := []rune(plain)
			if hit.end > len(runes) {
				t.Fatalf("width %d: hit column %d past line end %d", width, hit.end, len(runes))
			}
			// The named action's own button text has to sit under the hit.
			button := "[" + fileActions[hit.action] + "]"
			if got := string(runes[hit.start:hit.end]); got != button {
				t.Errorf("width %d: hit covers %q, want %q (line %q)", width, got, button, plain)
			}
			key := hit.fileID + ":" + fileActions[hit.action]
			if seen[key] {
				t.Errorf("width %d: duplicate hit for %s", width, key)
			}
			seen[key] = true
		}
	}
}

// The card body and the file list must name a file the same way. The list used
// to print the absolute path while the card printed the working-directory
// relative one, so the same file had two identities on screen.
func TestDeliveryPathDisplayIsRelativeAndSharedWithTheList(t *testing.T) {
	file := deliveryTestFile("f1", "notes.md", "", "/project/docs/notes.md")

	if got := deliveryDisplayPath(file, "/project/docs"); got != "notes.md" {
		t.Errorf("card path = %q, want notes.md", got)
	}
	if got := deliveryDisplayPath(file, "/project"); got != "docs/notes.md" {
		t.Errorf("card path = %q, want docs/notes.md", got)
	}
	// A path outside the working directory keeps its absolute form: a relative
	// path that climbs out is harder to act on than the absolute one.
	if got := deliveryDisplayPath(file, "/elsewhere"); got != "/project/docs/notes.md" {
		t.Errorf("outside path = %q, want the absolute path", got)
	}

	m := &Model{width: 80, height: 20, workingDir: "/project/docs"}
	m.fileViewer = fileViewerState{open: true, files: []message.DeliveredFile{file}}
	layout := m.fileViewerLayout()
	joined := terminaltext.StripANSI(strings.Join(layout.rows, "\n"))
	if strings.Contains(joined, "/project/docs/notes.md") {
		t.Errorf("file list still prints the absolute path:\n%s", joined)
	}
	if !strings.Contains(joined, "notes.md") {
		t.Errorf("file list lost the file:\n%s", joined)
	}
}

// present claims Enter for the file view, so it must also report itself as
// non-foldable. While canToggleAtWidth said otherwise the folded branch in
// renderDeliveryCard was unreachable and its "… N more files" row could never
// appear.
func TestPresentCardIsNeverFoldable(t *testing.T) {
	block := deliveryTestBlock("/project", deliveryTestFile("f1", "notes.md", "", "/project/notes.md"))
	if block.canToggleAtWidth(80) {
		t.Error("present card reports itself foldable while Enter opens the file view")
	}
	if !toolCardAlwaysExpanded(block.ToolName) {
		t.Error("present is missing from the always-expanded set")
	}
	if block.ToggleAtWidth(80) {
		t.Error("present card folded despite being pinned expanded")
	}
}

// One file has nothing to choose between, so Enter must show it. Opening the
// list first spent a keystroke to show the same name the card just showed.
func TestEnterOnSingleDeliveryPreviewsWithoutTheList(t *testing.T) {
	block := deliveryTestBlock("/project", deliveryTestFile("f1", "notes.md", "", "/project/notes.md"))
	m := &Model{width: 80, height: 40}
	m.viewport = &Viewport{width: 80, height: 40, blocks: []*Block{block}}
	m.focusedBlockID = block.ID

	m.normalActivation()

	if m.mode != ModeFileViewer {
		t.Fatalf("mode = %v, want ModeFileViewer", m.mode)
	}
	if !m.fileViewer.open || !m.fileViewer.showing {
		t.Fatalf("single delivery did not open on its content: open=%v showing=%v", m.fileViewer.open, m.fileViewer.showing)
	}
}

func TestEnterOnSeveralDeliveriesStaysOnTheList(t *testing.T) {
	block := deliveryTestBlock("/project",
		deliveryTestFile("f1", "notes.md", "", "/project/notes.md"),
		deliveryTestFile("f2", "report.md", "", "/project/report.md"),
	)
	m := &Model{width: 80, height: 40}
	m.viewport = &Viewport{width: 80, height: 40, blocks: []*Block{block}}
	m.focusedBlockID = block.ID

	m.normalActivation()

	if !m.fileViewer.open || m.fileViewer.showing {
		t.Fatalf("multi-file delivery skipped the chooser: open=%v showing=%v", m.fileViewer.open, m.fileViewer.showing)
	}
}

// Esc leaves the file view; it does not step back through it. Backing out of a
// single file's preview stranded the reader on a one-row menu showing an
// absolute path, so closing took two presses to open.
func TestEscClosesSingleDeliveryPreview(t *testing.T) {
	for _, showing := range []bool{true, false} {
		m := &Model{width: 80, height: 40, mode: ModeFileViewer}
		m.fileViewer = fileViewerState{
			open: true, showing: showing, prevMode: ModeNormal,
			files: []message.DeliveredFile{deliveryTestFile("f1", "notes.md", "", "/project/notes.md")},
			lines: []string{"hello"},
		}

		m.handleFileViewerKey(tea.KeyPressMsg{Code: tea.KeyEscape})

		if m.fileViewer.open {
			t.Fatalf("showing=%v: Esc left the file view open", showing)
		}
		if m.mode != ModeNormal {
			t.Fatalf("showing=%v: mode = %v, want ModeNormal", showing, m.mode)
		}
	}
}

// Several files still need the list, so Esc goes back to it rather than out.
func TestEscFromMultiFilePreviewReturnsToTheList(t *testing.T) {
	m := &Model{width: 80, height: 40, mode: ModeFileViewer}
	m.fileViewer = fileViewerState{
		open: true, showing: true, prevMode: ModeNormal,
		files: []message.DeliveredFile{
			deliveryTestFile("f1", "notes.md", "", "/project/notes.md"),
			deliveryTestFile("f2", "report.md", "", "/project/report.md"),
		},
		lines: []string{"hello"},
	}

	m.handleFileViewerKey(tea.KeyPressMsg{Code: tea.KeyEscape})

	if !m.fileViewer.open || m.fileViewer.showing {
		t.Fatalf("multi-file preview did not fall back to the list: open=%v showing=%v", m.fileViewer.open, m.fileViewer.showing)
	}
}

// The status bar has to name the one thing Esc will do. "back / close file
// view" is what taught readers that Esc might only step back one layer.
func TestFileViewerEscHintNamesOneAction(t *testing.T) {
	one := []message.DeliveredFile{deliveryTestFile("f1", "notes.md", "", "/project/notes.md")}
	two := append(one, deliveryTestFile("f2", "report.md", "", "/project/report.md"))

	cases := []struct {
		name  string
		state fileViewerState
		want  string
	}{
		{"preview one file", fileViewerState{open: true, showing: true, files: one}, "close file view"},
		{"preview many files", fileViewerState{open: true, showing: true, files: two}, "back to file list"},
		{"list", fileViewerState{open: true, files: two}, "close file view"},
		{"app menu", fileViewerState{open: true, showing: true, files: two, apps: deliveryTestApps()}, "close app list"},
		{"input", fileViewerState{open: true, showing: true, files: two, inputMode: "/"}, "cancel input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Model{mode: ModeFileViewer, fileViewer: tc.state}
			if got := m.fileViewerEscHint(); got != tc.want {
				t.Errorf("hint = %q, want %q", got, tc.want)
			}
		})
	}
}

// A button press already names its action, so the view must not also auto-load
// the file — that issued two previews for one click.
func TestDeliveryCardButtonDoesNotDoubleLoad(t *testing.T) {
	block := deliveryTestBlock("/project", deliveryTestFile("f1", "notes.md", "", "/project/notes.md"))
	block.renderDeliveryCard(80)

	m := &Model{width: 80, height: 40}
	for _, hit := range block.deliveryHits {
		if hit.action != 3 { // Copy path; it does not load content.
			continue
		}
		cmd, handled := m.activateDeliveryCard(block, hit.line, hit.start)
		if !handled || cmd == nil {
			t.Fatal("copy-path button did not dispatch")
		}
		if m.fileViewer.showing {
			t.Error("pressing a button also started a preview load")
		}
		if m.fileViewer.selected != 0 || len(m.fileViewer.files) != 1 {
			t.Errorf("button did not select its file: selected=%d files=%d", m.fileViewer.selected, len(m.fileViewer.files))
		}
		return
	}
	t.Fatal("no Copy path hit recorded")
}
func deliveryTestApps() []desktop.Application {
	return []desktop.Application{{ID: "app", Name: "Example"}}
}

// A click that misses every delivery button is not the card's action: the body
// is prose about the files, and the card's own hint says "click a button".
// Treating a miss as "show me this delivery" turned the whole card into an
// invisible button, so a reader could not start a text selection on it.
func TestDeliveryCardClickMissesButtonsStartsSelection(t *testing.T) {
	block := deliveryTestBlock("/project", deliveryTestFile("f1", "notes.md", "session note", "/project/notes.md"))
	m := clickBlockLineContaining(t, block, 80, 24, "session note")

	if m.fileViewer.open {
		t.Fatal("click on the card body opened the file view")
	}
	if !m.hasMouseSelection() {
		t.Fatal("click on the card body did not start a text selection")
	}
}

// Pressing a rendered button still acts, even though the card body no longer
// does.
func TestDeliveryCardButtonClickOpensFileView(t *testing.T) {
	block := deliveryTestBlock("/project", deliveryTestFile("f1", "notes.md", "session note", "/project/notes.md"))
	m := clickBlockLineContaining(t, block, 80, 24, "file(s) delivered")

	for _, hit := range block.deliveryHits {
		if hit.action != 0 { // Preview
			continue
		}
		m.Update(tea.MouseClickMsg{X: hit.start, Y: m.viewport.offset + hit.line, Button: tea.MouseLeft})
		if !m.fileViewer.open {
			t.Fatal("preview button did not open the file view")
		}
		return
	}
	t.Fatal("no Preview hit recorded")
}

// A compaction checkpoint carries the files delivered before it, but it is still
// a prose card: clicking it must start a text selection, not open a preview.
// The block used to be routed to the delivery card purely because
// DeliveredFiles was non-empty, so every click in the summary opened the file
// view instead of selecting.
func TestCompactionSummaryClickStartsSelectionNotDelivery(t *testing.T) {
	content := "[Context Summary]\n\nSummary body text\n"
	block := &Block{
		ID:                   1,
		Type:                 BlockCompactionSummary,
		Content:              content,
		CompactionSummaryRaw: content,
		DeliveredFiles:       []message.DeliveredFile{deliveryTestFile("f1", "notes.md", "", "/project/notes.md")},
	}
	m := clickBlockLineContaining(t, block, 80, 24, "Summary body text")

	if m.fileViewer.open {
		t.Fatal("click on the compaction summary opened the file view")
	}
	if !m.hasMouseSelection() {
		t.Fatal("click on the compaction summary did not start a text selection")
	}
}

// Enter keeps the checkpoint's earlier deliveries reachable: the card advertises
// "(Enter)", and the mouse no longer opens them. A refactor that applied the
// render gate to the keyboard path too would strand those files.
func TestCompactionSummaryEnterOpensEarlierDeliveries(t *testing.T) {
	content := "[Context Summary]\n\nSummary body text\n"
	block := &Block{
		ID:                   1,
		Type:                 BlockCompactionSummary,
		Content:              content,
		CompactionSummaryRaw: content,
		DeliveredFiles:       []message.DeliveredFile{deliveryTestFile("f1", "notes.md", "", "/project/notes.md")},
	}
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeNormal
	m.viewport.AppendBlock(block)
	m.layout = m.generateLayout(m.width, m.height)
	_ = m.viewport.Render("", nil, -1, -1, "")
	m.focusedBlockID = block.ID

	cmd := m.normalActivation()

	if cmd == nil || !m.fileViewer.open {
		t.Fatal("Enter on the checkpoint did not open its earlier deliveries")
	}
}

// clickBlockLineContaining lays out a model whose viewport holds block alone,
// left-clicks the first rendered line containing target, and returns the model.
func clickBlockLineContaining(t *testing.T, block *Block, width, height int, target string) *Model {
	t.Helper()
	m := NewModelWithSize(nil, width, height)
	m.mode = ModeNormal
	m.viewport.AppendBlock(block)
	m.layout = m.generateLayout(m.width, m.height)
	lines := block.Render(m.viewport.width, "")
	_ = m.viewport.Render("", nil, -1, -1, "")
	line := -1
	for i, l := range lines {
		if strings.Contains(terminaltext.StripANSI(l), target) {
			line = i
			break
		}
	}
	if line < 0 {
		t.Fatalf("no rendered line contains %q in %#v", target, lines)
	}
	m.Update(tea.MouseClickMsg{X: 2, Y: m.viewport.offset + line, Button: tea.MouseLeft})
	return &m
}
