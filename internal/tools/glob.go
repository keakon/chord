package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// GlobTool finds files matching a glob pattern with support for ** recursive matching.
type GlobTool struct{}

type globArgs struct {
	Pattern  string `json:"pattern"`
	FilePath string `json:"path,omitempty"`
}

const maxGlobResults = 1000

func (GlobTool) Name() string { return "Glob" }

func (GlobTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy("Glob", pathToolConcurrencyPolicy(args, "path"))
}

func (GlobTool) Description() string {
	return "Find files matching a glob pattern. Supports ** for recursive directory matching." +
		" Best for discovering candidate files by path or extension before using Read, Grep, or Lsp."
}

func (GlobTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Glob pattern to match files against (e.g. \"**/*.go\", \"src/**/*.ts\").",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Base directory to search in. Defaults to current directory.",
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
}

func (GlobTool) IsReadOnly() bool { return true }

func (GlobTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	startedAt := time.Now()
	var a globArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	baseDir := a.FilePath
	if baseDir == "" {
		baseDir = "."
	}

	fsys := os.DirFS(baseDir)
	matches, err := doublestar.Glob(fsys, a.Pattern)
	if err != nil {
		return "", fmt.Errorf("glob error: %w", err)
	}

	// Filter out excluded directory entries and .gitignore matches.
	ignore := newGitIgnoreMatcher(baseDir)
	filtered := make([]string, 0, len(matches))
	truncated := false
	for _, m := range matches {
		if isExcludedPath(m) {
			continue
		}
		// Skip entries matched by .gitignore.
		if ignore != nil {
			isDir := strings.HasSuffix(m, "/")
			if ignore.Match(m, isDir) {
				continue
			}
		}
		filtered = append(filtered, m)
		if len(filtered) >= maxGlobResults {
			truncated = true
			break
		}
	}

	if len(filtered) == 0 {
		logSlowSearch("Glob", baseDir, a.Pattern, "", startedAt, "candidate_count", len(matches), 0, truncated)
		return "No files matched the pattern.", nil
	}

	result := strings.Join(filtered, "\n")
	if len(filtered) == maxGlobResults {
		result += fmt.Sprintf("\n\n(showing first %d results)", maxGlobResults)
	}
	logSlowSearch("Glob", baseDir, a.Pattern, "", startedAt, "candidate_count", len(matches), len(filtered), truncated)
	return result, nil
}

// isExcludedPath returns true if the path is inside a skipped directory
// (VCS or tool data directories) or is one itself.
func isExcludedPath(p string) bool {
	parts := strings.Split(p, "/")
	for _, part := range parts {
		if skipDirNames[part] {
			return true
		}
	}
	return false
}
