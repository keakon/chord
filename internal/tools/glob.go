package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
		" If the exact relative file path is known, pass it as the pattern (e.g. patterns: [\"src/main.go\"]) instead of using ** from a very broad path like /, /tmp, or the home directory." +
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

func (GlobTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (GlobTool) CanRenderBeforeToolUseEnd(json.RawMessage) bool { return true }

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

	// Fast path: when all patterns are relative paths without glob metacharacters
	// (e.g. "architecture-review-20260621-064007.html"), stat them directly under
	// the base directory instead of walking it. This avoids scanning huge roots
	// (like the system temp directory) when the caller already knows the path.
	if exactFiles, ok := resolveExactIncludeFiles(resolvedBaseDir, patterns); ok {
		directMatches := make([]string, 0, len(exactFiles))
		for _, file := range exactFiles {
			info, statErr := os.Stat(file)
			if statErr != nil || info.IsDir() {
				continue
			}
			rel, relErr := filepath.Rel(resolvedBaseDir, file)
			if relErr != nil {
				rel = file
			}
			directMatches = append(directMatches, filepath.ToSlash(rel))
		}
		filtered, truncated := filterGlobMatches(resolvedBaseDir, directMatches)
		return formatGlobResult(a, resolvedBaseDir, patterns, len(directMatches), filtered, truncated, startedAt)
	}

	matches, err := globWalkMatches(resolvedBaseDir, patterns)
	if err != nil {
		return "", err
	}

	// Filter out excluded directory entries and .gitignore matches.
	filtered, truncated := filterGlobMatches(resolvedBaseDir, matches)
	return formatGlobResult(a, resolvedBaseDir, patterns, len(matches), filtered, truncated, startedAt)
}

func globWalkMatches(resolvedBaseDir string, patterns []string) ([]string, error) {
	if err := validateGlobPatterns(patterns); err != nil {
		return nil, err
	}
	seenMatches := make(map[string]struct{})
	matches := make([]string, 0)
	guard := newBroadSearchGuard("Glob", resolvedBaseDir, "patterns", patterns)
	err := fs.WalkDir(os.DirFS(resolvedBaseDir), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		guard.visit()
		if guard.shouldAbort() {
			return errGuardAbort
		}
		if d.IsDir() && path != "." && skipDirNames[d.Name()] {
			return fs.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		rel := filepath.ToSlash(path)
		matched, err := matchAnyGlobPattern(rel, patterns)
		if err != nil {
			return fmt.Errorf("glob error: %w (patterns use glob syntax like **/*.go, not regex syntax)", err)
		}
		if !matched {
			return nil
		}
		guard.candidate()
		if _, ok := seenMatches[rel]; ok {
			return nil
		}
		seenMatches[rel] = struct{}{}
		matches = append(matches, rel)
		return nil
	})
	if err == nil {
		return matches, nil
	}
	if err == errGuardAbort {
		return nil, guard.abortError()
	}
	return nil, err
}

func validateGlobPatterns(patterns []string) error {
	for _, pattern := range patterns {
		if !doublestar.ValidatePattern(pattern) {
			return fmt.Errorf("glob error: bad pattern (patterns use glob syntax like **/*.go, not regex syntax)")
		}
	}
	return nil
}

func matchAnyGlobPattern(path string, patterns []string) (bool, error) {
	for _, pattern := range patterns {
		matched, err := doublestar.PathMatch(pattern, path)
		if err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}

// filterGlobMatches drops excluded directories and .gitignore matches, and
// caps the result by both count and output bytes.
func filterGlobMatches(resolvedBaseDir string, matches []string) ([]string, bool) {
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
	return filtered, truncated
}

// formatGlobResult renders the filtered matches with coerce notes and the
// truncation hint, and records slow-search telemetry.
func formatGlobResult(a globArgs, resolvedBaseDir string, patterns []string, candidateCount int, filtered []string, truncated bool, startedAt time.Time) (string, error) {
	if len(filtered) == 0 {
		logSlowSearch("Glob", resolvedBaseDir, strings.Join(patterns, ","), "", startedAt, "candidate_count", candidateCount, 0, truncated)
		return prependNotes(globCoerceNotes(a), "No files matched the pattern."), nil
	}

	result := strings.Join(filtered, "\n")
	result = prependNotes(globCoerceNotes(a), result)
	if truncated || len(filtered) == maxGlobResults || len(result) >= maxGlobOutputBytes {
		result += fmt.Sprintf("\n\n(showing first %d results within %d KiB; refine pattern/path to narrow results)", len(filtered), maxGlobOutputBytes/1024)
	}
	logSlowSearch("Glob", resolvedBaseDir, strings.Join(patterns, ","), "", startedAt, "candidate_count", candidateCount, len(filtered), truncated)
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
