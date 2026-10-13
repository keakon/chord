// Package filedelivery validates local file identities independently of tools and UI.
package filedelivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/message"
)

const (
	WorkspaceHashLimit int64 = 8 << 20
	ArtifactHashLimit  int64 = 64 << 20
	BatchReadLimit     int64 = 256 << 20
)

func WorkspaceLocator(base, path string) (message.FileLocator, error) {
	if !validPath(path) || !filepath.IsAbs(base) {
		return message.FileLocator{}, errors.New("absolute working directory and valid file path required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return message.FileLocator{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return message.FileLocator{}, err
	}
	if !info.Mode().IsRegular() {
		return message.FileLocator{}, errors.New("target is not a regular file")
	}
	return message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Root: filepath.Clean(base), Path: path}}, nil
}
func validPath(path string) bool {
	return path != "" && len(path) <= 8192 && !strings.ContainsRune(path, 0) && utf8.ValidString(path)
}
func ArtifactLocator(path string) (message.FileLocator, error) {
	if !validPath(path) || filepath.IsAbs(path) {
		return message.FileLocator{}, errors.New("invalid session-relative artifact path")
	}
	path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if !strings.HasPrefix(path, "artifacts/") && !strings.HasPrefix(path, "images/") {
		return message.FileLocator{}, errors.New("artifact must be under artifacts/ or images/")
	}
	return message.FileLocator{Kind: message.FileLocatorArtifact, Artifact: &message.ArtifactFileLocator{RelPath: path}}, nil
}

// Open returns an owned handle. Artifacts are opened relative to a contained OS root.
func Open(locator message.FileLocator, sessionDir string) (*os.File, string, error) {
	var f *os.File
	var path string
	var err error
	switch {
	case locator.Kind == message.FileLocatorWorkspace && locator.Workspace != nil && locator.Artifact == nil:
		path = locator.Workspace.Path
		if !validPath(path) || !filepath.IsAbs(path) || !filepath.IsAbs(locator.Workspace.Root) {
			return nil, "", errors.New("invalid workspace locator")
		}
		resolved, e := filepath.EvalSymlinks(path)
		if e != nil {
			return nil, "", e
		}
		if resolved != path {
			return nil, "", errors.New("bound file target changed")
		}
		info, e := os.Stat(path)
		if e != nil {
			return nil, "", e
		}
		if !info.Mode().IsRegular() {
			return nil, "", errors.New("target is not a regular file")
		}
		f, err = openRegularCandidate(path)
		if err == nil {
			opened, e := f.Stat()
			if e != nil || !os.SameFile(info, opened) {
				f.Close()
				return nil, "", errors.New("file changed during open")
			}
		}
	case locator.Kind == message.FileLocatorArtifact && locator.Artifact != nil && locator.Workspace == nil:
		normalized, e := ArtifactLocator(locator.Artifact.RelPath)
		if e != nil {
			return nil, "", e
		}
		if normalized.Artifact.RelPath != locator.Artifact.RelPath || sessionDir == "" {
			return nil, "", errors.New("invalid artifact binding")
		}
		pieces := strings.SplitN(locator.Artifact.RelPath, "/", 2)
		rootPath := filepath.Join(sessionDir, pieces[0])
		info, e := os.Lstat(rootPath)
		if e != nil {
			return nil, "", e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, "", errors.New("artifact root must be a real directory")
		}
		root, e := os.OpenRoot(rootPath)
		if e != nil {
			return nil, "", e
		}
		defer root.Close()
		info, e = root.Stat(filepath.FromSlash(pieces[1]))
		if e != nil {
			return nil, "", e
		}
		if !info.Mode().IsRegular() {
			return nil, "", errors.New("artifact is not a regular file")
		}
		f, err = openRootCandidate(root, filepath.FromSlash(pieces[1]))
		if err == nil {
			opened, e := f.Stat()
			if e != nil || !os.SameFile(info, opened) {
				f.Close()
				return nil, "", errors.New("artifact changed during open")
			}
		}
		path = filepath.Join(sessionDir, filepath.FromSlash(locator.Artifact.RelPath))
	default:
		return nil, "", errors.New("invalid file locator")
	}
	if err != nil {
		return nil, "", err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, "", errors.New("target is not a regular file")
	}
	return f, path, nil
}

// Inspect hashes bounded files and rejects mutations observed during the read.
func Inspect(ctx context.Context, locator message.FileLocator, sessionDir string, budget *int64) (message.DeliveredFile, error) {
	var result message.DeliveredFile
	f, path, err := Open(locator, sessionDir)
	if err != nil {
		return result, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return result, err
	}
	limit := WorkspaceHashLimit
	artifact := locator.Kind == message.FileLocatorArtifact
	if artifact {
		limit = ArtifactHashLimit
		if before.Size() > limit {
			return result, errors.New("artifact exceeds 64 MiB; deliver as a current workspace file")
		}
	}
	hashContent := before.Size() <= limit
	readLimit := int64(512)
	if hashContent {
		readLimit = before.Size() + 1
	}
	h := sha256.New()
	var sniff []byte
	buf := make([]byte, 32<<10)
	var total int64
	for total < readLimit {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		n, e := f.Read(buf[:min(int64(len(buf)), readLimit-total)])
		total += int64(n)
		if budget != nil {
			*budget -= int64(n)
			if *budget < 0 {
				return result, errors.New("delivery batch exceeds read budget")
			}
		}
		if len(sniff) < 512 {
			sniff = append(sniff, buf[:min(n, 512-len(sniff))]...)
		}
		if hashContent {
			h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return result, e
		}
	}
	after, err := f.Stat()
	if err != nil {
		return result, err
	}
	if before.Size() != after.Size() || before.ModTime() != after.ModTime() || (hashContent && total != before.Size()) {
		return result, errors.New("file is changing; retry after writing finishes")
	}
	result = message.DeliveredFile{Locator: locator, DisplayName: filepath.Base(path), MIME: DetectMIME(path, sniff), SizeBytes: before.Size(), ModTimeUnixNano: before.ModTime().UnixNano()}
	if hashContent {
		result.SHA256 = hex.EncodeToString(h.Sum(nil))
	}
	return result, nil
}
func DetectMIME(path string, data []byte) string {
	sniff := http.DetectContentType(data)
	if strings.HasPrefix(sniff, "image/") || sniff == "application/pdf" {
		return sniff
	}
	if len(data) == 0 {
		return "text/plain"
	}
	if len(data) == 512 && !utf8.Valid(data) {
		for trim := 1; trim < utf8.UTFMax; trim++ {
			if utf8.Valid(data[:len(data)-trim]) {
				data = data[:len(data)-trim]
				break
			}
		}
	}
	if strings.ContainsRune(string(data), 0) || !utf8.Valid(data) {
		return "application/octet-stream"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	}
	if ext := mime.TypeByExtension(filepath.Ext(path)); ext != "" {
		return ext
	}
	return sniff
}

func Verify(ctx context.Context, declared message.DeliveredFile, sessionDir string) (message.DeliveredFile, error) {
	current, err := Inspect(ctx, declared.Locator, sessionDir, nil)
	if err != nil {
		return current, err
	}
	if declared.Locator.Kind == message.FileLocatorArtifact && (declared.SHA256 == "" || current.SHA256 != declared.SHA256 || current.SizeBytes != declared.SizeBytes) {
		return current, fmt.Errorf("declared artifact version is unavailable")
	}
	return current, nil
}
