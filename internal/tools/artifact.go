package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
				"description": "Artifact type, for example research_report, task_graph, review_report, or verification_log.",
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	abs := filepath.Join(dir, filename)
	mode := strings.TrimSpace(strings.ToLower(args.Mode))
	if mode == "" {
		mode = "create"
	}
	var writeErr error
	switch mode {
	case "create":
		f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				return "", fmt.Errorf("artifact already exists; use mode=append or mode=overwrite to update it")
			}
			return "", err
		}
		_, writeErr = f.WriteString(content + "\n")
		closeErr := f.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
	case "append":
		f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return "", err
		}
		if info, err := f.Stat(); err == nil && info.Size() > 0 {
			_, writeErr = f.WriteString("\n")
		}
		if writeErr == nil {
			_, writeErr = f.WriteString(content + "\n")
		}
		closeErr := f.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
	case "overwrite":
		writeErr = os.WriteFile(abs, []byte(content+"\n"), 0o644)
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
	ID      string `json:"id,omitempty"`
	Path    string `json:"path,omitempty"`
	RelPath string `json:"rel_path,omitempty"`
}

func (ReadArtifactTool) Name() string { return NameReadArtifact }

func (ReadArtifactTool) Description() string {
	return "Read a runtime artifact by session-relative artifact path. Use this for SubAgent handoff artifacts such as research reports, task graphs, review reports, or verification logs. Only artifacts under the current session's artifacts directory are readable."
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
		},
		"additionalProperties": false,
		"anyOf": []map[string]any{
			{"required": []string{"path"}},
			{"required": []string{"rel_path"}},
		},
	}
}

func (ReadArtifactTool) IsReadOnly() bool { return true }

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
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
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
	return abs, nil
}
