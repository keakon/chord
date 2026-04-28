package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/lsp"
)

// DeleteTool removes one or more explicit files or symlinks from disk.
// If LSP is set, it closes matching documents in language servers and clears
// touched-file tracking for deleted or already-absent paths.
type DeleteTool struct {
	LSP *lsp.Manager // nil when LSP not configured
}

// DeleteRequest is the normalized Delete tool input.
type DeleteRequest struct {
	Paths  []string `json:"paths"`
	Reason string   `json:"reason"`
}

type deletePathIssue struct {
	Path   string
	Reason string
}

type deleteExecutionResult struct {
	Deleted       []string
	AlreadyAbsent []string
	Blocked       []deletePathIssue
	Failed        []deletePathIssue
	NotAttempted  []string
}

func (t DeleteTool) Name() string { return "Delete" }

func (DeleteTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	// Delete may target multiple files. Until runtime batching grows native
	// multi-resource support, keep it conservatively exclusive.
	return normalizeConcurrencyPolicy("Delete", deleteToolConcurrencyPolicy(args))
}

func (t DeleteTool) Description() string {
	return "Delete one or more explicit files or symlinks. Use this to remove files instead of writing empty content with Write. Requires paths and a short reason. Does not delete directories or wildcard patterns."
}

func (t DeleteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paths": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"minItems":    1,
				"description": "Absolute or relative paths to files or symlinks to delete. Does not delete directories.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Short explanation of why these files should be removed.",
			},
		},
		"required":             []string{"paths", "reason"},
		"additionalProperties": false,
	}
}

func (t DeleteTool) IsReadOnly() bool { return false }

// DecodeDeleteRequest parses, validates, cleans, de-duplicates, and sorts the
// Delete tool arguments. Unknown fields are rejected.
func DecodeDeleteRequest(raw json.RawMessage) (DeleteRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var req DeleteRequest
	if err := dec.Decode(&req); err != nil {
		return DeleteRequest{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != nil && err != io.EOF {
		return DeleteRequest{}, fmt.Errorf("invalid arguments: %w", err)
	}
	req.Paths = NormalizeDeletePaths(req.Paths)
	if len(req.Paths) == 0 {
		return DeleteRequest{}, fmt.Errorf("paths is required")
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		return DeleteRequest{}, fmt.Errorf("reason is required")
	}
	return req, nil
}

// NormalizeDeletePaths applies trimming, filepath.Clean, de-duplication, and a
// stable lexical sort. Empty path entries are discarded.
func NormalizeDeletePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out
}

// DeleteResultGroups parses the structured Delete tool result text.
type DeleteResultGroups struct {
	Deleted       []string
	AlreadyAbsent []string
	Blocked       []string
	Failed        []string
	NotAttempted  []string
}

// ParseDeleteResult extracts grouped path lists from Delete tool output.
func ParseDeleteResult(text string) DeleteResultGroups {
	var groups DeleteResultGroups
	var current *[]string

	for _, rawLine := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		switch line {
		case "Deleted (0):", "Deleted:":
			current = &groups.Deleted
			continue
		case "Already absent (0):", "Already absent:":
			current = &groups.AlreadyAbsent
			continue
		case "Blocked (0):", "Blocked:":
			current = &groups.Blocked
			continue
		case "Failed (0):", "Failed:":
			current = &groups.Failed
			continue
		case "Not attempted (0):", "Not attempted:":
			current = &groups.NotAttempted
			continue
		}
		if strings.HasPrefix(line, "Deleted (") && strings.HasSuffix(line, "):") {
			current = &groups.Deleted
			continue
		}
		if strings.HasPrefix(line, "Already absent (") && strings.HasSuffix(line, "):") {
			current = &groups.AlreadyAbsent
			continue
		}
		if strings.HasPrefix(line, "Blocked (") && strings.HasSuffix(line, "):") {
			current = &groups.Blocked
			continue
		}
		if strings.HasPrefix(line, "Failed (") && strings.HasSuffix(line, "):") {
			current = &groups.Failed
			continue
		}
		if strings.HasPrefix(line, "Not attempted (") && strings.HasSuffix(line, "):") {
			current = &groups.NotAttempted
			continue
		}
		if current == nil || !strings.HasPrefix(line, "- ") {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if idx := strings.Index(item, " — "); idx >= 0 {
			item = strings.TrimSpace(item[:idx])
		}
		if item == "" {
			continue
		}
		*current = append(*current, item)
	}
	return groups
}

func (t DeleteTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	req, err := DecodeDeleteRequest(raw)
	if err != nil {
		return "", err
	}

	result, execErr := t.executeDelete(ctx, req)
	text := formatDeleteResult(result, execErr)
	if execErr == nil {
		return text, nil
	}
	return text, execErr
}

func (t DeleteTool) executeDelete(ctx context.Context, req DeleteRequest) (deleteExecutionResult, error) {
	var result deleteExecutionResult
	targets := make([]string, 0, len(req.Paths))
	totalPaths := int64(len(req.Paths))
	processed := int64(0)

	for _, path := range req.Paths {
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				result.AlreadyAbsent = append(result.AlreadyAbsent, path)
				t.clearLSPDeleteState(path)
				processed++
				reportToolProgress(ctx, ToolProgressSnapshot{Label: "paths", Current: processed, Total: totalPaths})
				continue
			}
			result.Blocked = append(result.Blocked, deletePathIssue{Path: path, Reason: fmt.Sprintf("stating path: %v", err)})
			processed++
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "paths", Current: processed, Total: totalPaths})
			continue
		}
		if info.IsDir() {
			result.Blocked = append(result.Blocked, deletePathIssue{Path: path, Reason: "path is a directory, not a file"})
			processed++
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "paths", Current: processed, Total: totalPaths})
			continue
		}
		mode := info.Mode()
		if !mode.IsRegular() && mode&os.ModeSymlink == 0 {
			result.Blocked = append(result.Blocked, deletePathIssue{Path: path, Reason: "unsupported file type"})
			processed++
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "paths", Current: processed, Total: totalPaths})
			continue
		}
		targets = append(targets, path)
	}

	if len(result.Blocked) > 0 {
		return result, fmt.Errorf("delete failed before execution")
	}

	for i, path := range targets {
		if err := ctx.Err(); err != nil {
			result.NotAttempted = append(result.NotAttempted, targets[i:]...)
			return result, err
		}

		invalidatePathCache(path)
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				result.AlreadyAbsent = append(result.AlreadyAbsent, path)
				t.clearLSPDeleteState(path)
				processed++
				reportToolProgress(ctx, ToolProgressSnapshot{Label: "paths", Current: processed, Total: totalPaths})
				continue
			}
			result.Failed = append(result.Failed, deletePathIssue{Path: path, Reason: err.Error()})
			result.NotAttempted = append(result.NotAttempted, targets[i+1:]...)
			processed++
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "paths", Current: processed, Total: totalPaths})
			return result, fmt.Errorf("delete stopped after an execution error")
		}
		invalidatePathCache(path)
		result.Deleted = append(result.Deleted, path)
		t.clearLSPDeleteState(path)
		processed++
		reportToolProgress(ctx, ToolProgressSnapshot{Label: "paths", Current: processed, Total: totalPaths})
	}

	return result, nil
}

func (t DeleteTool) clearLSPDeleteState(path string) {
	if t.LSP == nil {
		return
	}
	if absPath, err := filepath.Abs(path); err == nil {
		t.LSP.UnmarkTouched(absPath)
		_ = t.LSP.DidCloseErr(absPath)
	}
}

func formatDeleteResult(result deleteExecutionResult, execErr error) string {
	var sections []string
	if len(result.Deleted) > 0 {
		sections = append(sections, formatDeletePathsSection("Deleted", result.Deleted))
	}
	if len(result.AlreadyAbsent) > 0 {
		sections = append(sections, formatDeletePathsSection("Already absent", result.AlreadyAbsent))
	}
	if len(result.Blocked) > 0 {
		sections = append(sections, formatDeleteIssuesSection("Blocked", result.Blocked))
	}
	if len(result.Failed) > 0 {
		sections = append(sections, formatDeleteIssuesSection("Failed", result.Failed))
	}
	if len(result.NotAttempted) > 0 {
		sections = append(sections, formatDeletePathsSection("Not attempted", result.NotAttempted))
	}

	headline := "Delete completed."
	switch {
	case execErr == nil:
		// keep success headline
	case len(result.Blocked) > 0 && len(result.Deleted) == 0:
		headline = "Delete failed before execution; no files were removed."
	default:
		headline = "Delete stopped after an execution error."
	}

	if len(sections) == 0 {
		return headline
	}
	return headline + "\n\n" + strings.Join(sections, "\n\n")
}

func formatDeletePathsSection(title string, paths []string) string {
	lines := make([]string, 0, len(paths)+1)
	lines = append(lines, fmt.Sprintf("%s (%d):", title, len(paths)))
	for _, path := range paths {
		lines = append(lines, "- "+path)
	}
	return strings.Join(lines, "\n")
}

func formatDeleteIssuesSection(title string, issues []deletePathIssue) string {
	lines := make([]string, 0, len(issues)+1)
	lines = append(lines, fmt.Sprintf("%s (%d):", title, len(issues)))
	for _, issue := range issues {
		lines = append(lines, fmt.Sprintf("- %s — %s", issue.Path, issue.Reason))
	}
	return strings.Join(lines, "\n")
}
