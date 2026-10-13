package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/desktop"
	"github.com/keakon/chord/internal/filedelivery"
	"github.com/keakon/chord/internal/message"
)

type fileViewerState struct {
	theme                   string
	checkout                string
	checkoutGeneration      uint64
	linkLines               []int
	action                  int
	open                    bool
	owner                   imageViewerOwner
	prevMode                Mode
	files                   []message.DeliveredFile
	selected                int
	showing                 bool
	markdown, wrap          bool
	offset, horizontal      int
	wrapOffset              int
	raw                     []byte
	lines, plain            []string
	note                    string
	inputMode, input, query string
	apps                    []desktop.Application
	appIndex                int
	generation              uint64
	cancel                  context.CancelFunc
}

func (m *Model) deliverySessionDir() string {
	if provider, ok := m.agent.(interface{ SessionDir() string }); ok {
		return provider.SessionDir()
	}
	return ""
}
func (m *Model) openDeliveryFiles(files []message.DeliveredFile) tea.Cmd {
	if len(files) == 0 {
		return nil
	}
	previous := m.mode
	if m.fileViewer.open {
		previous = m.fileViewer.prevMode
	}
	if m.fileViewer.cancel != nil {
		m.fileViewer.cancel()
	}
	m.fileViewerGeneration++
	m.fileViewer = fileViewerState{generation: m.fileViewerGeneration, theme: m.theme.Name, checkout: m.workingDir, checkoutGeneration: m.workingDirGeneration, open: true, owner: m.imageViewerOwner(), prevMode: previous, files: message.CloneDeliveredFiles(files), markdown: true}
	return m.switchModeWithIME(ModeFileViewer)
}
func (m *Model) closeFileViewer() tea.Cmd {
	previous := m.fileViewer.prevMode
	if m.fileViewer.cancel != nil {
		m.fileViewer.cancel()
	}
	m.fileViewerNext = nil
	m.fileViewerGeneration++
	m.fileViewer = fileViewerState{}
	return m.switchModeWithIME(previous)
}
func (m *Model) prepareFileViewer() tea.Cmd {
	v := &m.fileViewer
	if m.agent != nil && m.deliverySessionDir() == "" {
		return m.enqueueToast("Remote file preview requires a download; copy the original path", "warn")
	}
	if !v.open || len(v.files) == 0 {
		return nil
	}
	if v.cancel != nil {
		v.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	m.fileViewerGeneration++
	v.generation = m.fileViewerGeneration
	v.note = "Loading…"
	v.raw = nil
	v.lines = nil
	v.plain = nil
	v.theme = m.theme.Name
	v.showing = true
	file := v.files[v.selected]
	if m.filePreviewCache == nil {
		m.filePreviewCache = &fileViewerCache{}
	}
	req := fileViewerRequest{checkoutGeneration: m.workingDirGeneration, cache: m.filePreviewCache, ctx: ctx, generation: v.generation, owner: v.owner, file: file, session: m.deliverySessionDir(), width: max(1, m.width-10), markdown: v.markdown && strings.HasPrefix(file.MIME, "text/markdown"), style: contentMarkdownStyleConfig(), highlighter: newCodeHighlighterWithLanguage(file.DisplayName, "", "")}
	if m.fileViewerBusy {
		m.fileViewerNext = &req
		return nil
	}
	m.fileViewerBusy = true
	return func() tea.Msg { return loadFileViewer(req) }
}
func (m *Model) handleFileViewerLoaded(result fileViewerLoadedMsg) tea.Cmd {
	m.fileViewerBusy = false
	var next tea.Cmd
	if pending := m.fileViewerNext; pending != nil {
		m.fileViewerNext = nil
		m.fileViewerBusy = true
		req := *pending
		next = func() tea.Msg { return loadFileViewer(req) }
	}
	if result.links != nil {
		if result.request.generation != m.fileViewerGeneration || result.request.owner != m.imageViewerOwner() || result.request.checkoutGeneration != m.workingDirGeneration {
			return next
		}
		return tea.Batch(next, m.handleFileLinksLoaded(*result.links))
	}
	v := &m.fileViewer
	if !v.open || !v.showing || len(v.files) == 0 || result.request.file.ID != v.files[v.selected].ID || result.request.generation != v.generation || result.request.owner != m.imageViewerOwner() || result.request.checkoutGeneration != m.workingDirGeneration {
		return next
	}
	if result.image != nil {
		v.showing = false
		open := m.openImageViewerItems([]BlockImagePart{*result.image}, 0, ModeFileViewer)
		if result.note != "" {
			return tea.Batch(next, open, m.enqueueToast(result.note, "warn"))
		}
		return tea.Batch(next, open)
	}
	v.note = result.note
	v.raw = result.raw
	v.lines = result.lines
	v.plain = result.plain
	v.offset = 0
	v.wrapOffset = 0
	if v.selected < len(v.linkLines) && v.linkLines[v.selected] > 0 {
		v.offset = min(max(0, len(v.lines)-1), v.linkLines[v.selected]-1)
	}
	if result.err != nil {
		v.note = result.err.Error()
	}
	return next
}
func (m *Model) handleFileViewerKey(msg tea.KeyMsg) tea.Cmd {
	v := &m.fileViewer
	key := msg.String()
	if v.inputMode != "" {
		switch key {
		case "esc":
			v.inputMode = ""
			v.input = ""
		case "backspace":
			r := []rune(v.input)
			if len(r) > 0 {
				v.input = string(r[:len(r)-1])
			}
		case "enter":
			if v.inputMode == "/" {
				v.query = v.input
				m.searchFileViewer(1)
			} else if n, e := strconv.Atoi(v.input); e == nil && n > 0 && n <= len(v.lines) && !(v.markdown && strings.HasPrefix(v.files[v.selected].MIME, "text/markdown")) {
				v.offset = n - 1
				v.wrapOffset = 0
			} else {
				v.note = "Source line is outside the loaded range, or source mode is required"
			}
			v.inputMode = ""
		default:
			if len([]rune(key)) == 1 || key == "space" {
				if key == "space" {
					key = " "
				}
				v.input += key
			}
		}
		return nil
	}
	if len(v.apps) > 0 {
		switch key {
		case "esc":
			v.apps = nil
		case "up", "k":
			v.appIndex = max(0, v.appIndex-1)
		case "down", "j":
			v.appIndex = min(len(v.apps)-1, v.appIndex+1)
		case "enter":
			if v.appIndex >= 0 && v.appIndex < len(v.apps) {
				return m.fileDesktopAction("with", v.apps[v.appIndex].ID)
			}
		case "r":
			if refresher, ok := m.desktopService.(interface{ RefreshAssociations() }); ok {
				refresher.RefreshAssociations()
			}
			return m.fileDesktopAction("associations", "")
		}
		return nil
	}
	switch key {
	case "esc":
		if v.showing {
			m.invalidateFilePreview()
			v.showing = false
			return nil
		}
		return m.closeFileViewer()
	case "up", "k":
		if v.showing {
			m.scrollFileViewer(-1)
		} else {
			m.selectFileViewerFile(max(0, v.selected-1))
		}
	case "down", "j":
		if v.showing {
			m.scrollFileViewer(1)
		} else {
			m.selectFileViewerFile(min(len(v.files)-1, v.selected+1))
		}
	case "pgup":
		m.scrollFileViewer(-m.fileViewerPageSize())
	case "pgdown":
		m.scrollFileViewer(m.fileViewerPageSize())
	case "home":
		v.offset = 0
		v.wrapOffset = 0
	case "end":
		m.fileViewerEnd()
	case "left":
		v.horizontal = max(0, v.horizontal-4)
	case "right":
		v.horizontal += 4
	case "w":
		v.wrap = !v.wrap
		v.wrapOffset = 0
	case "l":
		if v.showing {
			return m.openLocalFileLinks(localFileLinks(string(v.raw), filepath.Dir(deliveryPath(v.files[v.selected], m.deliverySessionDir()))))
		}
	case "m":
		v.markdown = !v.markdown
		return m.prepareFileViewer()
	case "tab":
		v.action = (v.action + 1) % len(fileActions)
	case "shift+tab":
		v.action = (v.action + len(fileActions) - 1) % len(fileActions)
	case "enter":
		return m.activateFileAction(v.action)
	case "r":
		return m.prepareFileViewer()
	case "/", "g":
		v.inputMode = key
		v.input = ""
	case "n":
		m.searchFileViewer(1)
	case "N":
		m.searchFileViewer(-1)
	case "p":
		return writeClipboardCmd(deliveryPath(v.files[v.selected], m.deliverySessionDir()), "File path copied")
	case "y":
		if !v.showing || v.raw == nil {
			return nil
		}
		if strings.ContainsRune(string(v.raw), 27) {
			return tea.Batch(writeClipboardCmd(string(v.raw), "Loaded source copied"), m.enqueueToast("Copied source contains file escape control characters", "warn"))
		}
		return writeClipboardCmd(string(v.raw), "Loaded source copied")
	case "o":
		return m.fileDesktopAction("open", "")
	case "a":
		return m.fileDesktopAction("associations", "")
	}
	return nil
}
func (m *Model) searchFileViewer(delta int) {
	v := &m.fileViewer
	if v.query == "" || len(v.plain) == 0 {
		return
	}
	for step := 1; step <= len(v.plain); step++ {
		i := (v.offset + delta*step%len(v.plain) + len(v.plain)) % len(v.plain)
		if strings.Contains(v.plain[i], v.query) {
			v.offset = i
			v.wrapOffset = 0
			v.note = "Search covers loaded text in the current mode"
			return
		}
	}
	v.note = "No match in loaded text"
}
func deliveryPath(f message.DeliveredFile, session string) string {
	if f.Locator.Workspace != nil {
		return f.Locator.Workspace.Path
	}
	if f.Locator.Artifact != nil {
		return filepath.Join(session, filepath.FromSlash(f.Locator.Artifact.RelPath))
	}
	return ""
}

type fileDesktopResult struct {
	token      uint64
	path       string
	fileID     string
	owner      imageViewerOwner
	generation uint64
	apps       []desktop.Application
	err        error
	action     string
	note       string
	truncated  bool
}

func (m *Model) fileDesktopAction(action, id string) tea.Cmd {
	v := &m.fileViewer
	if m.desktopService == nil {
		m.desktopService = desktop.New()
	}
	service := m.desktopService
	f := v.files[v.selected]
	session := m.deliverySessionDir()
	if m.agent != nil && session == "" {
		return m.enqueueToast("Local opening is unavailable for remote files; copy the original path", "warn")
	}
	path := deliveryPath(f, session)
	if m.fileActionsBusy == nil {
		m.fileActionsBusy = make(map[string]uint64)
	}
	if m.fileActionsBusy[path] != 0 || len(m.fileActionsBusy) >= 4 {
		return m.enqueueToast("A system request is still processing; wait before retrying", "info")
	}
	m.fileActionToken++
	token := m.fileActionToken
	m.fileActionsBusy[path] = token
	owner := v.owner
	generation := v.generation
	v.note = "System request in progress…"
	command := func() tea.Msg {
		result := fileDesktopResult{owner: owner, generation: generation, action: action, path: path, fileID: f.ID, token: token}
		ctx := context.Background()
		current, err := filedelivery.Verify(ctx, f, session)
		if err != nil {
			result.err = err
			return result
		}
		result.note = fileVersionNote(f, current)
		path := deliveryPath(f, session)
		switch action {
		case "open":
			result.err = service.OpenDefault(ctx, path)
		case "with":
			result.err = service.OpenWith(ctx, path, id)
		case "associations":
			caps, err := service.Capabilities(ctx)
			if err != nil {
				result.err = err
			} else if caps.Enumerate {
				list, e := service.Associations(ctx, path)
				result.apps = list.Apps
				result.truncated = list.Truncated
				result.err = e
			} else if caps.NativeChooser {
				result.action = "chooser"
				result.err = service.ChooseApplication(ctx, path)
			} else {
				result.err = fmt.Errorf("application selection unavailable: %s", caps.Reason)
			}
		}
		return result
	}
	return tea.Batch(command, tea.Tick(10*time.Second, func(time.Time) tea.Msg { return fileDesktopSlowMsg{owner: owner, path: path, token: token} }))

}
func (m *Model) handleFileDesktopResult(result fileDesktopResult) tea.Cmd {
	if m.fileActionsBusy[result.path] == result.token {
		delete(m.fileActionsBusy, result.path)
	}
	v := &m.fileViewer
	if !v.open || result.owner != m.imageViewerOwner() || result.generation != v.generation || v.files[v.selected].ID != result.fileID {
		return nil
	}
	if result.err != nil {
		v.note = result.err.Error()
	} else if result.action == "associations" {
		v.apps = result.apps
		v.appIndex = 0
		v.note = "System application query completed"
		if result.truncated {
			v.note += " · application list truncated"
		}
		if len(result.apps) == 0 {
			v.note = "No associated applications returned"
		}
	} else {
		v.apps = nil
		v.note = "Open request handed to the system"
		if result.action == "chooser" {
			v.note = "System application chooser closed"
		}
	}
	if result.note != "" {
		v.note = result.note + " · " + v.note
	}
	return nil
}

type fileDesktopSlowMsg struct {
	owner imageViewerOwner
	path  string
	token uint64
}

func (m *Model) handleFileDesktopSlow(result fileDesktopSlowMsg) tea.Cmd {
	if m.fileActionsBusy[result.path] != result.token || result.owner != m.imageViewerOwner() {
		return nil
	}
	if m.fileViewer.open {
		m.fileViewer.note = "System open request is still processing; do not retry yet"
	}
	return nil
}

// fileMenuStart keeps the selected entry visible even in a short terminal.
func fileMenuStart(selected, height, rowsPerItem int) int {
	visible := max(1, (height-1)/rowsPerItem)
	return max(0, selected-visible+1)
}
