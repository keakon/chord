package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// GrepTool searches file contents using a regex pattern.
type GrepTool struct{}

type grepArgs struct {
	Pattern  string `json:"pattern"`
	FilePath string `json:"path,omitempty"`
	Include  string `json:"glob,omitempty"`
}

const (
	maxGrepMatches     = 120
	maxGrepOutputBytes = 12 * 1024
)

var errMaxGrepMatchesReached = errors.New("max grep matches reached")

func (GrepTool) Name() string { return "Grep" }

func (GrepTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy("Grep", pathToolConcurrencyPolicy(args, "path"))
}

func (GrepTool) Description() string {
	return "Search file contents using a regular expression. pattern uses regex syntax, not glob syntax or plain text; escape literal special characters like \\[\\], (), and {} when needed." +
		" Use glob only to filter filenames by basename." +
		" Returns matching lines with file paths and line numbers." +
		" Best for discovering candidate files, symbols, or text matches when the exact location is not known yet." +
		" For semantic navigation at a known position (definition, references, implementations), prefer the Lsp tool when the file type has LSP coverage."
}

func (GrepTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regular expression for file contents. This is regex syntax, not a glob pattern or plain text (for example, use .* rather than * or **, and escape literal [] as \\[\\]).",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory or file path to search in. Supports ~ for the current user's home directory. Defaults to current directory.",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "Glob pattern to filter filenames only, matched against each file's basename (e.g. \"*.go\", \"*.{ts,tsx}\"). Not a recursive path glob; use Glob for **/ path matching.",
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
}

func (GrepTool) IsReadOnly() bool { return true }

func (GrepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	startedAt := time.Now()
	var a grepArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}

	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex pattern: %w (pattern uses regex syntax; escape literal special characters such as [] as \\[\\])", err)
	}

	searchPath := a.FilePath
	if searchPath == "" {
		searchPath = "."
	}
	resolvedSearchPath, info, err := resolveExistingToolPath(searchPath, PathTargetAny, "search")
	if err != nil {
		return "", err
	}

	var matches []string
	var outputBytes int
	var scannedFiles int64
	truncated := false

	if !info.IsDir() {
		if err := ensureRegularFilePath(searchPath, info); err != nil {
			return "", err
		}
		// Search a single file. Honor the binary-extension fast-path so that
		// e.g. `Grep pattern path=foo.pyc` never returns mojibake.
		if IsBinaryExtension(filepath.Base(resolvedSearchPath)) {
			return "No matches found.", nil
		}
		fileMatches, truncatedByBytes, err := searchFile(resolvedSearchPath, re, maxGrepMatches, maxGrepOutputBytes)
		if err != nil {
			return "", err
		}
		matches = fileMatches
		outputBytes = joinedLinesBytes(matches)
		truncated = truncatedByBytes
		scannedFiles = 1
		reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
	} else {
		// Walk the directory tree.
		// Load .gitignore rules from the search root (if any).
		ignore := newGitIgnoreMatcher(resolvedSearchPath)

		err = filepath.WalkDir(resolvedSearchPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // skip errors
			}

			// Skip known VCS / tool directories.
			if d.IsDir() && skipDirNames[d.Name()] {
				return filepath.SkipDir
			}

			// Skip directories matched by .gitignore.
			if d.IsDir() {
				if rel, err := filepath.Rel(resolvedSearchPath, path); err == nil {
					rel = filepath.ToSlash(rel)
					if ignore.Match(rel, true) {
						return filepath.SkipDir
					}
				}
				return nil
			}

			// Skip files matched by .gitignore.
			if rel, err := filepath.Rel(resolvedSearchPath, path); err == nil {
				rel = filepath.ToSlash(rel)
				if ignore.Match(rel, false) {
					return nil
				}
			}

			// Apply include filter on the filename.
			if a.Include != "" {
				matched, matchErr := matchIncludePattern(d.Name(), a.Include)
				if matchErr != nil || !matched {
					return nil
				}
			}

			// Skip binary/unreadable files by checking if the file is regular.
			if !d.Type().IsRegular() {
				return nil
			}

			// Fast-path: skip files with known binary extensions without
			// opening them. searchFile still does a content sniff for files
			// with no extension or with a text-looking extension but binary
			// contents.
			if IsBinaryExtension(d.Name()) {
				return nil
			}

			remainingMatches := maxGrepMatches - len(matches)
			remainingBytes := maxGrepOutputBytes - outputBytes
			if remainingMatches <= 0 || remainingBytes <= 0 {
				truncated = true
				return errMaxGrepMatchesReached
			}
			fileMatches, truncatedByBytes, err := searchFile(path, re, remainingMatches, remainingBytes)
			if err != nil {
				return nil // skip files we can't read
			}
			matches = append(matches, fileMatches...)
			outputBytes = joinedLinesBytes(matches)
			scannedFiles++
			if scannedFiles <= 5 || scannedFiles%10 == 0 {
				reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
			}

			// Stop early if we have enough matches.
			if len(matches) >= maxGrepMatches || truncatedByBytes || outputBytes >= maxGrepOutputBytes {
				truncated = true
				return errMaxGrepMatchesReached
			}
			return nil
		})
		// Ignore the max-match sentinel and surface real walk failures.
		if err != nil && !errors.Is(err, errMaxGrepMatchesReached) {
			return "", fmt.Errorf("walking directory: %w", err)
		}
		if scannedFiles > 0 {
			reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
		}
	}

	if len(matches) == 0 {
		logSlowSearch("Grep", resolvedSearchPath, a.Pattern, a.Include, startedAt, "scanned_files", int(scannedFiles), 0, truncated)
		return "No matches found.", nil
	}

	if len(matches) > maxGrepMatches {
		matches = matches[:maxGrepMatches]
	}

	result := strings.Join(matches, "\n")
	if truncated || len(matches) == maxGrepMatches || len(result) >= maxGrepOutputBytes {
		result += fmt.Sprintf("\n\n(showing first %d matches within %d KiB; narrow path/glob/pattern for more precise results)", len(matches), maxGrepOutputBytes/1024)
	}
	logSlowSearch("Grep", resolvedSearchPath, a.Pattern, a.Include, startedAt, "scanned_files", int(scannedFiles), len(matches), truncated)
	return result, nil
}

// searchFile reads a file and returns matching lines in "path:linenum:content" format.
// Binary files are skipped to avoid producing mojibake / stray terminal control
// sequences in the tool output.
func searchFile(path string, re *regexp.Regexp, maxMatches, maxBytes int) ([]string, bool, error) {
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

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if re.MatchString(line) {
			display := sanitizeGrepLine(line)
			// Truncate very long lines in output.
			if len(display) > MaxLineLength {
				display = display[:MaxLineLength] + "..."
			}
			match := fmt.Sprintf("%s:%d:%s", path, lineNum, display)
			matchBytes := len(match)
			if len(matches) > 0 {
				matchBytes++
			}
			if maxBytes > 0 && len(matches) > 0 && outputBytes+matchBytes > maxBytes {
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
