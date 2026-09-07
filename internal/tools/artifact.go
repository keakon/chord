package tools

import (
	"bufio"
	"bytes"
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

const (
	maxImmutableResultBytes = 10 * 1024 * 1024
	MaxInlineResultBytes    = 32 * 1024
)

func canonicalResultObject(resultType string, raw json.RawMessage, maxBytes int) (string, []byte, error) {
	resultType = strings.TrimSpace(resultType)
	if resultType == "" {
		return "", nil, fmt.Errorf("result_type is required")
	}
	if len(resultType) > 256 {
		return "", nil, fmt.Errorf("result_type exceeds maximum size 256 bytes")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", nil, fmt.Errorf("result must be a JSON object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return "", nil, fmt.Errorf("result must be a JSON object")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return "", nil, fmt.Errorf("canonicalize result: %w", err)
	}
	canonical = append(canonical, '\n')
	if maxBytes > 0 && len(canonical) > maxBytes {
		return "", nil, fmt.Errorf("result exceeds maximum size %d bytes", maxBytes)
	}
	return resultType, canonical, nil
}

func resultRefID(resultType, digest string) string {
	typeSum := sha256.Sum256([]byte(resultType + "\x00" + digest))
	return "sha256-" + hex.EncodeToString(typeSum[:])
}

func SaveImmutableResult(sessionDir, resultType string, raw json.RawMessage) (ResultRef, json.RawMessage, error) {
	resultType, canonical, err := canonicalResultObject(resultType, raw, maxImmutableResultBytes)
	if err != nil {
		return ResultRef{}, nil, err
	}
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return ResultRef{}, nil, fmt.Errorf("session directory is unavailable")
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	id := resultRefID(resultType, digest)
	relPath := filepath.ToSlash(filepath.Join("artifacts", "results", id+".json"))
	abs := filepath.Join(sessionDir, filepath.FromSlash(relPath))
	f, err := privatefs.OpenFile(sessionDir, abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		if !os.IsExist(err) {
			return ResultRef{}, nil, err
		}
		existingDigest, hashErr := fileSHA256(abs)
		if hashErr != nil || existingDigest != digest {
			return ResultRef{}, nil, fmt.Errorf("immutable result path collision for %s", id)
		}
	} else {
		written, err := f.Write(canonical)
		if err == nil && written != len(canonical) {
			err = io.ErrShortWrite
		}
		if err != nil {
			_ = f.Close()
			_ = os.Remove(abs)
			return ResultRef{}, nil, err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(abs)
			return ResultRef{}, nil, err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(abs)
			return ResultRef{}, nil, err
		}
	}
	ref := ResultRef{ID: id, ResultType: resultType, RelPath: relPath, SHA256: digest, SizeBytes: int64(len(canonical))}
	inline := append(json.RawMessage(nil), canonical[:len(canonical)-1]...)
	return ref, inline, nil
}

func ValidateResultRef(sessionDir string, ref ResultRef, expectedType string) (ResultRef, error) {
	ref.ID = strings.TrimSpace(ref.ID)
	ref.ResultType = strings.TrimSpace(ref.ResultType)
	ref.RelPath = filepath.ToSlash(strings.TrimSpace(ref.RelPath))
	ref.SHA256 = strings.ToLower(strings.TrimSpace(ref.SHA256))
	if ref.ID == "" || ref.ResultType == "" || ref.RelPath == "" || ref.SHA256 == "" || ref.SizeBytes <= 0 {
		return ResultRef{}, fmt.Errorf("result_ref requires id, result_type, rel_path, sha256, and positive size_bytes")
	}
	if expectedType = strings.TrimSpace(expectedType); expectedType != "" && ref.ResultType != expectedType {
		return ResultRef{}, fmt.Errorf("result_ref result_type %q does not match %q", ref.ResultType, expectedType)
	}
	if len(ref.SHA256) != sha256.Size*2 {
		return ResultRef{}, fmt.Errorf("result_ref sha256 must be a 64-character digest")
	}
	if _, err := hex.DecodeString(ref.SHA256); err != nil {
		return ResultRef{}, fmt.Errorf("result_ref sha256 must be hexadecimal")
	}
	if ref.ID != resultRefID(ref.ResultType, ref.SHA256) {
		return ResultRef{}, fmt.Errorf("result_ref id does not match result_type and sha256")
	}
	expectedRelPath := filepath.ToSlash(filepath.Join("artifacts", "results", ref.ID+".json"))
	if ref.RelPath != expectedRelPath {
		return ResultRef{}, fmt.Errorf("result_ref rel_path does not match id")
	}
	validated, err := ValidateArtifactRefs(sessionDir, []ArtifactRef{{RelPath: ref.RelPath, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}})
	if err != nil {
		return ResultRef{}, err
	}
	if len(validated) != 1 {
		return ResultRef{}, fmt.Errorf("result_ref validation failed")
	}
	return ref, nil
}

// SaveArtifactTool writes a runtime artifact under the active session artifacts dir.
type SaveArtifactTool struct{}

type saveArtifactArgs struct {
	Filename    string          `json:"filename"`
	Type        string          `json:"type,omitempty"`
	Description string          `json:"description,omitempty"`
	Content     string          `json:"content"`
	MimeType    string          `json:"mime_type,omitempty"`
	Mode        string          `json:"mode,omitempty"`
	ResultType  string          `json:"result_type,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

func (SaveArtifactTool) Name() string { return NameSaveArtifact }

func (SaveArtifactTool) Description() string {
	return "Save or update a runtime artifact for optional downstream worker handoff, such as a research report, task graph, review report, or verification log. This writes only under the current session's artifacts directory and does not modify project files. Multiple artifacts are allowed. Use mode=create for a new artifact, mode=append to add to an existing artifact, and mode=overwrite to replace an existing artifact intentionally. Alternatively, provide result_type with a JSON-object result (instead of filename, content, and mode) to store it as an immutable content-addressed result under artifacts/results/; the returned ResultRef can be passed directly as complete's result_ref."
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
			"result_type": map[string]any{
				"type":        "string",
				"description": "Application-defined result media type. Must be given together with result and without filename, content, or mode.",
			},
			"result": map[string]any{
				"type":        "object",
				"description": "JSON object to persist without runtime interpretation. Must be given together with result_type and without filename, content, or mode.",
			},
		},
		"anyOf": []map[string]any{
			{"required": []string{"filename", "content"}},
			{"required": []string{"result_type", "result"}},
		},
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
	rawResult := bytes.TrimSpace(args.Result)
	// A present-but-invalid result (including explicit JSON null) counts as
	// provided: it falls through to canonicalResultObject's object check so
	// the caller sees "result must be a JSON object" instead of a misleading
	// "result is required".
	resultProvided := len(rawResult) > 0
	if args.ResultType != "" || resultProvided {
		if strings.TrimSpace(args.Filename) != "" || content != "" || strings.TrimSpace(args.Mode) != "" {
			return "", fmt.Errorf("result_type/result cannot be combined with filename, content, or mode")
		}
		if strings.TrimSpace(args.ResultType) == "" {
			return "", fmt.Errorf("result_type is required when result is provided")
		}
		if !resultProvided {
			return "", fmt.Errorf("result is required when result_type is provided")
		}
		ref, _, err := SaveImmutableResult(sessionDir, args.ResultType, args.Result)
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(ref)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
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
	Path           string `json:"path,omitempty"`
	RelPath        string `json:"rel_path,omitempty"`
	Offset         *int   `json:"offset,omitempty"`
	Limit          *int   `json:"limit,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
}

func (ReadArtifactTool) Name() string { return NameReadArtifact }

func (ReadArtifactTool) Description() string {
	return "Read a runtime artifact by session-relative path with bounded line paging. offset is a 1-based line number (1 = the first line) and limit defaults to 2000 lines. The result reports the returned range, total lines, and SHA-256. Supply expected_sha256 to reject content changed since an ArtifactRef snapshot was created."
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
			"offset":          map[string]any{"type": "integer", "minimum": 0, "description": "1-based line number to start reading from (1 = the first line); 0 or omitted means the first line. Defaults to 1."},
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
	// The public offset is a 1-based start line (1 = the first line); 0 or
	// absent also mean the first line, matching Read. Internally this maps back
	// to a 0-based slice index.
	startLine := 1
	if offsetArg != nil {
		if *offsetArg < 0 {
			return artifactReadResult{}, fmt.Errorf("offset must be non-negative")
		}
		if *offsetArg > 0 {
			startLine = *offsetArg
		}
	}
	offset := startLine - 1
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
		return artifactReadResult{}, readOffsetPastEndError(startLine, result.TotalLines, limitArg)
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
