package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// GrepTool searches file contents using a regex pattern.
type GrepTool struct {
	BaseDir string // session working directory for relative paths; empty keeps process cwd behavior
}

type grepArgs struct {
	Pattern         string   `json:"pattern"`
	Paths           []string `json:"paths,omitempty"`
	Includes        []string `json:"includes,omitempty"`
	PathsCoerced    bool     `json:"-"`
	IncludesCoerced bool     `json:"-"`
}

// UnmarshalJSON accepts either a string or array of strings for paths and
// includes, recording whether a scalar was coerced into a single-element list.
// This keeps strict array semantics in the documented schema while preventing
// hard failures when models supply a single string by habit.
func (a *grepArgs) UnmarshalJSON(data []byte) error {
	var raw struct {
		Pattern  string          `json:"pattern"`
		Paths    json.RawMessage `json:"paths,omitempty"`
		Includes json.RawMessage `json:"includes,omitempty"`
		Path     json.RawMessage `json:"path,omitempty"`
		Glob     json.RawMessage `json:"glob,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	a.Pattern = raw.Pattern
	// Accept deprecated singular fields when their current counterparts are
	// absent so legacy-shaped calls still work; current fields always win.
	if len(raw.Paths) == 0 {
		raw.Paths = raw.Path
	}
	if len(raw.Includes) == 0 {
		raw.Includes = raw.Glob
	}
	paths, pathsCoerced, err := DecodeStringOrList(raw.Paths)
	if err != nil {
		return fmt.Errorf("paths: %w", err)
	}
	includes, includesCoerced, err := DecodeStringOrList(raw.Includes)
	if err != nil {
		return fmt.Errorf("includes: %w", err)
	}
	a.Paths = paths
	a.Includes = includes
	a.PathsCoerced = pathsCoerced
	a.IncludesCoerced = includesCoerced
	return nil
}

const (
	maxGrepMatches     = 120
	maxGrepOutputBytes = 12 * 1024
)

var errMaxGrepMatchesReached = errors.New("max grep matches reached")

func (GrepTool) Name() string { return NameGrep }

func (t GrepTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameGrep, pathsToolConcurrencyPolicyInDir(args, "paths", t.BaseDir))
}

func (GrepTool) Description() string {
	return "Search file contents using a regular expression. If pattern is not valid regex, it is safely searched as literal text and the result reports that fallback." +
		" Use paths for one or more files/directories (JSON array, e.g. paths: [\"internal\", \"cmd\"]), and includes for optional path globs such as **/*.go (JSON array, e.g. includes: [\"**/*.go\"]). Relative paths resolve from the session working directory." +
		" If the exact file path is known, pass the full file path in paths instead of searching its parent directory with the filename in includes; includes filters files during traversal and does not avoid walking the search path." +
		" Single bare strings are tolerated for paths/includes but arrays are preferred." +
		" Returns matching lines with file paths and line numbers." +
		" Best for discovering candidate files, symbols, or text matches when the exact location is not known yet." +
		" For semantic navigation at a known position (definition, references, implementations), prefer the lsp tool when the file type has LSP coverage."
}

func (GrepTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regular expression for file contents. If invalid as regex, it is searched as literal text and the result reports that fallback.",
			},
			"paths": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"description":      "One or more files/directories to search (JSON array, e.g. [\"internal\", \"cmd\"]). Relative paths resolve from the session working directory. Supports ~ for the current user's home directory. Defaults to the session working directory when omitted.",
				"coerceFromString": true,
			},
			"includes": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"description":      "Optional path glob filters relative to each searched directory, as a JSON array (e.g. [\"**/*.go\"] or [\"internal/**/*.ts\", \"cmd/**/*.ts\"]). Omit to search all non-ignored text files.",
				"coerceFromString": true,
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
}

func (GrepTool) IsReadOnly() bool { return true }

func (GrepTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (GrepTool) CanRenderBeforeToolUseEnd(json.RawMessage) bool { return true }

// legacyArgAliases maps deprecated singular field names to the current plural
// schema fields so legacy-shaped calls validate without exposing the old names
// in Parameters().
func (GrepTool) legacyArgAliases() map[string]string {
	return map[string]string{"path": "paths", "glob": "includes"}
}

func (t GrepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	startedAt := time.Now()
	var a grepArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	re, err := regexp.Compile(a.Pattern)
	literalFallback := false
	if err != nil {
		re = regexp.MustCompile(regexp.QuoteMeta(a.Pattern))
		literalFallback = true
	}

	var matches []string
	var outputBytes int
	var scannedFiles int64
	truncated := false
	paths := grepSearchPaths(a, t.BaseDir)
	includes := grepIncludes(a)
	searched := make([]string, 0, len(paths))

	var pathErrors []string
	for _, searchPath := range paths {
		resolvedSearchPath, info, err := resolveExistingToolPathInDir(searchPath, t.BaseDir, PathTargetAny, "search")
		if err != nil {
			pathErrors = append(pathErrors, grepPathErrorWithHint(searchPath, t.BaseDir, err).Error())
			continue
		}
		searched = append(searched, resolvedSearchPath)
		rootMatches, rootBytes, rootScanned, rootTruncated, err := grepSearchRoot(ctx, searchPath, resolvedSearchPath, info, re, includes, t.BaseDir, maxGrepMatches-len(matches), maxGrepOutputBytes-outputBytes)
		if err != nil {
			pathErrors = append(pathErrors, fmt.Sprintf("%s: %v", resolvedSearchPath, err))
			continue
		}
		matches = append(matches, rootMatches...)
		outputBytes += rootBytes
		scannedFiles += rootScanned
		if rootTruncated || len(matches) >= maxGrepMatches || outputBytes >= maxGrepOutputBytes {
			truncated = true
			break
		}
	}

	// Every path failed to resolve/search: return the aggregate error. Judge by
	// whether all paths errored, not by match count, otherwise a successful but
	// empty search plus one failed path would be misreported as all-failed.
	if len(pathErrors) > 0 && len(pathErrors) == len(paths) {
		return "", fmt.Errorf("all search paths failed: %s", strings.Join(pathErrors, "; "))
	}

	filter := strings.Join(includes, ",")
	searchLabel := strings.Join(searched, ",")
	notes := grepCoerceNotes(a)
	// Append per-path failures as notes when partial results exist.
	for _, pe := range pathErrors {
		notes = append(notes, "grep: skipped path: "+pe)
	}
	if len(matches) == 0 {
		logSlowSearch("Grep", searchLabel, a.Pattern, filter, startedAt, "scanned_files", int(scannedFiles), 0, truncated)
		msg := "No matches found."
		if literalFallback {
			msg = "No matches found. (pattern was invalid regex; searched as literal text)"
		}
		return prependNotes(notes, msg), nil
	}

	if len(matches) > maxGrepMatches {
		matches = matches[:maxGrepMatches]
	}

	result := strings.Join(matches, "\n")
	if literalFallback {
		result = "Note: pattern was invalid regex; searched as literal text.\n" + result
	}
	result = prependNotes(notes, result)
	if truncated || len(matches) == maxGrepMatches || len(result) >= maxGrepOutputBytes {
		result += fmt.Sprintf("\n\n(showing first %d matches within %d KiB; narrow paths/includes/pattern for more precise results)", len(matches), maxGrepOutputBytes/1024)
	}
	logSlowSearch("Grep", searchLabel, a.Pattern, filter, startedAt, "scanned_files", int(scannedFiles), len(matches), truncated)
	return result, nil
}

func grepSearchPaths(a grepArgs, baseDir string) []string {
	paths := normalizeStringList(a.Paths)
	if len(paths) == 0 {
		if strings.TrimSpace(baseDir) != "" {
			paths = []string{baseDir}
		} else {
			paths = []string{"."}
		}
	}
	return paths
}

func grepIncludes(a grepArgs) []string {
	return normalizeStringList(a.Includes)
}

func grepCoerceNotes(a grepArgs) []string {
	var notes []string
	if a.PathsCoerced {
		notes = append(notes, "Note: paths was a single string; treated as one path. Prefer paths: [...] next time.")
	}
	if a.IncludesCoerced {
		notes = append(notes, "Note: includes was a single string; treated as one filter. Prefer includes: [...] next time.")
	}
	return notes
}

func prependNotes(notes []string, body string) string {
	if len(notes) == 0 {
		return body
	}
	return strings.Join(notes, "\n") + "\n" + body
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// DecodeStringOrList decodes a JSON value that may be either a single string or
// an array of strings. coerced is true when the caller supplied a bare string,
// so the executor can attach a result-level hint nudging the documented array
// shape and permission/display layers can reproduce the same scalar->array
// coercion instead of falling back to a wildcard argument. An empty/missing
// field returns (nil, false, nil).
func DecodeStringOrList(raw json.RawMessage) ([]string, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		return nil, false, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, false, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, false, err
	}
	return []string{single}, true, nil
}

func grepSearchRoot(ctx context.Context, searchPath, resolvedSearchPath string, info os.FileInfo, re *regexp.Regexp, includes []string, baseDir string, maxMatches, maxBytes int) ([]string, int, int64, bool, error) {
	if maxMatches <= 0 || maxBytes <= 0 {
		return nil, 0, 0, true, nil
	}
	if !info.IsDir() {
		if err := ensureRegularFilePath(searchPath, info); err != nil {
			return nil, 0, 0, false, err
		}
		if IsBinaryExtension(filepath.Base(resolvedSearchPath)) {
			return nil, 0, 1, false, nil
		}
		fileMatches, truncated, err := searchFile(resolvedSearchPath, func() string {
			return displayPathForBaseDir(resolvedSearchPath, baseDir)
		}, re, maxMatches, maxBytes)
		if err != nil {
			return nil, 0, 0, false, err
		}
		reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: 1})
		return fileMatches, joinedLinesBytes(fileMatches), 1, truncated, nil
	}

	// Fast path: when includes contains a relative path with no glob metacharacters
	// (e.g. "architecture-review-20260621-064007.html" or "src/main.go"), try the
	// exact file under the search root before recursively walking it. This avoids
	// walking huge roots (like the system temp directory) when the caller already
	// knows the relative path.
	if exactFiles, ok := resolveExactIncludeFiles(resolvedSearchPath, includes); ok {
		var matches []string
		var outputBytes int
		var scannedFiles int64
		truncated := false
		for _, file := range exactFiles {
			remainingMatches := maxMatches - len(matches)
			remainingBytes := maxBytes - outputBytes
			if remainingMatches <= 0 || remainingBytes <= 0 {
				truncated = true
				break
			}
			info, err := os.Stat(file)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if IsBinaryExtension(filepath.Base(file)) {
				continue
			}
			fileMatches, fileTruncated, err := searchFile(file, func() string {
				return displayPathForBaseDir(file, baseDir)
			}, re, remainingMatches, remainingBytes)
			if err != nil {
				return nil, 0, 0, false, err
			}
			matches = append(matches, fileMatches...)
			outputBytes = joinedLinesBytes(matches)
			scannedFiles++
			if fileTruncated {
				truncated = true
				break
			}
		}
		if scannedFiles > 0 {
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
		}
		return matches, outputBytes, scannedFiles, truncated, nil
	}

	var matches []string
	var outputBytes int
	var scannedFiles int64
	truncated := false
	ignore := newGitIgnoreMatcher(resolvedSearchPath)
	guard := newBroadSearchGuard("Grep", resolvedSearchPath, "includes", includes)
	err := filepath.WalkDir(resolvedSearchPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		guard.visit()
		if guard.shouldAbort() {
			return errGuardAbort
		}
		if d.IsDir() && skipDirNames[d.Name()] {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if rel, err := filepath.Rel(resolvedSearchPath, path); err == nil {
				rel = filepath.ToSlash(rel)
				if ignore.Match(rel, true) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if rel, err := filepath.Rel(resolvedSearchPath, path); err == nil {
			rel = filepath.ToSlash(rel)
			if ignore.Match(rel, false) {
				return nil
			}
			if len(includes) > 0 {
				matched, matchErr := matchAnyIncludePattern(rel, includes)
				if matchErr != nil || !matched {
					return nil
				}
			}
		}
		if !d.Type().IsRegular() || IsBinaryExtension(d.Name()) {
			return nil
		}
		guard.candidate()
		remainingMatches := maxMatches - len(matches)
		remainingBytes := maxBytes - outputBytes
		if remainingMatches <= 0 || remainingBytes <= 0 {
			truncated = true
			return errMaxGrepMatchesReached
		}
		fileMatches, truncatedByBytes, err := searchFile(path, func() string {
			return displayPathForBaseDir(path, baseDir)
		}, re, remainingMatches, remainingBytes)
		if err != nil {
			return nil
		}
		matches = append(matches, fileMatches...)
		outputBytes = joinedLinesBytes(matches)
		scannedFiles++
		if scannedFiles <= 5 || scannedFiles%10 == 0 {
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
		}
		if len(matches) >= maxMatches || truncatedByBytes || outputBytes >= maxBytes {
			truncated = true
			return errMaxGrepMatchesReached
		}
		return nil
	})
	switch {
	case err == nil:
	case errors.Is(err, errMaxGrepMatchesReached):
	case errors.Is(err, errGuardAbort):
		return nil, 0, scannedFiles, truncated, guard.abortError()
	default:
		return nil, 0, scannedFiles, truncated, fmt.Errorf("walking directory: %w", err)
	}
	if scannedFiles > 0 {
		reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
	}
	return matches, outputBytes, scannedFiles, truncated, nil
}

func grepPathErrorWithHint(path string, baseDir string, err error) error {
	if err == nil || !strings.Contains(err.Error(), "path not found:") || !strings.ContainsAny(path, " \t\n\r") {
		return err
	}
	parts := strings.Fields(path)
	if len(parts) < 2 {
		return err
	}
	for _, part := range parts {
		resolved, resolveErr := resolveToolPathInDir(part, baseDir)
		if resolveErr != nil {
			return err
		}
		if _, statErr := os.Stat(resolved); statErr != nil {
			return err
		}
	}
	return fmt.Errorf("%w. grep.paths accepts an array of file or directory paths; to search multiple directories, pass each path as a separate array item", err)
}

// searchFile reads a file and returns matching lines in "path:linenum:content" format.
// Binary files are skipped to avoid producing mojibake / stray terminal control
// sequences in the tool output.
func searchFile(path string, displayPathFunc func() string, re *regexp.Regexp, maxMatches, maxBytes int) ([]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	// Peek the head of the file to detect binary content (NUL bytes, high
	// ratio of control bytes, known binary content-types). Matches ripgrep's
	// default behavior of skipping binary files.
	head := make([]byte, binarySampleBytes)
	n, _ := io.ReadFull(f, head)
	if looksBinary(head[:n]) {
		return nil, false, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, false, err
	}

	var matches []string
	var outputBytes int
	scanner := bufio.NewScanner(f)
	// Increase scanner buffer for long lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0
	displayPath := ""
	displayPathResolved := false
	getDisplayPath := func() string {
		if !displayPathResolved {
			if displayPathFunc != nil {
				displayPath = displayPathFunc()
			}
			if strings.TrimSpace(displayPath) == "" {
				displayPath = path
			}
			displayPathResolved = true
		}
		return displayPath
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if re.MatchString(line) {
			displayPath := getDisplayPath()
			display := sanitizeGrepLine(line)
			match := fmt.Sprintf("%s:%d:%s", displayPath, lineNum, display)
			matchBytes := len(match)
			if len(matches) > 0 {
				matchBytes++
			}
			if maxBytes > 0 && outputBytes+matchBytes > maxBytes {
				if len(matches) > 0 {
					return matches, true, scanner.Err()
				}
				prefix := fmt.Sprintf("%s:%d:", displayPath, lineNum)
				available := maxBytes - len(prefix) - len("...")
				if available <= 0 {
					return nil, true, scanner.Err()
				}
				match = prefix + truncateStringToValidUTF8Prefix(display, available) + "..."
				matchBytes = len(match)
				if matchBytes > maxBytes {
					return nil, true, scanner.Err()
				}
				matches = append(matches, match)
				outputBytes += matchBytes
				return matches, true, scanner.Err()
			}
			matches = append(matches, match)
			outputBytes += matchBytes
			if maxMatches > 0 && len(matches) >= maxMatches {
				return matches, true, scanner.Err()
			}
			if maxBytes > 0 && outputBytes >= maxBytes {
				return matches, true, scanner.Err()
			}
		}
	}

	return matches, false, scanner.Err()
}

func joinedLinesBytes(lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	n := len(lines) - 1
	for _, line := range lines {
		n += len(line)
	}
	return n
}

// sanitizeGrepLine strips C0 control characters (except tab) and replaces
// invalid UTF-8 byte sequences with U+FFFD. This prevents embedded ESC/CSI
// bytes from corrupting the terminal's SGR state when the result is rendered
// in the TUI, and avoids dumping arbitrary binary bytes into the context.
func sanitizeGrepLine(s string) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// matchIncludePattern supports simple glob patterns including brace expansion
// like "*.{go,ts}".
func matchIncludePattern(name string, pattern string) (bool, error) {
	// Handle brace expansion for patterns like "*.{go,ts}".
	if strings.Contains(pattern, "{") && strings.Contains(pattern, "}") {
		start := strings.Index(pattern, "{")
		end := strings.Index(pattern, "}")
		if start < end {
			prefix := pattern[:start]
			suffix := pattern[end+1:]
			alternatives := strings.SplitSeq(pattern[start+1:end], ",")
			for alt := range alternatives {
				expanded := prefix + strings.TrimSpace(alt) + suffix
				matched, err := filepath.Match(expanded, name)
				if err != nil {
					return false, err
				}
				if matched {
					return true, nil
				}
			}
			return false, nil
		}
	}

	return filepath.Match(pattern, name)
}

func matchAnyIncludePattern(path string, patterns []string) (bool, error) {
	base := filepath.Base(path)
	for _, pattern := range patterns {
		if strings.Contains(pattern, "/") || strings.Contains(pattern, "**") {
			matched, err := doublestar.PathMatch(pattern, path)
			if err != nil || matched {
				return matched, err
			}
			continue
		}
		matched, err := matchIncludePattern(base, pattern)
		if err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}
