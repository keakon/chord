package tui

import (
	"fmt"
	_ "image/gif"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
)

var execExternalImageCommand = exec.Command

// openImageResultMsg is emitted after an async attempt to open a user image.
type openImageResultMsg struct {
	err error
}

type openImageViewerMsg struct {
	blockID    int
	imageIndex int
}

func imagePartsFromContentParts(parts []message.ContentPart) []BlockImagePart {
	if len(parts) == 0 {
		return nil
	}
	imageParts := make([]BlockImagePart, 0, len(parts))
	imageIndex := 0
	for _, part := range parts {
		if part.Type != "image" {
			continue
		}
		imageIndex++
		imageParts = append(imageParts, BlockImagePart{
			FileName:        imagePartDisplayName(part.FileName, part.ImagePath, part.MimeType, imageIndex),
			ImagePath:       part.ImagePath,
			MimeType:        part.MimeType,
			Data:            part.Data,
			Index:           imageIndex - 1,
			RenderStartLine: -1,
			RenderEndLine:   -1,
		})
	}
	if len(imageParts) == 0 {
		return nil
	}
	return imageParts
}

func imagePartDisplayName(fileName, imagePath, mimeType string, imageIndex int) string {
	name := strings.TrimSpace(fileName)
	if name != "" {
		base := strings.TrimSpace(filepath.Base(name))
		ext := strings.ToLower(filepath.Ext(base))
		if strings.EqualFold(base, "clipboard.png") || strings.EqualFold(base, "clipboard.jpg") || strings.EqualFold(base, "clipboard.jpeg") || strings.HasPrefix(strings.ToLower(base), "clipboard-") {
			return fmt.Sprintf("image%d%s", imageIndex, attachmentExtForMimeType(mimeType))
		}
		if base != "" && base != "." && base != string(filepath.Separator) {
			if ext == "" {
				return base + attachmentExtForMimeType(mimeType)
			}
			return base
		}
	}
	base := filepath.Base(strings.TrimSpace(imagePath))
	if base != "" && base != "." && base != string(filepath.Separator) {
		if strings.EqualFold(base, "clipboard.png") || strings.EqualFold(base, "clipboard.jpg") || strings.EqualFold(base, "clipboard.jpeg") || strings.HasPrefix(strings.ToLower(base), "clipboard-") {
			return fmt.Sprintf("image%d%s", imageIndex, attachmentExtForMimeType(mimeType))
		}
		return base
	}
	return fmt.Sprintf("image%d%s", imageIndex, attachmentExtForMimeType(mimeType))
}

func thumbnailPreviewData(part BlockImagePart) ([]byte, error) {
	entry, err := imageRuntimeEntryForPart(part)
	if err != nil {
		return nil, err
	}
	return entry.raw(part)
}

func imagePartLineHit(part BlockImagePart, lineInBlock int) bool {
	if part.RenderStartLine < 0 || part.RenderRows <= 0 {
		return false
	}
	return lineInBlock >= part.RenderStartLine && lineInBlock < part.RenderStartLine+part.RenderRows
}

func imagePartBodyLeftColumn() int {
	return UserCardStyle.GetMarginLeft() + UserCardStyle.GetBorderLeftSize() + UserCardStyle.GetPaddingLeft() + imagePlaceholderMargin
}

func (b *Block) imagePartAtLine(lineInBlock, width int) (BlockImagePart, bool) {
	if b == nil || b.Type != BlockUser || len(b.ImageParts) == 0 {
		return BlockImagePart{}, false
	}
	_ = b.Render(width, "")
	for idx, part := range b.ImageParts {
		if !imagePartLineHit(part, lineInBlock) {
			continue
		}
		part.Index = idx
		return part, true
	}
	return BlockImagePart{}, false
}

func (b *Block) imagePartAtPoint(lineInBlock, viewportCol, width int) (BlockImagePart, bool) {
	if b == nil || b.Type != BlockUser || len(b.ImageParts) == 0 {
		return BlockImagePart{}, false
	}
	_ = b.Render(width, "")
	bodyLeft := imagePartBodyLeftColumn()
	for idx, part := range b.ImageParts {
		if !imagePartLineHit(part, lineInBlock) || part.RenderCols <= 0 {
			continue
		}
		if viewportCol < bodyLeft || viewportCol >= bodyLeft+part.RenderCols {
			continue
		}
		part.Index = idx
		return part, true
	}
	return BlockImagePart{}, false
}

func (b *Block) firstImagePart(width int) (BlockImagePart, bool) {
	if b == nil || b.Type != BlockUser || len(b.ImageParts) == 0 {
		return BlockImagePart{}, false
	}
	_ = b.Render(width, "")
	for idx, part := range b.ImageParts {
		if part.RenderCols > 0 && part.RenderRows > 0 {
			part.Index = idx
			return part, true
		}
	}
	return BlockImagePart{}, false
}

func openBlockImageCmd(imageOpenDir string, block *Block, part BlockImagePart, caps TerminalImageCapabilities) tea.Cmd {
	if caps.Backend != ImageBackendNone && caps.SupportsFullscreen {
		blockID := -1
		if block != nil {
			blockID = block.ID
		}
		return func() tea.Msg { return openImageViewerMsg{blockID: blockID, imageIndex: part.Index} }
	}
	return func() tea.Msg {
		path, err := ensureImageOpenPath(imageOpenDir, part)
		if err == nil {
			err = openImageExternally(path)
		}
		return openImageResultMsg{err: err}
	}
}

func ensureImageOpenPath(imageOpenDir string, part BlockImagePart) (string, error) {
	if path := strings.TrimSpace(part.ImagePath); path != "" {
		if _, err := os.Stat(path); err == nil {
			abs, absErr := filepath.Abs(path)
			if absErr == nil {
				return abs, nil
			}
			return path, nil
		}
	}
	data, err := thumbnailPreviewData(part)
	if err != nil {
		return "", fmt.Errorf("image file unavailable: %w", err)
	}
	root := strings.TrimSpace(imageOpenDir)
	if root == "" {
		root, err = os.MkdirTemp("", "chord-image-open-")
		if err != nil {
			return "", fmt.Errorf("create temporary image root: %w", err)
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create image temp dir: %w", err)
	}
	pattern := sanitizeImageFileName(part.FileName)
	if pattern == "" {
		pattern = "image"
	}
	ext := filepath.Ext(pattern)
	base := strings.TrimSuffix(pattern, ext)
	if ext == "" {
		ext = attachmentExtForMimeType(part.MimeType)
	}
	f, err := os.CreateTemp(root, base+"-*"+ext)
	if err != nil {
		return "", fmt.Errorf("create temporary image file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", fmt.Errorf("write temporary image file: %w", err)
	}
	return f.Name(), nil
}

func sanitizeImageFileName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return ""
	}
	name = strings.ReplaceAll(name, string(filepath.Separator), "-")
	name = strings.Map(func(r rune) rune {
		switch r {
		case 0, '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		default:
			return r
		}
	}, name)
	name = strings.Trim(name, " .")
	return name
}

func openImageExternally(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("image file unavailable")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = execExternalImageCommand("open", path)
	case "windows":
		cmd = execExternalImageCommand("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = execExternalImageCommand("xdg-open", path)
	}
	if err := suppressExternalCommandOutput(cmd).Start(); err != nil {
		return fmt.Errorf("open image externally: %w", err)
	}
	return nil
}
