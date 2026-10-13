package tui

import (
	"context"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/keakon/chord/internal/filedelivery"
	"github.com/keakon/chord/internal/message"
)

type FileOpenRequest struct {
	Locator message.FileLocator
	Line    int
}
type localFileLink struct {
	path string
	line int
}

// Only explicit Markdown link nodes are actionable. Code spans, naked paths,
// terminal hyperlinks, and arbitrary URI schemes are not file references.
func localFileLinks(source, base string) []localFileLink {
	if len(source) > fileViewerMarkdownLimit {
		return nil
	}
	data := []byte(source)
	root := goldmark.New().Parser().Parse(text.NewReader(data))
	var links []localFileLink
	_ = ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		var destination []byte
		switch link := n.(type) {
		case *ast.Link:
			destination = link.Destination
		case *ast.Image:
			destination = link.Destination
		default:
			return ast.WalkContinue, nil
		}
		target, err := url.Parse(string(destination))
		if err != nil || (target.Scheme != "" && target.Scheme != "file") || (target.Host != "" && target.Host != "localhost" && runtime.GOOS != "windows") || target.RawQuery != "" {
			return ast.WalkContinue, nil
		}
		path := target.Path
		if runtime.GOOS == "windows" && target.Scheme == "file" {
			if target.Host != "" && target.Host != "localhost" {
				path = "//" + target.Host + path
			} else if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
				path = path[1:]
			}
		}
		if path == "" {
			return ast.WalkContinue, nil
		}
		if !filepath.IsAbs(path) {
			if !filepath.IsAbs(base) {
				return ast.WalkContinue, nil
			}
			path = filepath.Join(base, path)
		}
		line := 0
		if target.Fragment != "" {
			if !strings.HasPrefix(target.Fragment, "L") {
				return ast.WalkContinue, nil
			}
			line, err = strconv.Atoi(strings.TrimPrefix(target.Fragment, "L"))
			if err != nil || line < 1 {
				return ast.WalkContinue, nil
			}
		}
		links = append(links, localFileLink{path: path, line: line})
		if len(links) >= 16 {
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return links
}

type fileLinksLoadedMsg struct {
	owner imageViewerOwner
	files []message.DeliveredFile
	lines []int
}

func (m *Model) openLocalFileLinks(links []localFileLink) tea.Cmd {
	if len(links) == 0 {
		return nil
	}
	if m.agent != nil && m.deliverySessionDir() == "" {
		return m.enqueueToast("Remote file preview requires a download; copy the original path", "warn")
	}
	if m.fileViewer.cancel != nil {
		m.fileViewer.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.fileViewer.cancel = cancel
	m.fileViewerGeneration++
	req := fileViewerRequest{checkoutGeneration: m.workingDirGeneration, ctx: ctx, owner: m.imageViewerOwner(), generation: m.fileViewerGeneration, links: append([]localFileLink(nil), links...)}
	if m.fileViewerBusy {
		m.fileViewerNext = &req
		return nil
	}
	m.fileViewerBusy = true
	return func() tea.Msg { return loadFileViewer(req) }
}

func loadLocalFileLinks(req fileViewerRequest) fileLinksLoadedMsg {
	result := fileLinksLoadedMsg{owner: req.owner}
	for _, link := range req.links {
		if req.ctx.Err() != nil {
			break
		}
		loc, err := filedelivery.WorkspaceLocator(filepath.Dir(link.path), link.path)
		if err != nil {
			continue
		}
		file, err := filedelivery.Inspect(req.ctx, loc, "", nil)
		if err != nil {
			continue
		}
		file.ID = "reference:" + link.path
		result.files = append(result.files, file)
		result.lines = append(result.lines, link.line)
	}
	return result
}

func (m *Model) handleFileLinksLoaded(result fileLinksLoadedMsg) tea.Cmd {
	if result.owner != m.imageViewerOwner() {
		return nil
	}
	if len(result.files) == 0 {
		return m.enqueueToast("Referenced files are unavailable at their original paths", "warn")
	}
	cmd := m.openDeliveryFiles(result.files)
	m.fileViewer.linkLines = result.lines
	for _, line := range result.lines {
		if line > 0 {
			m.fileViewer.markdown = false
			break
		}
	}
	return cmd
}
