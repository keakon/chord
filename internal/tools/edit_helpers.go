package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func resolveEditPathForBase(path, baseDir string) (string, error) {
	resolved, err := resolveToolPathInDir(path, baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func ResolveEditPathInDir(path, baseDir string) (string, error) {
	return resolveEditPathForBase(path, baseDir)
}

func extractEditPathArg(args json.RawMessage) string {
	var parsed replaceEditArgs
	if err := json.Unmarshal(unwrapToolArgs(args), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Path)
}

// ExtractEditPathFromArgsInDir returns the effective path used by EditTool.
// Keep this parser aligned with replaceEditArgs so permission, locking, and
// file-state bookkeeping follow the same legacy alias as execution.
func ExtractEditPathFromArgsInDir(args json.RawMessage, baseDir string) string {
	path := extractEditPathArg(args)
	if path == "" {
		return ""
	}
	resolved, err := ResolveEditPathInDir(path, baseDir)
	if err != nil {
		return ""
	}
	return resolved
}

// ExtractEditPathFromArgs extracts the file path from Edit or ApplyPatch tool
// arguments for display purposes. It does not resolve the path against a
// BaseDir — callers without session context (e.g. TUI) use this to get the
// model-facing path for display. Agent-internal code should use the pipeline's
// trackedEditPathFromArgs instead.
func ExtractEditPathFromArgs(args json.RawMessage) string {
	path := extractEditPathArg(args)
	if path == "" {
		return ""
	}
	if resolved, err := resolveToolPath(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func normalizePatchUnicodeLine(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\u00a0', '\u2007', '\u202f':
			r = ' '
		case '“', '”', '„', '‟':
			r = '"'
		case '‘', '’', '‚', '‛':
			r = '\''
		case '–', '—', '−':
			r = '-'
		}
		if unicode.IsSpace(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}

func normalizePatchProsePunctuationLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\u00a0', '\u2007', '\u202f':
			r = ' '
		case '“', '”', '„', '‟':
			r = '"'
		case '‘', '’', '‚', '‛':
			r = '\''
		case '–', '—', '−':
			r = '-'
		case '，':
			r = ','
		case '；':
			r = ';'
		case '：':
			r = ':'
		case '。':
			r = '.'
		case '！':
			r = '!'
		case '？':
			r = '?'
		case '（':
			r = '('
		case '）':
			r = ')'
		}
		if unicode.IsSpace(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}

func commonPrefixLen(a, b string) (runes int, bytes int) {
	for bytes < len(a) && bytes < len(b) {
		ra, sizeA := utf8.DecodeRuneInString(a[bytes:])
		rb, sizeB := utf8.DecodeRuneInString(b[bytes:])
		if sizeA != sizeB || ra != rb {
			break
		}
		bytes += sizeA
		runes++
	}
	return runes, bytes
}

func commonSuffixLen(a, b string, prefixBytes int) int {
	count := 0
	for ai, bi := len(a), len(b); ai > prefixBytes && bi > prefixBytes; {
		ra, sizeA := utf8.DecodeLastRuneInString(a[:ai])
		rb, sizeB := utf8.DecodeLastRuneInString(b[:bi])
		if sizeA != sizeB || ra != rb || ai-sizeA < prefixBytes || bi-sizeB < prefixBytes {
			break
		}
		ai -= sizeA
		bi -= sizeB
		count++
	}
	return count
}
