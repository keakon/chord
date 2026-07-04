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

func (GlobTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
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
	captureFullOutput := strings.TrimSpace(SessionDirFromContext(ctx)) != ""

	// Fast path: when all patterns are relative paths without glob metacharacters
	// (e.g. "architecture-review-20260621-064007.html"), stat them directly under
	// the base directory instead of walking it. This avoids scanning huge roots
	// (like the system temp directory) when the caller already knows the path.
	if exactFiles, ok := resolveExactIncludeFiles(resolvedBaseDir, patterns); ok {
		acc := newGlobMatchAccumulator(resolvedBaseDir, len(exactFiles), captureFullOutput)
		for _, file := range exactFiles {
			info, statErr := os.Stat(file)
			if statErr != nil || info.IsDir() {
				continue
			}
			rel, relErr := filepath.Rel(resolvedBaseDir, file)
			if relErr != nil {
				rel = file
			}
			acc.addCandidate(filepath.ToSlash(rel))
		}
		return formatGlobResult(ctx, a, resolvedBaseDir, patterns, acc.result(), startedAt)
	}

	result, err := globWalkMatches(resolvedBaseDir, patterns, captureFullOutput)
	if err != nil {
		return "", err
	}
	return formatGlobResult(ctx, a, resolvedBaseDir, patterns, result, startedAt)
}

func globWalkMatches(resolvedBaseDir string, patterns []string, captureFullOutput bool) (globResult, error) {
	if err := validateGlobPatterns(patterns); err != nil {
		return globResult{}, err
	}
	seenMatches := make(map[string]struct{})
	acc := newGlobMatchAccumulator(resolvedBaseDir, 0, captureFullOutput)
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
		acc.addCandidate(rel)
		return nil
	})
	if err == nil {
		return acc.result(), nil
	}
	if err == errGuardAbort {
		return globResult{}, guard.abortError()
	}
	return globResult{}, err
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

type globResult struct {
	filtered       []string
	fullFiltered   string
	candidateCount int
	truncated      bool
}

type globMatchAccumulator struct {
	ignore            *gitIgnoreMatcher
	filtered          []string
	fullFiltered      strings.Builder
	outputBytes       int
	candidateCount    int
	truncated         bool
	captureFullOutput bool
}

func newGlobMatchAccumulator(resolvedBaseDir string, initialCap int, captureFullOutput bool) *globMatchAccumulator {
	if initialCap > maxGlobResults {
		initialCap = maxGlobResults
	}
	return &globMatchAccumulator{
		ignore:            newGitIgnoreMatcher(resolvedBaseDir),
		filtered:          make([]string, 0, initialCap),
		captureFullOutput: captureFullOutput,
	}
}

func (a *globMatchAccumulator) addCandidate(m string) {
	a.candidateCount++
	if isExcludedPath(m) {
		return
	}
	if a.ignore != nil {
		isDir := strings.HasSuffix(m, "/")
		if a.ignore.Match(m, isDir) {
			return
		}
	}
	matchBytes := len(m)
	if a.truncated {
		a.appendFullFiltered(m)
		return
	}
	if len(a.filtered) > 0 && a.outputBytes+matchBytes > maxGlobOutputBytes {
		a.truncated = true
		a.initFullFiltered()
		a.appendFullFiltered(m)
		return
	}
	a.filtered = append(a.filtered, m)
	a.outputBytes += matchBytes
	if len(a.filtered) >= maxGlobResults || a.outputBytes >= maxGlobOutputBytes {
		a.truncated = true
		a.initFullFiltered()
	}
}

func (a *globMatchAccumulator) initFullFiltered() {
	if !a.captureFullOutput || a.fullFiltered.Len() > 0 {
		return
	}
	for _, m := range a.filtered {
		a.appendFullFiltered(m)
	}
}

func (a *globMatchAccumulator) appendFullFiltered(m string) {
	if !a.captureFullOutput {
		return
	}
	if a.fullFiltered.Len() > 0 {
		a.fullFiltered.WriteByte('\n')
	}
	a.fullFiltered.WriteString(m)
}

func (a *globMatchAccumulator) result() globResult {
	return globResult{
		filtered:       a.filtered,
		fullFiltered:   a.fullFiltered.String(),
		candidateCount: a.candidateCount,
		truncated:      a.truncated,
	}
}

// formatGlobResult renders the filtered matches with coerce notes and the
// truncation hint, and records slow-search telemetry.
func formatGlobResult(ctx context.Context, a globArgs, resolvedBaseDir string, patterns []string, result globResult, startedAt time.Time) (string, error) {
	if len(result.filtered) == 0 {
		logSlowSearch("Glob", resolvedBaseDir, strings.Join(patterns, ","), "", startedAt, "candidate_count", result.candidateCount, 0, result.truncated)
		return prependNotes(globCoerceNotes(a), "No files matched the pattern."), nil
	}

	inlineMatches := strings.Join(result.filtered, "\n")
	content := prependNotes(globCoerceNotes(a), inlineMatches)
	if result.truncated || len(result.filtered) == maxGlobResults || len(content) >= maxGlobOutputBytes {
		fullFiltered := result.fullFiltered
		if fullFiltered == "" {
			fullFiltered = inlineMatches
		}
		if savedPath := saveFullOutput(fullFiltered, SessionDirFromContext(ctx), "glob-results"); savedPath != "" {
			content += fmt.Sprintf("\n\n(showing first %d results within %d KiB; full results saved to %s; refine pattern/path to narrow results)", len(result.filtered), maxGlobOutputBytes/1024, savedPath)
		} else {
			content += fmt.Sprintf("\n\n(showing first %d results within %d KiB; refine pattern/path to narrow results)", len(result.filtered), maxGlobOutputBytes/1024)
		}
	}
	logSlowSearch("Glob", resolvedBaseDir, strings.Join(patterns, ","), "", startedAt, "candidate_count", result.candidateCount, len(result.filtered), result.truncated)
	return content, nil
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
