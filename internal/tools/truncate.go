package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxOutputLines is the maximum number of lines kept in truncated output.
	MaxOutputLines = 2000
	// MaxOutputBytes is the maximum byte length before truncation is triggered.
	MaxOutputBytes = 50 * 1024
	// MaxLineLength is the maximum UTF-8 byte length per output line before
	// per-line truncation (suffix aligned to a valid UTF-8 boundary).
	MaxLineLength = 2000
)

// TruncateOptions controls how output truncation is performed.
type TruncateOptions struct {
	// MaxLines is the maximum number of lines to keep. Defaults to MaxOutputLines (2000).
	MaxLines int
	// MaxBytes is the maximum byte length before truncation is triggered. Defaults to MaxOutputBytes (50KB).
	MaxBytes int
	// Direction controls which part of the output is preserved:
	//   "head"      – keep only the first MaxLines lines
	//   "tail"      – keep only the last MaxLines lines
	//   "head+tail" – keep the first 40% and last 60% of MaxLines (default)
	Direction string
	// ArtifactKey enables idempotent artifact storage. When non-empty, repeated
	// truncation of the same finalized tool result reuses the same file path.
	ArtifactKey string
}

// TruncateResult holds the result of truncating tool output.
type TruncateResult struct {
	// Content is the (possibly truncated) output text.
	Content string
	// Truncated is true when the original output exceeded limits.
	Truncated bool
	// SavedPath is the file path where the full output was saved, or "" if
	// it was not truncated or the save failed.
	SavedPath string
	// Hint is a truncation notice (without agent-specific suggestions) that
	// callers can use or augment depending on the agent's capabilities.
	Hint string
	// Preview is the model-facing preview kept inline after truncation.
	Preview string
	// ArtifactReference is the stable reference text for the saved full output.
	ArtifactReference string
}

// defaults fills zero-valued fields with their default values.
func (o TruncateOptions) defaults() TruncateOptions {
	if o.MaxLines <= 0 {
		o.MaxLines = MaxOutputLines
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = MaxOutputBytes
	}
	if o.Direction == "" {
		o.Direction = "head+tail"
	}
	return o
}

// TruncateOutput truncates output that exceeds MaxOutputLines or MaxOutputBytes
// using the default "head+tail" strategy. It is a convenience wrapper around
// TruncateOutputWithOptions and maintains backward compatibility.
func TruncateOutput(output string, sessionDir string) TruncateResult {
	return TruncateOutputWithOptions(output, sessionDir, TruncateOptions{})
}

// countLines returns the number of lines in s, matching strings.Split(s, "\n") boundaries.
func countLines(s string) int {
	if s == "" {
		return 1
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n + 1
}

// buildLineOffsets returns the byte offset of the first byte of each line in s
// (same boundaries as strings.Split(s, "\n")).
func buildLineOffsets(s string) []int {
	if s == "" {
		return []int{0}
	}
	nl := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			nl++
		}
	}
	offs := make([]int, 0, nl+1)
	offs = append(offs, 0)
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			offs = append(offs, i+1)
		}
	}
	return offs
}

func lineByteLen(s string, offs []int, lineIdx int) int {
	start := offs[lineIdx]
	var end int
	if lineIdx+1 < len(offs) {
		end = offs[lineIdx+1] - 1
	} else {
		end = len(s)
	}
	return end - start
}

func cumulativeSizeOffsets(s string, offs []int, n int) int {
	if n <= 0 {
		return 0
	}
	total := 0
	for i := 0; i < n; i++ {
		total += lineByteLen(s, offs, i)
	}
	if n > 1 {
		total += n - 1
	}
	return total
}

func cumulativeSizeLineRange(s string, offs []int, from, to int) int {
	n := to - from
	if n <= 0 {
		return 0
	}
	total := 0
	for i := from; i < to; i++ {
		total += lineByteLen(s, offs, i)
	}
	if n > 1 {
		total += n - 1
	}
	return total
}

func materializeLineRange(s string, offs []int, from, to int) []string {
	if from < 0 {
		from = 0
	}
	if to > len(offs) {
		to = len(offs)
	}
	if from >= to {
		return nil
	}
	out := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		start := offs[i]
		var end int
		if i+1 < len(offs) {
			end = offs[i+1] - 1
		} else {
			end = len(s)
		}
		out = append(out, s[start:end])
	}
	return out
}

func firstLineString(s string, offs []int) string {
	if len(offs) == 0 {
		return ""
	}
	start := offs[0]
	var end int
	if len(offs) > 1 {
		end = offs[1] - 1
	} else {
		end = len(s)
	}
	return s[start:end]
}

func trimLinesToByteLimitOffsets(s string, offs []int, maxBytes int, direction string) []string {
	if direction == "head+tail" {
		return trimLinesToByteLimitHeadTailOffsets(s, offs, maxBytes)
	}
	total := len(offs)
	lo, hi := 0, total
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if cumulativeSizeOffsets(s, offs, mid) <= maxBytes {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return materializeLineRange(s, offs, 0, lo)
}

func trimLinesToByteLimitHeadTailOffsets(s string, offs []int, maxBytes int) []string {
	headBudget := maxBytes * 2 / 5
	tailBudget := maxBytes - headBudget
	total := len(offs)

	headLines := 0
	{
		lo, hi := 0, total
		for lo < hi {
			mid := lo + (hi-lo+1)/2
			if cumulativeSizeOffsets(s, offs, mid) <= headBudget {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		headLines = lo
	}

	remaining := total - headLines
	tailLines := 0
	{
		lo, hi := 0, remaining
		for lo < hi {
			mid := lo + (hi-lo+1)/2
			if cumulativeSizeLineRange(s, offs, total-mid, total) <= tailBudget {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		tailLines = lo
	}

	if headLines == 0 && tailLines == 0 {
		return nil
	}
	if tailLines == 0 {
		return materializeLineRange(s, offs, 0, headLines)
	}
	if headLines == 0 {
		return materializeLineRange(s, offs, total-tailLines, total)
	}
	out := make([]string, 0, headLines+tailLines)
	out = append(out, materializeLineRange(s, offs, 0, headLines)...)
	out = append(out, materializeLineRange(s, offs, total-tailLines, total)...)
	return out
}

func applyDirectionTruncationOffsets(s string, offs []int, totalLines int, opts TruncateOptions, savedPath string) []string {
	omitted := totalLines - opts.MaxLines

	switch opts.Direction {
	case "tail":
		from := totalLines - opts.MaxLines
		if from < 0 {
			from = 0
		}
		kept := materializeLineRange(s, offs, from, totalLines)
		kept = truncateLines(kept)
		marker := truncationMarker(omitted, savedPath)
		return append([]string{marker}, kept...)

	case "head":
		headEnd := opts.MaxLines
		if headEnd > totalLines {
			headEnd = totalLines
		}
		kept := materializeLineRange(s, offs, 0, headEnd)
		kept = truncateLines(kept)
		marker := truncationMarker(omitted, savedPath)
		return append(kept, marker)

	default: // "head+tail"
		headCount := opts.MaxLines * 2 / 5
		if headCount > totalLines {
			headCount = totalLines
		}
		tailCount := opts.MaxLines - headCount
		if tailCount > totalLines-headCount {
			tailCount = totalLines - headCount
		}

		head := truncateLines(materializeLineRange(s, offs, 0, headCount))
		tail := truncateLines(materializeLineRange(s, offs, totalLines-tailCount, totalLines))
		marker := truncationMarker(omitted, savedPath)

		result := make([]string, 0, len(head)+1+len(tail))
		result = append(result, head...)
		result = append(result, marker)
		result = append(result, tail...)
		return result
	}
}

// truncateStringToValidUTF8Prefix returns up to n bytes of s, shortened if needed
// so the result is valid UTF-8.
func truncateStringToValidUTF8Prefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n <= 0 {
		return ""
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// TruncateOutputWithOptions truncates output according to the supplied options.
// When truncation occurs the full output is saved to a file under sessionDir and
// a notice is included in the returned content.
func TruncateOutputWithOptions(output string, sessionDir string, opts TruncateOptions) TruncateResult {
	opts = opts.defaults()

	nLines := countLines(output)
	needsTruncation := nLines > opts.MaxLines || len(output) > opts.MaxBytes

	if !needsTruncation {
		lines := strings.Split(output, "\n")
		truncated := truncateLines(lines)
		content := strings.Join(truncated, "\n")
		return TruncateResult{
			Content: content,
			Preview: content,
		}
	}

	savedPath := saveFullOutput(output, sessionDir, opts.ArtifactKey)
	offs := buildLineOffsets(output)
	lineCount := len(offs)

	var lines []string // nil until byte-trim or final materialize
	if len(output) > opts.MaxBytes {
		firstLine := firstLineString(output, offs)
		lines = trimLinesToByteLimitOffsets(output, offs, opts.MaxBytes, opts.Direction)
		if len(lines) == 0 {
			if len(firstLine) > opts.MaxBytes {
				firstLine = truncateStringToValidUTF8Prefix(firstLine, opts.MaxBytes) + "..."
			}
			lines = []string{firstLine}
		}
		lineCount = len(lines)
	}

	if lineCount > opts.MaxLines {
		if lines != nil {
			lines = applyDirectionTruncation(lines, lineCount, opts, savedPath)
		} else {
			lines = applyDirectionTruncationOffsets(output, offs, lineCount, opts, savedPath)
		}
	} else {
		if lines == nil {
			lines = materializeLineRange(output, offs, 0, lineCount)
		}
		lines = truncateLines(lines)
	}

	content := strings.Join(lines, "\n")
	reference := artifactReference(savedPath)
	hint := "Output truncated."
	if reference != "" {
		hint = "Output truncated. " + reference
	}

	return TruncateResult{
		Content:           content,
		Truncated:         true,
		SavedPath:         savedPath,
		Hint:              hint,
		Preview:           content,
		ArtifactReference: reference,
	}
}

// applyDirectionTruncation selects lines according to the Direction strategy and
// returns the resulting slice (with per-line truncation applied and any
// truncation marker inserted).
func applyDirectionTruncation(lines []string, totalLines int, opts TruncateOptions, savedPath string) []string {
	omitted := totalLines - opts.MaxLines

	switch opts.Direction {
	case "tail":
		from := totalLines - opts.MaxLines
		if from < 0 {
			from = 0
		}
		kept := lines[from:]
		kept = truncateLines(kept)
		marker := truncationMarker(omitted, savedPath)
		return append([]string{marker}, kept...)

	case "head":
		headEnd := opts.MaxLines
		if headEnd > totalLines {
			headEnd = totalLines
		}
		kept := lines[:headEnd]
		kept = truncateLines(kept)
		marker := truncationMarker(omitted, savedPath)
		return append(kept, marker)

	default: // "head+tail"
		headCount := opts.MaxLines * 2 / 5
		if headCount > totalLines {
			headCount = totalLines
		}
		tailCount := opts.MaxLines - headCount
		if tailCount > totalLines-headCount {
			tailCount = totalLines - headCount
		}

		head := truncateLines(lines[:headCount])
		tail := truncateLines(lines[totalLines-tailCount:])
		marker := truncationMarker(omitted, savedPath)

		result := make([]string, 0, headCount+1+tailCount)
		result = append(result, head...)
		result = append(result, marker)
		result = append(result, tail...)
		return result
	}
}

// truncationMarker returns the omission notice inserted between kept sections.
func truncationMarker(omitted int, savedPath string) string {
	if ref := artifactReference(savedPath); ref != "" {
		return fmt.Sprintf(
			"\n\n... [%d lines truncated. %s Use Grep to search or Read with offset/limit to view specific sections.] ...\n",
			omitted, ref,
		)
	}
	return fmt.Sprintf("\n\n... [%d lines truncated] ...\n", omitted)
}

// truncateLines shortens every line that exceeds MaxLineLength UTF-8 bytes,
// aligned to a code-unit boundary.
func truncateLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) > MaxLineLength {
			out[i] = truncateStringToValidUTF8Prefix(l, MaxLineLength) + "..."
		} else {
			out[i] = l
		}
	}
	return out
}

func artifactReference(savedPath string) string {
	if strings.TrimSpace(savedPath) == "" {
		return ""
	}
	return fmt.Sprintf("Full output saved to %s.", savedPath)
}

func sanitizeArtifactKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return ""
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

func deriveArtifactFilename(output, artifactKey string) string {
	if key := sanitizeArtifactKey(artifactKey); key != "" {
		return key + ".log"
	}
	sum := sha256.Sum256([]byte(output))
	return "tool-output-" + hex.EncodeToString(sum[:8]) + ".log"
}

func saveFullOutput(output string, sessionDir string, artifactKey string) string {
	if sessionDir == "" {
		return ""
	}
	toolOutputsDir := sessionToolOutputsDir(sessionDir)
	if toolOutputsDir == "" {
		return ""
	}
	if err := os.MkdirAll(toolOutputsDir, 0o755); err != nil {
		return ""
	}
	filename := deriveArtifactFilename(output, artifactKey)
	p := filepath.Join(toolOutputsDir, filename)

	if existing, err := os.ReadFile(p); err == nil {
		if string(existing) == output {
			return p
		}
		prefix := strings.TrimSuffix(filename, filepath.Ext(filename))
		suffix := filepath.Ext(filename)
		fallbackSum := sha256.Sum256([]byte(output))
		fallback := fmt.Sprintf("%s-%s%s", prefix, hex.EncodeToString(fallbackSum[:8]), suffix)
		p = filepath.Join(toolOutputsDir, fallback)
	}

	if err := writeArtifactFile(p, output); err != nil {
		if existing, readErr := os.ReadFile(p); readErr == nil && string(existing) == output {
			return p
		}
		return ""
	}
	return p
}

func writeArtifactFile(path string, output string) error {
	f, err := openFileNoFollow(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(output); err != nil {
		return err
	}
	return nil
}

func ListArtifactFiles(sessionDir string) ([]string, error) {
	root := sessionToolOutputsDir(sessionDir)
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		out = append(out, filepath.Join(root, entry.Name()))
	}
	sort.Strings(out)
	return out, nil
}
