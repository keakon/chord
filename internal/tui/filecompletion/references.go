package filecompletion

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/keakon/chord/internal/filectx"
)

var atMentionTokenRE = regexp.MustCompile(`@(?:\\.|[^\s@])+`)
var atMentionLineRangeSuffixRE = regexp.MustCompile(`^(.*):(\d+)(?:-(\d+))?$`)

// Characters that commonly delimit a file reference in prose. They are only
// trimmed when the full candidate does not resolve to a file.
const atMentionTrimSuffixes = ".,;:!?)]}>\"'，。；：！？）】》」』’”"

// Characters that can separate an @ file reference from following prose. These
// are only used as a fallback after the full candidate and simple suffix trims
// fail, and candidates are tried from right to left to preserve the longest
// existing path when punctuation is part of a filename.
const atMentionProseDelimiters = ",;:!?)]}>\"'，。；：！？、）】》」』’”"

// Option is a display-ready path candidate; directories allow continued browsing.
type Option struct {
	Path  string
	IsDir bool
}

// Reference identifies a file and an optional source line range.
type Reference struct {
	Path  string
	Lines filectx.LineRange
}

func (r Reference) displayPath() string {
	if r.Lines.IsSet() {
		return r.Path + ":" + r.Lines.String()
	}
	return r.Path
}

func (r Reference) dedupeKey(workingDir string) string {
	resolved := filepath.Clean(ResolvePath(r.Path, workingDir))
	return resolved + "\x00" + r.Lines.String()
}

// DedupeRefs preserves the first reference for each resolved path and line range.
func DedupeRefs(refs []Reference, workingDir string) []Reference {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(refs))
	out := make([]Reference, 0, len(refs))
	for _, ref := range refs {
		key := ref.dedupeKey(workingDir)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	return out
}

// DisplayRefs returns reference labels including any line ranges.
func DisplayRefs(refs []Reference) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref.displayPath())
	}
	return out
}

func expandAtMentionPathPrefix(path string) string {
	if path == "~" {
		return "~/"
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		if path == "~/" {
			return filepath.ToSlash(home) + "/"
		}
		return filepath.ToSlash(filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(path, "~/"))))
	}
	return path
}

// ResolvePath expands home references and resolves relative paths against workingDir.
func ResolvePath(path, workingDir string) string {
	expanded := filepath.FromSlash(expandAtMentionPathPrefix(path))
	if filepath.IsAbs(expanded) {
		return expanded
	}
	if workingDir == "" {
		return expanded
	}
	return filepath.Join(workingDir, expanded)
}

func escapeAtMentionPath(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		if unicode.IsSpace(r) || r == '\\' || r == '@' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// UnescapePath removes the escapes used in an @ file reference.
func UnescapePath(path string) string {
	if !strings.Contains(path, "\\") {
		return path
	}
	var b strings.Builder
	b.Grow(len(path))
	escaped := false
	for _, r := range path {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		b.WriteRune('\\')
	}
	return b.String()
}

func hasAtMentionBoundaryBefore(text string, start int) bool {
	if start <= 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:start])
	if r == utf8.RuneError {
		return false
	}
	// Only reject @mentions that are clearly part of an identifier (email, package@version, decorator, etc.)
	// All other characters (punctuation, whitespace, symbols) are treated as valid boundaries.
	return !(unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_')
}

func atMentionCandidateExists(candidate, workingDir string, tried map[string]bool) bool {
	if candidate == "" || tried[candidate] {
		return false
	}
	tried[candidate] = true
	resolved := ResolvePath(candidate, workingDir)
	info, err := os.Stat(resolved)
	return err == nil && !info.IsDir()
}

func trimAtMentionCandidate(candidate string) (string, bool) {
	if candidate == "" {
		return "", false
	}
	r, size := utf8.DecodeLastRuneInString(candidate)
	if r == utf8.RuneError || !strings.ContainsRune(atMentionTrimSuffixes, r) {
		return "", false
	}
	return candidate[:len(candidate)-size], true
}

func proseDelimitedAtMentionCandidates(candidate string) []string {
	var out []string
	for i := len(candidate); i > 0; {
		r, size := utf8.DecodeLastRuneInString(candidate[:i])
		if r == utf8.RuneError {
			break
		}
		i -= size
		if !strings.ContainsRune(atMentionProseDelimiters, r) {
			continue
		}
		prefix := strings.TrimSpace(candidate[:i])
		if prefix != "" {
			out = append(out, prefix)
		}
	}
	return out
}

func resolveAtMentionCandidateRef(candidate, workingDir string) (Reference, bool) {
	candidate = filepath.ToSlash(UnescapePath(candidate))
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return Reference{}, false
	}
	tried := map[string]bool{}
	for trimmed := candidate; trimmed != ""; {
		if atMentionCandidateExists(trimmed, workingDir, tried) {
			return Reference{Path: trimmed}, true
		}
		next, ok := trimAtMentionCandidate(trimmed)
		if !ok {
			break
		}
		trimmed = next
	}
	if ref, ok := parseAtMentionLineRangeRef(candidate, workingDir, tried); ok {
		return ref, true
	}
	for _, delimited := range proseDelimitedAtMentionCandidates(candidate) {
		for trimmed := delimited; trimmed != ""; {
			if atMentionCandidateExists(trimmed, workingDir, tried) {
				return Reference{Path: trimmed}, true
			}
			if ref, ok := parseAtMentionLineRangeRef(trimmed, workingDir, tried); ok {
				return ref, true
			}
			next, ok := trimAtMentionCandidate(trimmed)
			if !ok {
				break
			}
			trimmed = next
		}
	}
	return Reference{}, false
}

func parseAtMentionLineRangeRef(candidate, workingDir string, tried map[string]bool) (Reference, bool) {
	matches := atMentionLineRangeSuffixRE.FindStringSubmatch(candidate)
	if matches == nil {
		return Reference{}, false
	}
	path := strings.TrimSpace(matches[1])
	if path == "" || !atMentionCandidateExists(path, workingDir, tried) {
		return Reference{}, false
	}
	start, err := strconv.Atoi(matches[2])
	if err != nil || start <= 0 {
		return Reference{}, false
	}
	end := start
	if matches[3] != "" {
		end, err = strconv.Atoi(matches[3])
		if err != nil || end < start {
			return Reference{}, false
		}
	}
	return Reference{Path: path, Lines: filectx.LineRange{Start: start, End: end}}, true
}

// ParseRefs resolves @ file references in input text, preferring existing full paths
// over punctuation trimming or line-range interpretation.
func ParseRefs(texts []string, workingDir string) []Reference {
	var refs []Reference
	seen := make(map[string]bool)
	for _, text := range texts {
		if text == "" {
			continue
		}
		for _, loc := range atMentionTokenRE.FindAllStringIndex(text, -1) {
			if len(loc) != 2 {
				continue
			}
			start, end := loc[0], loc[1]
			if !hasAtMentionBoundaryBefore(text, start) {
				continue
			}
			raw := text[start+1 : end]
			ref, ok := resolveAtMentionCandidateRef(raw, workingDir)
			if !ok {
				continue
			}
			key := ref.dedupeKey(workingDir)
			if seen[key] {
				continue
			}
			seen[key] = true
			refs = append(refs, ref)
		}
	}
	return refs
}
