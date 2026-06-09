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
	Patterns        []string `json:"patterns,omitempty"`
	Path            string   `json:"path,omitempty"`
	PatternsCoerced bool     `json:"-"`
}

// UnmarshalJSON accepts either a string or array of strings for patterns,
// recording whether a scalar was coerced into a single-element list so the
// executor can attach a result-level hint nudging the documented array shape.
func (a *globArgs) UnmarshalJSON(data []byte) error {
	var raw struct {
		Patterns json.RawMessage `json:"patterns,omitempty"`
		Pattern  json.RawMessage `json:"pattern,omitempty"`
		Path     string          `json:"path,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Accept the deprecated singular "pattern" field when "patterns" is absent
	// so legacy-shaped calls still work; the current field always wins.
	if len(raw.Patterns) == 0 {
		raw.Patterns = raw.Pattern
	}
	patterns, coerced, err := DecodeStringOrList(raw.Patterns)
	if err != nil {
		return fmt.Errorf("patterns: %w", err)
	}
	a.Patterns = patterns
	a.Path = raw.Path
	a.PatternsCoerced = coerced
	return nil
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
		" patterns are path globs, not regular expressions and not file-contents searches." +
		" Pass patterns as a JSON array (e.g. patterns: [\"**/*.go\"] or patterns: [\"src/**/*.ts\", \"test/**/*.ts\"]); a single bare string is tolerated but a single-element array is preferred." +
		" Best for discovering candidate files by path or extension before using read, grep, or lsp."
}

func (GlobTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patterns": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"minItems":         1,
				"description":      "Path globs relative to path, as a JSON array (e.g. [\"**/*.go\"] or [\"src/**/*.ts\", \"test/**/*.ts\"]). Supports ** for recursive directory matching. This is glob syntax, not regex and not a file-contents search.",
				"coerceFromString": true,
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Single base directory to search from. Supports ~ for the current user's home directory. Defaults to current directory.",
			},
		},
		"required":             []string{"patterns"},
		"additionalProperties": false,
	}
}

func (GlobTool) IsReadOnly() bool { return true }

// legacyArgAliases maps deprecated singular field names to the current plural
// schema fields so legacy-shaped calls validate without exposing the old names
// in Parameters().
func (GlobTool) legacyArgAliases() map[string]string {
	return map[string]string{"pattern": "patterns"}
}

func (GlobTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	startedAt := time.Now()
	var a globArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	patterns := normalizeStringList(a.Patterns)
	if len(patterns) == 0 {
		return "", fmt.Errorf("patterns is required")
	}

	baseDir := a.Path
	if baseDir == "" {
		baseDir = "."
	}
	resolvedBaseDir, err := resolveToolPath(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	fsys := os.DirFS(resolvedBaseDir)
	matches := make([]string, 0)
	seenMatches := make(map[string]struct{})
	for _, pattern := range patterns {
		patternMatches, err := doublestar.Glob(fsys, pattern)
		if err != nil {
			return "", fmt.Errorf("glob error: %w (patterns use glob syntax like **/*.go, not regex syntax)", err)
		}
		for _, match := range patternMatches {
			if _, ok := seenMatches[match]; ok {
				continue
			}
			seenMatches[match] = struct{}{}
			matches = append(matches, match)
		}
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
		logSlowSearch("Glob", resolvedBaseDir, strings.Join(patterns, ","), "", startedAt, "candidate_count", len(matches), 0, truncated)
		return prependNotes(globCoerceNotes(a), "No files matched the pattern."), nil
	}

	result := strings.Join(filtered, "\n")
	result = prependNotes(globCoerceNotes(a), result)
	if truncated || len(filtered) == maxGlobResults || len(result) >= maxGlobOutputBytes {
		result += fmt.Sprintf("\n\n(showing first %d results within %d KiB; refine pattern/path to narrow results)", len(filtered), maxGlobOutputBytes/1024)
	}
	logSlowSearch("Glob", resolvedBaseDir, strings.Join(patterns, ","), "", startedAt, "candidate_count", len(matches), len(filtered), truncated)
	return result, nil
}

func globCoerceNotes(a globArgs) []string {
	if !a.PatternsCoerced {
		return nil
	}
	return []string{"Note: patterns was a single string; treated as one pattern. Prefer patterns: [...] next time."}
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
