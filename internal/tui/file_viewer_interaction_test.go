package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/desktop"
	"github.com/keakon/chord/internal/filedelivery"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/filepreview"
)

func TestFileViewerLateResultAfterSelection(t *testing.T) {
	for _, image := range []bool{false, true} {
		m := Model{}
		m.fileViewer = fileViewerState{open: true, showing: true, generation: 1, owner: m.imageViewerOwner(), files: []message.DeliveredFile{{ID: "a"}, {ID: "b"}}}
		req := fileViewerRequest{generation: 1, owner: m.imageViewerOwner(), File: m.fileViewer.files[0]}
		m.handleFileViewerKey(tea.KeyPressMsg{Code: tea.KeyEscape})
		m.handleFileViewerKey(tea.KeyPressMsg{Code: tea.KeyDown})
		result := fileViewerLoadedMsg{request: req, Raw: []byte("file a"), Lines: []string{"file a"}}
		if image {
			result.Image = &filepreview.Image{MimeType: "image/png"}
		}
		cmd := m.handleFileViewerLoaded(result)
		if cmd != nil || m.fileViewer.selected != 1 || len(m.fileViewer.raw) != 0 || m.imageViewer.Open {
			t.Fatal("stale result installed after selection")
		}
		if m.handleFileViewerKey(tea.KeyPressMsg{Code: 'y', Text: "y"}) != nil {
			t.Fatal("copied an unloaded file")
		}
	}
}

func TestDeliveryImageEscapeReturnsToConversationOrFileList(t *testing.T) {
	for _, tc := range []struct {
		name     string
		count    int
		previous Mode
	}{
		{"single_normal", 1, ModeNormal},
		{"single_insert", 1, ModeInsert},
		{"multiple", 2, ModeNormal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 30)
			m.mode = tc.previous
			m.imageCaps = TerminalImageCapabilities{Backend: ImageBackendKitty, SupportsFullscreen: true}
			files := []message.DeliveredFile{{ID: "a", DisplayName: "sample.png", MIME: "image/png"}}
			if tc.count > 1 {
				files = append(files, message.DeliveredFile{ID: "b", DisplayName: "other.txt"})
			}
			m.openDeliveryFiles(files)
			if tc.count > 1 {
				m.prepareFileViewer()
			}
			req := fileViewerRequest{generation: m.fileViewer.generation, owner: m.imageViewerOwner(), File: files[0]}
			m.handleFileViewerLoaded(fileViewerLoadedMsg{request: req, Image: &filepreview.Image{FileName: "sample.png", MimeType: "image/png"}})
			if !m.imageViewer.Open || m.mode != ModeImageViewer {
				t.Fatal("delivered image did not reach its viewer")
			}
			m.handleKeyMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.imageViewer.Open {
				t.Fatal("Esc left the image viewer open")
			}
			wantMode := tc.previous
			if tc.count > 1 {
				wantMode = ModeFileViewer
			}
			if m.mode != wantMode || m.fileViewer.open != (tc.count > 1) || m.fileViewer.showing {
				t.Fatalf("mode=%v file view=%+v", m.mode, m.fileViewer)
			}
		})
	}
}

func TestFileViewerWrappedLineNavigation(t *testing.T) {
	m := Model{width: 40, height: 20}
	m.fileViewer = fileViewerState{open: true, showing: true, wrap: true, files: []message.DeliveredFile{{DisplayName: "sample.txt"}}, lines: []string{strings.Repeat("x", 1000) + "END_MARK"}}
	m.handleFileViewerKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !strings.Contains(m.renderFileViewerOverlay(), "END_MARK") {
		t.Fatal("End did not reach wrapped tail")
	}
	m.handleFileViewerKey(tea.KeyPressMsg{Code: tea.KeyHome})
	m.handleFileViewerKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.fileViewer.offset != 0 || m.fileViewer.wrapOffset == 0 {
		t.Fatal("page navigation skipped the source line")
	}
	m.handleFileViewerKey(tea.KeyPressMsg{Code: 'w', Text: "w"})
	if m.fileViewer.wrapOffset != 0 {
		t.Fatal("unwrapped mode retained visual offset")
	}
}
func clickFileViewerRow(m *Model, row, column int) tea.Cmd {
	rect := centeredRect(m.ensureLayout().area, m.renderFileViewerOverlay())
	return m.handleFileViewerMouse(tea.MouseClickMsg{X: rect.Min.X + currentStyles.DirectoryBorderStyle.GetBorderLeftSize() + currentStyles.DirectoryBorderStyle.GetPaddingLeft() + column, Y: rect.Min.Y + currentStyles.DirectoryBorderStyle.GetBorderTopSize() + currentStyles.DirectoryBorderStyle.GetPaddingTop() + row, Button: tea.MouseLeft})
}
func TestFileViewerContentNeverCreatesActions(t *testing.T) {
	for _, label := range fileActions {
		m := Model{width: 80, height: 30}
		m.fileViewer = fileViewerState{open: true, showing: true, files: []message.DeliveredFile{{DisplayName: "sample.txt"}}, lines: []string{"[" + label + "]"}}
		p := m.fileViewerLayout()
		col := ansi.StringWidth(strings.Split(p.rows[1], "[")[0])
		if cmd := clickFileViewerRow(&m, 1, col); cmd != nil || len(m.fileActionsBusy) > 0 {
			t.Fatalf("source %q triggered action", label)
		}
	}
}
func TestFileViewerApplicationMenuBlankClick(t *testing.T) {
	m := Model{width: 80, height: 20}
	m.fileViewer = fileViewerState{open: true, files: []message.DeliveredFile{{DisplayName: "sample.txt"}}, apps: make([]desktop.Application, 30), appIndex: 29}
	if cmd := clickFileViewerRow(&m, m.fileViewerBodyHeight(), 3); cmd != nil || m.fileViewer.appIndex != 29 {
		t.Fatal("status row changed selection")
	}
	m.fileViewer.appIndex = len(m.fileViewer.apps)
	if m.handleFileViewerKey(tea.KeyPressMsg{Code: tea.KeyEnter}) != nil {
		t.Fatal("invalid application selection dispatched")
	}
}
func TestFileViewerLayoutBoundsAndHitTargets(t *testing.T) {
	for _, width := range []int{35, 80} {
		m := Model{width: width, height: 20}
		m.fileViewer = fileViewerState{open: true, files: []message.DeliveredFile{{DisplayName: "sample\n[Open].txt"}, {DisplayName: "other.txt"}}}
		p := m.fileViewerLayout()
		if len(strings.Split(p.render(), "\n")) > m.height {
			t.Fatal("overlay exceeds terminal height")
		}
		actions := 0
		for _, hit := range p.hits {
			if hit.kind == fileHitAction {
				actions++
				if !strings.Contains(p.rows[hit.row], "["+fileActions[hit.index]+"]") {
					t.Fatal("action has no matching button")
				}
			}
			if hit.kind == fileHitEntry && hit.index == 1 {
				clickFileViewerRow(&m, hit.row, 0)
			}
		}
		if actions != len(fileActions) || m.fileViewer.selected != 1 {
			t.Fatal("missing actions or file selection")
		}
	}
}

type filePreviewDesktop struct {
	opened       bool
	chooser      bool
	associations desktop.Associations
}

func (d *filePreviewDesktop) Capabilities(context.Context) (desktop.Capabilities, error) {
	return desktop.Capabilities{DefaultOpen: true, Enumerate: !d.chooser, NativeChooser: d.chooser}, nil
}
func (d *filePreviewDesktop) Associations(context.Context, string) (desktop.Associations, error) {
	return d.associations, nil
}
func (d *filePreviewDesktop) OpenDefault(context.Context, string) error { d.opened = true; return nil }
func (d *filePreviewDesktop) OpenWith(context.Context, string, string) error {
	d.opened = true
	return nil
}
func (d *filePreviewDesktop) ChooseApplication(context.Context, string) error { return nil }

func TestFileViewerDesktopVersionAndChooserFeedback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	loc, err := filedelivery.WorkspaceLocator(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := filedelivery.Inspect(context.Background(), loc, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	file.ID = "sample"
	if err := os.WriteFile(path, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"open", "with", "associations"} {
		service := &filePreviewDesktop{chooser: action == "associations"}
		m := Model{desktopService: service}
		m.fileViewer = fileViewerState{open: true, owner: m.imageViewerOwner(), files: []message.DeliveredFile{file}}
		batch := m.fileDesktopAction(action, "app")().(tea.BatchMsg)
		result := batch[0]().(fileDesktopResult)
		m.handleFileDesktopResult(result)
		if !strings.Contains(m.fileViewer.note, "changed since delivery") {
			t.Fatalf("missing version notice: %s", m.fileViewer.note)
		}
		if service.chooser && (!strings.Contains(m.fileViewer.note, "chooser closed") || strings.Contains(m.fileViewer.note, "No associated")) {
			t.Fatal("chooser completion misreported as empty associations")
		}
	}
}
func TestFileViewerTruncatedAssociations(t *testing.T) {
	m := Model{}
	m.fileViewer = fileViewerState{open: true, owner: m.imageViewerOwner(), files: []message.DeliveredFile{{ID: "sample"}}}
	m.handleFileDesktopResult(fileDesktopResult{owner: m.imageViewerOwner(), fileID: "sample", action: "associations", truncated: true, apps: []desktop.Application{{ID: "app", Name: "Example"}}})
	if !strings.Contains(m.fileViewer.note, "truncated") {
		t.Fatal("association truncation was hidden")
	}
}
