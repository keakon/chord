package tools

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/privatefs"
)

// ArtifactRef is a typed reference to a runtime-managed artifact.
// Paths are session-relative and must stay within the active session directory.
type ArtifactRef struct {
	ID             string `json:"id,omitempty"`
	Type           string `json:"type,omitempty"`
	RelPath        string `json:"rel_path,omitempty"`
	Path           string `json:"path,omitempty"`
	Description    string `json:"description,omitempty"`
	MimeType       string `json:"mime_type,omitempty"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
	CreatedByTask  string `json:"created_by_task,omitempty"`
	CreatedByAgent string `json:"created_by_agent,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
}

type ResultRef struct {
	ID         string `json:"id"`
	ResultType string `json:"result_type"`
	RelPath    string `json:"rel_path"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
}

func NormalizeArtifactRef(ref ArtifactRef) ArtifactRef {
	ref.ID = strings.TrimSpace(ref.ID)
	ref.Type = strings.TrimSpace(ref.Type)
	ref.RelPath = strings.TrimSpace(ref.RelPath)
	ref.Path = strings.TrimSpace(ref.Path)
	ref.Description = strings.TrimSpace(ref.Description)
	ref.MimeType = strings.TrimSpace(ref.MimeType)
	ref.CreatedByTask = strings.TrimSpace(ref.CreatedByTask)
	ref.CreatedByAgent = strings.TrimSpace(ref.CreatedByAgent)
	ref.SHA256 = strings.ToLower(strings.TrimSpace(ref.SHA256))
	if ref.RelPath == "" && ref.Path != "" {
		ref.RelPath = ref.Path
	}
	if ref.Path == "" && ref.RelPath != "" {
		ref.Path = ref.RelPath
	}
	return ref
}

func NormalizeArtifactRefs(refs []ArtifactRef) []ArtifactRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]ArtifactRef, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		ref = NormalizeArtifactRef(ref)
		if ref.ID == "" && ref.RelPath == "" && ref.Description == "" {
			continue
		}
		key := ref.ID
		if key == "" {
			key = ref.RelPath
		}
		if key == "" {
			key = ref.Description
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func writeArtifactString(f *os.File, content string) error {
	n, err := f.WriteString(content)
	if err != nil {
		return err
	}
	if n != len(content) {
		return io.ErrShortWrite
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ArtifactSHA256(path string) (string, error) {
	return fileSHA256(path)
}

// SaveArtifactTool writes a runtime artifact under the active session artifacts dir.
type SaveArtifactTool struct{}

type saveArtifactArgs struct {
	Filename    string `json:"filename"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content"`
	MimeType    string `json:"mime_type,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

func (SaveArtifactTool) Name() string { return NameSaveArtifact }

func (SaveArtifactTool) Description() string {
	return "Save or update a runtime artifact for optional downstream worker handoff, such as a research report, task graph, review report, or verification log. This writes only under the current session's artifacts directory and does not modify project files. Multiple artifacts are allowed. Use mode=create for a new artifact, mode=append to add to an existing artifact, and mode=overwrite to replace an existing artifact intentionally."
}

func (SaveArtifactTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"filename": map[string]any{
				"type":        "string",
				"description": "Artifact filename, for example research.md. Path separators are stripped.",
			},
			"type": map[string]any{
				"type":        "string",
				"description": "Artifact type, for example research_report, task_graph, review_report, or verification_log. Defaults to handoff_note.",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Short description of the artifact.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write. For append mode, this content is appended as a new block.",
			},
			"mime_type": map[string]any{
				"type":        "string",
				"description": "Optional MIME type, defaults to text/markdown.",
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "Write mode: create (default, fail if file exists), append (append content), or overwrite (replace existing content).",
				"enum":        []string{"create", "append", "overwrite"},
			},
		},
		"required":             []string{"filename", "content"},
		"additionalProperties": false,
	}
}

func (SaveArtifactTool) IsReadOnly() bool { return false }

func (SaveArtifactTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args saveArtifactArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	sessionDir := SessionDirFromContext(ctx)
	if strings.TrimSpace(sessionDir) == "" {
		return "", fmt.Errorf("session directory is unavailable")
	}
	content := strings.TrimSpace(args.Content)
	if content == "" {
		return "", fmt.Errorf("content is required")
	}
	filename := sanitizeArtifactFilename(args.Filename)
	if filename == "" {
		return "", fmt.Errorf("filename is required")
	}
	agentID := sanitizeArtifactPathComponent(AgentIDFromContext(ctx))
	if agentID == "" {
		agentID = "agent"
	}
	taskID := sanitizeArtifactPathComponent(TaskIDFromContext(ctx))
	if taskID == "" {
		taskID = "task"
	}
	artifactType := sanitizeArtifactPathComponent(args.Type)
	if artifactType == "" {
		artifactType = "handoff_note"
	}
	dir := filepath.Join(sessionDir, "artifacts", "subagents", agentID, taskID)
	abs := filepath.Join(dir, filename)
	mode := strings.TrimSpace(strings.ToLower(args.Mode))
	if mode == "" {
		mode = "create"
	}
	var writeErr error
	switch mode {
	case "create":
		f, err := privatefs.OpenFile(sessionDir, abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			if os.IsExist(err) {
				return "", fmt.Errorf("artifact already exists; use mode=append or mode=overwrite to update it")
			}
			return "", err
		}
		writeErr = writeArtifactString(f, content+"\n")
		closeErr := f.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
	case "append":
		f, err := privatefs.OpenFile(sessionDir, abs, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
		if err != nil {
			return "", err
		}
		if info, err := f.Stat(); err == nil && info.Size() > 0 {
			writeErr = writeArtifactString(f, "\n")
		}
		if writeErr == nil {
			writeErr = writeArtifactString(f, content+"\n")
		}
		closeErr := f.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
	case "overwrite":
		f, err := privatefs.OpenFile(sessionDir, abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return "", err
		}
		writeErr = writeArtifactString(f, content+"\n")
		closeErr := f.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
	default:
		return "", fmt.Errorf("invalid mode %q: expected create, append, or overwrite", args.Mode)
	}
	if writeErr != nil {
		return "", writeErr
	}
	rel, err := filepath.Rel(sessionDir, abs)
	if err != nil {
		return "", err
	}
	info, _ := os.Stat(abs)
	mimeType := strings.TrimSpace(args.MimeType)
	if mimeType == "" {
		mimeType = "text/markdown"
	}
	ref := ArtifactRef{
		ID:             strings.TrimSuffix(filename, filepath.Ext(filename)),
		Type:           artifactType,
		RelPath:        filepath.ToSlash(rel),
		Path:           filepath.ToSlash(rel),
		Description:    strings.TrimSpace(args.Description),
		MimeType:       mimeType,
		CreatedByTask:  TaskIDFromContext(ctx),
		CreatedByAgent: AgentIDFromContext(ctx),
	}
	if info != nil {
		ref.SizeBytes = info.Size()
	}
	ref.SHA256, err = fileSHA256(abs)
	if err != nil {
		return "", fmt.Errorf("hash artifact: %w", err)
	}
	out, err := json.Marshal(NormalizeArtifactRef(ref))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func sanitizeArtifactPathComponent(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func sanitizeArtifactFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = filepath.Base(filepath.FromSlash(s))
	s = strings.TrimSpace(s)
	if s == "." || s == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "._-")
}

// ReadArtifactTool reads only artifacts under the active session artifacts dir.
type ReadArtifactTool struct{}

type readArtifactArgs struct {
	ID             string `json:"id,omitempty"`
	Path           string `json:"path,omitempty"`
	RelPath        string `json:"rel_path,omitempty"`
	Offset         *int   `json:"offset,omitempty"`
	Limit          *int   `json:"limit,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}

func (ReadArtifactTool) Name() string { return NameReadArtifact }

func (ReadArtifactTool) Description() string {
	return "Read a runtime artifact by session-relative path with bounded line paging. offset is a 0-based line offset and limit defaults to 2000 lines. The result reports the returned range, total lines, and SHA-256. Supply expected_sha256 to reject content changed since an ArtifactRef snapshot was created."
}

func (ReadArtifactTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Session-relative artifact path, for example artifacts/subagents/agent-1/report.md.",
			},
			"rel_path": map[string]any{
				"type":        "string",
				"description": "Alias for path.",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Optional artifact id for logs; path or rel_path is still required.",
			},
			"offset":          map[string]any{"type": "integer", "minimum": 0, "description": "0-based line offset. Defaults to 0."},
			"limit":           map[string]any{"type": "integer", "minimum": 1, "maximum": MaxOutputLines, "description": "Maximum lines to return. Defaults to 2000."},
			"expected_sha256": map[string]any{"type": "string", "description": "Optional lowercase SHA-256 digest expected for the complete artifact."},
		},
		"additionalProperties": false,
		"anyOf": []map[string]any{
			{"required": []string{"path"}},
			{"required": []string{"rel_path"}},
		},
	}
}

func (ReadArtifactTool) IsReadOnly() bool { return true }

func (ReadArtifactTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (ReadArtifactTool) CanRenderBeforeToolUseEnd(json.RawMessage) bool { return true }

func (ReadArtifactTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args readArtifactArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	sessionDir := SessionDirFromContext(ctx)
	if strings.TrimSpace(sessionDir) == "" {
		return "", fmt.Errorf("session directory is unavailable")
	}
	rel := strings.TrimSpace(args.Path)
	if rel == "" {
		rel = strings.TrimSpace(args.RelPath)
	}
	abs, err := ResolveSessionArtifactPath(sessionDir, rel)
	if err != nil {
		return "", err
	}
	result, err := readArtifactPage(abs, args.Offset, args.Limit)
	if err != nil {
		return "", err
	}
	expected := strings.ToLower(strings.TrimSpace(args.ExpectedSHA256))
	if expected != "" {
		if len(expected) != sha256.Size*2 {
			return "", fmt.Errorf("expected_sha256 must be a 64-character hexadecimal SHA-256 digest")
		}
		if _, err := hex.DecodeString(expected); err != nil {
			return "", fmt.Errorf("expected_sha256 must be hexadecimal")
		}
		if expected != result.SHA256 {
			return "", fmt.Errorf("artifact digest mismatch: expected %s, got %s", expected, result.SHA256)
		}
	}
	return result.render(), nil
}

type artifactReadResult struct {
	Lines      []string
	StartLine  int
	EndLine    int
	TotalLines int
	SHA256     string
	Truncated  bool
}

func (r artifactReadResult) render() string {
	rangeText := "none"
	if r.StartLine > 0 && r.EndLine >= r.StartLine {
		rangeText = fmt.Sprintf("%d-%d", r.StartLine, r.EndLine)
	}
	header := fmt.Sprintf("ARTIFACT_RESULT lines=%s total=%d sha256=%s", rangeText, r.TotalLines, r.SHA256)
	if r.Truncated {
		header += " truncated=budget"
	}
	return buildReadContent(header, r.Lines)
}

func readArtifactPage(path string, offsetArg, limitArg *int) (artifactReadResult, error) {
	offset := 0
	if offsetArg != nil {
		if *offsetArg < 0 {
			return artifactReadResult{}, fmt.Errorf("offset must be non-negative")
		}
		offset = *offsetArg
	}
	limit := MaxOutputLines
	if limitArg != nil {
		if *limitArg <= 0 || *limitArg > MaxOutputLines {
			return artifactReadResult{}, fmt.Errorf("limit must be between 1 and %d", MaxOutputLines)
		}
		limit = *limitArg
	}
	f, err := os.Open(path)
	if err != nil {
		return artifactReadResult{}, err
	}
	defer f.Close()
	h := sha256.New()
	reader := bufio.NewReaderSize(f, 32*1024)
	result := artifactReadResult{}
	lineIndex := 0
	line := make([]byte, 0, 1024)
	outputBytes := 0
	lineTruncated := false
	for {
		fragment, readErr := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			_, _ = h.Write(fragment)
			if lineIndex >= offset && lineIndex < offset+limit {
				contentFragment := fragment
				if readErr != bufio.ErrBufferFull {
					contentFragment = bytesTrimLineEnding(contentFragment)
				}
				// Reserve one byte for buildReadContent's trailing newline so an
				// oversized requested line still returns a bounded prefix instead
				// of being dropped in favor of a later line.
				remaining := MaxOutputBytes - outputBytes - len(line) - 1
				if remaining > 0 {
					line = append(line, contentFragment[:min(len(contentFragment), remaining)]...)
				}
				if len(contentFragment) > max(remaining, 0) {
					lineTruncated = true
				}
			}
		}
		lineDone := readErr != bufio.ErrBufferFull
		if lineDone && (len(fragment) > 0 || len(line) > 0) {
			result.TotalLines++
			if lineIndex >= offset && lineIndex < offset+limit {
				candidate := truncateStringToValidUTF8Prefix(string(line), len(line))
				candidateBytes := len(candidate) + 1
				if outputBytes+candidateBytes <= MaxOutputBytes {
					if len(result.Lines) == 0 {
						result.StartLine = lineIndex + 1
					}
					result.Lines = append(result.Lines, candidate)
					outputBytes += candidateBytes
					result.EndLine = lineIndex + 1
				} else {
					result.Truncated = true
				}
				result.Truncated = result.Truncated || lineTruncated
			}
			lineIndex++
			line = line[:0]
			lineTruncated = false
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != bufio.ErrBufferFull {
			return artifactReadResult{}, readErr
		}
	}
	result.SHA256 = hex.EncodeToString(h.Sum(nil))
	if offset > result.TotalLines {
		return artifactReadResult{}, readOffsetPastEndError(offset, result.TotalLines, limitArg)
	}
	return result, nil
}

func bytesTrimLineEnding(line []byte) []byte {
	text := strings.TrimSuffix(string(line), "\n")
	text = strings.TrimSuffix(text, "\r")
	return []byte(text)
}

func ResolveSessionArtifactPath(sessionDir, relPath string) (string, error) {
	sessionDir = strings.TrimSpace(sessionDir)
	relPath = strings.TrimSpace(relPath)
	if sessionDir == "" {
		return "", fmt.Errorf("session directory is required")
	}
	if relPath == "" {
		return "", fmt.Errorf("artifact path is required")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("artifact path must be session-relative")
	}
	relPath = filepath.Clean(filepath.FromSlash(relPath))
	if relPath == "." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) || relPath == ".." {
		return "", fmt.Errorf("artifact path escapes session directory")
	}
	parts := strings.Split(relPath, string(filepath.Separator))
	if len(parts) == 0 || parts[0] != "artifacts" {
		return "", fmt.Errorf("artifact path must be under artifacts/")
	}
	sessionAbs, err := filepath.Abs(sessionDir)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(sessionAbs, relPath)
	abs, err = filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	artifactsRoot := filepath.Join(sessionAbs, "artifacts")
	if abs != artifactsRoot && !strings.HasPrefix(abs, artifactsRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path escapes artifacts directory")
	}
	resolvedRoot, err := filepath.EvalSymlinks(artifactsRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		resolvedRoot = artifactsRoot
	}
	if rootInfo, lstatErr := os.Lstat(artifactsRoot); lstatErr == nil && rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("artifacts directory must not be a symbolic link")
	} else if lstatErr != nil && !os.IsNotExist(lstatErr) {
		return "", lstatErr
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return abs, nil
		}
		return "", err
	}
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path escapes artifacts directory through symbolic link")
	}
	abs = resolved
	return abs, nil
}

func ValidateArtifactRefs(sessionDir string, refs []ArtifactRef) ([]ArtifactRef, error) {
	refs = NormalizeArtifactRefs(refs)
	for i := range refs {
		ref := refs[i]
		path := ref.RelPath
		if path == "" {
			path = ref.Path
		}
		abs, err := ResolveSessionArtifactPath(sessionDir, path)
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", i+1, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("artifact %d: %w", i+1, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("artifact %d is not a regular file", i+1)
		}
		if ref.SizeBytes != 0 && ref.SizeBytes != info.Size() {
			return nil, fmt.Errorf("artifact %d size mismatch: expected %d, got %d", i+1, ref.SizeBytes, info.Size())
		}
		if ref.SHA256 != "" {
			digest, err := fileSHA256(abs)
			if err != nil {
				return nil, fmt.Errorf("artifact %d hash: %w", i+1, err)
			}
			if digest != ref.SHA256 {
				return nil, fmt.Errorf("artifact %d digest mismatch: expected %s, got %s", i+1, ref.SHA256, digest)
			}
		}
		refs[i].RelPath = filepath.ToSlash(path)
		refs[i].Path = ""
	}
	return refs, nil
}
