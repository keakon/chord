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

const (
	maxGlobResults     = 250
	maxGlobOutputBytes = 16 * 1024
)

func (GlobTool) Name() string { return NameGlob }

func (GlobTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameGlob, pathToolConcurrencyPolicy(args, "path"))
}

func (GlobTool) Description() string {
	return "Find files by path using glob syntax. Supports ** for recursive directory matching relative to path." +
		" pattern is a path glob, not a regular expression and not a file-contents search." +
		" Best for discovering candidate files by path or extension before using read, grep, or lsp."
}

func (GlobTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Path glob relative to path (e.g. \"**/*.go\", \"src/**/*.ts\"). Supports ** for recursive directory matching. This is glob syntax, not regex and not a file-contents search.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Base directory to search in. Supports ~ for the current user's home directory. Defaults to current directory.",
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
	resolvedBaseDir, err := resolveToolPath(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	fsys := os.DirFS(resolvedBaseDir)
	matches, err := doublestar.Glob(fsys, a.Pattern)
	if err != nil {
		return "", fmt.Errorf("glob error: %w (pattern uses glob syntax like **/*.go, not regex syntax)", err)
	}

	// Filter out excluded directory entries and .gitignore matches.
	ignore := newGitIgnoreMatcher(resolvedBaseDir)
	filtered := make([]string, 0, len(matches))
	outputBytes := 0
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
		matchBytes := len(m)
		if len(filtered) > 0 {
			matchBytes++
		}
		if len(filtered) > 0 && outputBytes+matchBytes > maxGlobOutputBytes {
			truncated = true
			break
		}
		filtered = append(filtered, m)
		outputBytes += matchBytes
		if len(filtered) >= maxGlobResults || outputBytes >= maxGlobOutputBytes {
			truncated = true
			break
		}
	}

	if len(filtered) == 0 {
		logSlowSearch("Glob", resolvedBaseDir, a.Pattern, "", startedAt, "candidate_count", len(matches), 0, truncated)
		return "No files matched the pattern.", nil
	}

	result := strings.Join(filtered, "\n")
	if truncated || len(filtered) == maxGlobResults || len(result) >= maxGlobOutputBytes {
		result += fmt.Sprintf("\n\n(showing first %d results within %d KiB; refine pattern/path to narrow results)", len(filtered), maxGlobOutputBytes/1024)
	}
	logSlowSearch("Glob", resolvedBaseDir, a.Pattern, "", startedAt, "candidate_count", len(matches), len(filtered), truncated)
	return result, nil
}

// isExcludedPath returns true if the path is inside a skipped directory
// (VCS or tool data directories) or is one itself.
func isExcludedPath(p string) bool {
	parts := strings.SplitSeq(p, "/")
	for part := range parts {
		if skipDirNames[part] {
			return true
		}
	}
	return false
}
