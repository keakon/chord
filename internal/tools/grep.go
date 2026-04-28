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

const maxGrepMatches = 250

var errMaxGrepMatchesReached = errors.New("max grep matches reached")

func (GrepTool) Name() string { return "Grep" }

func (GrepTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy("Grep", pathToolConcurrencyPolicy(args, "path"))
}

func (GrepTool) Description() string {
	return "Search file contents using a regular expression pattern. Returns matching lines with file paths and line numbers." +
		" Best for discovering candidate files, symbols, or text matches when the exact location is not known yet." +
		" For semantic navigation at a known position (definition, references, implementations), prefer the Lsp tool when the file type has LSP coverage."
}

func (GrepTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regular expression pattern to search for.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory or file path to search in. Defaults to current directory.",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "Glob pattern to filter filenames (e.g. \"*.go\", \"*.{ts,tsx}\").",
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
		return "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	searchPath := a.FilePath
	if searchPath == "" {
		searchPath = "."
	}

	// Check if searchPath is a file or directory.
	info, err := os.Stat(searchPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("path not found: %s", searchPath)
		}
		return "", fmt.Errorf("accessing path: %w", err)
	}

	var matches []string
	var scannedFiles int64
	truncated := false

	if !info.IsDir() {
		// Search a single file. Honor the binary-extension fast-path so that
		// e.g. `Grep pattern path=foo.pyc` never returns mojibake.
		if IsBinaryExtension(filepath.Base(searchPath)) {
			return "No matches found.", nil
		}
		fileMatches, err := searchFile(searchPath, re)
		if err != nil {
			return "", err
		}
		matches = fileMatches
		scannedFiles = 1
		reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
	} else {
		// Walk the directory tree.
		// Load .gitignore rules from the search root (if any).
		ignore := newGitIgnoreMatcher(searchPath)

		err = filepath.WalkDir(searchPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // skip errors
			}

			// Skip known VCS / tool directories.
			if d.IsDir() && skipDirNames[d.Name()] {
				return filepath.SkipDir
			}

			// Skip directories matched by .gitignore.
			if d.IsDir() {
				if rel, err := filepath.Rel(searchPath, path); err == nil {
					rel = filepath.ToSlash(rel)
					if ignore.Match(rel, true) {
						return filepath.SkipDir
					}
				}
				return nil
			}

			// Skip files matched by .gitignore.
			if rel, err := filepath.Rel(searchPath, path); err == nil {
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

			fileMatches, err := searchFile(path, re)
			if err != nil {
				return nil // skip files we can't read
			}
			matches = append(matches, fileMatches...)
			scannedFiles++
			if scannedFiles <= 5 || scannedFiles%10 == 0 {
				reportToolProgress(ctx, ToolProgressSnapshot{Label: "files", Current: scannedFiles})
			}

			// Stop early if we have enough matches.
			if len(matches) >= maxGrepMatches {
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
		logSlowSearch("Grep", searchPath, a.Pattern, a.Include, startedAt, "scanned_files", int(scannedFiles), 0, truncated)
		return "No matches found.", nil
	}

	if len(matches) > maxGrepMatches {
		matches = matches[:maxGrepMatches]
	}

	result := strings.Join(matches, "\n")
	if len(matches) == maxGrepMatches {
		result += fmt.Sprintf("\n\n(showing first %d matches)", maxGrepMatches)
	}
	logSlowSearch("Grep", searchPath, a.Pattern, a.Include, startedAt, "scanned_files", int(scannedFiles), len(matches), truncated)
	return result, nil
}

// searchFile reads a file and returns matching lines in "path:linenum:content" format.
// Binary files are skipped to avoid producing mojibake / stray terminal control
// sequences in the tool output.
func searchFile(path string, re *regexp.Regexp) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Peek the head of the file to detect binary content (NUL bytes, high
	// ratio of control bytes, known binary content-types). Matches ripgrep's
	// default behavior of skipping binary files.
	head := make([]byte, binarySampleBytes)
	n, _ := io.ReadFull(f, head)
	if looksBinary(head[:n]) {
		return nil, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	var matches []string
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
			matches = append(matches, fmt.Sprintf("%s:%d:%s", path, lineNum, display))
		}
	}

	return matches, scanner.Err()
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
			alternatives := strings.Split(pattern[start+1:end], ",")
			for _, alt := range alternatives {
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
