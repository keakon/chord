package tui

import (
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/pathutil"
)

func deliveryDisplayPaths(files []message.DeliveredFile, workingDir string) (string, []string) {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = deliveryDisplayPath(f, workingDir)
	}
	if len(paths) < 2 {
		return "", paths
	}
	root := deliveryCommonRoot(paths)
	if root == "" {
		return "", paths
	}
	relative := make([]string, len(paths))
	for i, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", paths
		}
		relative[i] = rel
	}
	return root, relative
}

func deliveryCommonRoot(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	root := filepath.Dir(paths[0])
	for _, path := range paths[1:] {
		dir := filepath.Dir(path)
		for {
			if _, ok := pathutil.RelToBase(dir, root); ok {
				break
			}
			parent := filepath.Dir(root)
			if parent == root {
				return ""
			}
			root = parent
		}
	}
	if root == "." || root == ".." || strings.HasPrefix(root, ".."+string(filepath.Separator)) || filepath.Dir(root) == root {
		return ""
	}
	return root
}

func deliveryRootLabel(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Base(root)
}

func deliveryPathParts(path string) (string, string) {
	dir, name := filepath.Split(path)
	dir = strings.TrimSuffix(dir, string(filepath.Separator))
	dir = filepath.ToSlash(dir)
	if dir == "." {
		dir = ""
	}
	return dir, name
}

func deliveryCardPathLine(path, mime string, width int) string {
	suffix := ""
	if label := deliveryTypeLabel(mime); label != "" {
		suffix = " · " + label
		if len(suffix) >= width {
			suffix = ""
		} else {
			width -= len(suffix)
		}
	}
	path = truncateToolHeaderMiddle(sanitizeToolDisplayText(path), width)
	dir, name := deliveryPathParts(path)
	var b strings.Builder
	if dir != "" {
		b.WriteString(currentStyles.DimStyle.Render(dir + "/"))
	}
	b.WriteString(currentStyles.ToolFileNameStyle.Render(fileLabel(name)))
	if suffix != "" {
		b.WriteString(currentStyles.DimStyle.Render(suffix))
	}
	return b.String()
}

func deliveryTypeLabel(mime string) string {
	mediaType, _, _ := strings.Cut(mime, ";")
	mediaType = strings.TrimSpace(mediaType)
	if mediaType == "" || strings.HasPrefix(mediaType, "text/") {
		return ""
	}
	return mediaType
}
