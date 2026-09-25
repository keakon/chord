package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/privatefs"
)

const (
	// MaxOutputLines is the maximum number of lines kept in an over-budget
	// preview.
	MaxOutputLines = 2000
	// MaxOutputBytes is the maximum inline byte length before truncation is
	// triggered.
	MaxOutputBytes = 50 * 1024
	// MaxLineLength is the maximum UTF-8 byte length per line in an over-budget
	// preview (suffix aligned to a valid UTF-8 boundary).
	MaxLineLength = 2000
	// ArtifactReferencePrefix starts every model-facing reference to a full
	// tool output saved outside the inline result.
	ArtifactReferencePrefix = "Full output saved to "
	// ArtifactReadGuidance is appended to ordinary truncated-tool references.
	ArtifactReadGuidance = "Only if the preview is insufficient, use grep first or read with offset/limit for needed ranges; use a script/parser for huge single-line structured output. Do not read the entire output by default."

	maxArtifactReferencePathBytes = 4096

	truncationMarkerOpen   = "... ["
	linesOmittedNotice     = " lines omitted"
	showingLinesNotice     = "; showing lines "
	firstLineTruncatedHead = "line 1 truncated to "
	bytesNotice            = " bytes"
)

// TruncateOptions controls how output truncation is performed. An over-budget
// preview keeps the first 40% and the last 60% of the line and byte budgets.
type TruncateOptions struct {
	// MaxLines is the maximum number of lines to keep in an over-budget preview.
	// It defaults to MaxOutputLines (2000).
	MaxLines int
	// MaxBytes is the maximum inline byte length before truncation is triggered,
	// not counting a single trailing newline.
	// It defaults to MaxOutputBytes (50KB).
	MaxBytes int
	// ArtifactKey enables idempotent artifact storage. When non-empty, repeated
	// truncation of the same finalized tool result reuses the same file path.
	ArtifactKey string
}

// TruncateResult holds the result of truncating tool output.
type TruncateResult struct {
	// Content is the (possibly truncated) output text.
	Content string
	// Truncated is true when Content is an over-budget preview rather than the
	// exact full output.
	Truncated bool
	// SavedPath is the file path where the full output was saved, or "" if
	// it was not truncated or the save failed.
	SavedPath string
	// Hint is a truncation notice (without agent-specific suggestions) that
	// callers can use or augment depending on the agent's capabilities.
	Hint string
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
	return o
}

// buildLineOffsets returns the byte offset of the first byte of each line in
// s, counting lines the way wc -l does: a trailing newline terminates the last
// line instead of starting an empty one. The byte range of that last line
// therefore includes its newline, and empty output has no lines.
func buildLineOffsets(s string) []int {
	if s == "" {
		return nil
	}
	offs := make([]int, 0, strings.Count(s, "\n")+1)
	offs = append(offs, 0)
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			offs = append(offs, i+1)
		}
	}
	// A trailing newline terminated the last line; it does not begin an
	// empty one, so the offset entry after that newline is dropped and the
	// last line's byte range keeps the newline itself.
	if strings.HasSuffix(s, "\n") {
		offs = offs[:len(offs)-1]
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

// buildLineSizePrefix returns prefix sums of line byte lengths:
// prefix[0] is 0 and prefix[i+1] is prefix[i] plus lineByteLen(i).
func buildLineSizePrefix(s string, offs []int) []int {
	prefix := make([]int, len(offs)+1)
	for i := range offs {
		prefix[i+1] = prefix[i] + lineByteLen(s, offs, i)
	}
	return prefix
}

func cumulativeSizeOffsets(prefix []int, n int) int {
	if n <= 0 {
		return 0
	}
	total := prefix[n]
	if n > 1 {
		total += n - 1
	}
	return total
}

func cumulativeSizeLineRange(prefix []int, from, to int) int {
	n := to - from
	if n <= 0 {
		return 0
	}
	total := prefix[to] - prefix[from]
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

// firstLineString returns the first line without its line terminator, even
// when it is the output's only line (whose byte range keeps the newline).
func firstLineString(s string, offs []int) string {
	if len(offs) == 0 {
		return ""
	}
	if len(offs) > 1 {
		return s[offs[0] : offs[1]-1]
	}
	return strings.TrimSuffix(s[offs[0]:], "\n")
}

// previewWindow selects the lines shown inline: head lines from the front of
// the output and tail lines from the back. fallback reports that not even one
// complete line fits the byte budget, so the caller must keep a byte-truncated
// first line instead.
func previewWindow(s string, offs []int, opts TruncateOptions) (head, tail int, fallback bool) {
	total := len(offs)
	headBudget := opts.MaxBytes * 2 / 5
	tailBudget := opts.MaxBytes - headBudget
	headLimit := opts.MaxLines * 2 / 5
	tailLimit := opts.MaxLines - headLimit

	prefix := buildLineSizePrefix(s, offs)
	head = min(fitHeadLines(prefix, headBudget), headLimit)
	// Joining nonempty head and tail windows inserts one additional newline.
	// Reserve it before selecting the tail so retaining every line cannot
	// exceed MaxBytes while claiming that the unchanged output was truncated.
	if head > 0 {
		tailBudget = max(tailBudget-1, 0)
	}
	tail = min(fitTailLines(prefix, tailBudget, total-head), tailLimit)
	return head, tail, head+tail == 0
}

// fitHeadLines returns how many lines from the front of the output fit within
// maxBytes.
func fitHeadLines(prefix []int, maxBytes int) int {
	lo, hi := 0, len(prefix)-1
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if cumulativeSizeOffsets(prefix, mid) <= maxBytes {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// fitTailLines returns how many lines from the back of the output fit within
// maxBytes, counting at most remaining lines.
func fitTailLines(prefix []int, maxBytes, remaining int) int {
	total := len(prefix) - 1
	remaining = min(remaining, total)
	lo, hi := 0, remaining
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if cumulativeSizeLineRange(prefix, total-mid, total) <= maxBytes {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
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
	// Single forward pass: the original loop re-validated the whole prefix per
	// trimmed byte, which is quadratic for binary output. The longest valid
	// UTF-8 prefix ends either at the first invalid sequence or at the start
	// of a rune the boundary splits.
	i := 0
	for i < n {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return s[:i]
		}
		if i+size > n {
			return s[:i]
		}
		i += size
	}
	return s[:n]
}

// TruncateOutputWithOptions truncates output according to the supplied options.
// When truncation occurs the full output is saved to a file under sessionDir and
// a notice is included in the returned content.
func TruncateOutputWithOptions(output string, sessionDir string, opts TruncateOptions) TruncateResult {
	opts = opts.defaults()

	// Keep every result that fits the inline byte budget verbatim. Line count and
	// per-line limits shape only the preview of an output that already exceeds
	// the byte budget; applying them independently forces needless artifact
	// rereads for otherwise small results such as search responses with one long
	// embedded snippet. A single trailing newline is not counted: it is the
	// only byte a preview of such an output could drop, so an output that fits
	// without it stays verbatim instead of claiming a truncation that omits
	// nothing.
	needsTruncation := len(strings.TrimSuffix(output, "\n")) > opts.MaxBytes

	if !needsTruncation {
		return TruncateResult{Content: output}
	}

	savedPath := saveFullOutput(output, sessionDir, opts.ArtifactKey)
	offs := buildLineOffsets(output)
	totalLines := len(offs)

	head, tail, fallback := previewWindow(output, offs, opts)
	var content string
	if fallback {
		// No complete line fits the byte budget; keep a readable slice of the
		// first line, cut once to the smaller of the byte budget and the
		// per-line limit. The ellipsis is part of the cut, not a decoration: a
		// first line that fits whole must not wear a truncation marker on
		// itself. The marker reports the byte cut and the omitted lines and
		// carries the artifact reference, so a caller that only reads Content
		// keeps both.
		firstLine := firstLineString(output, offs)
		var notice string
		if limit := min(opts.MaxBytes, MaxLineLength); len(firstLine) > limit {
			kept := truncateStringToValidUTF8Prefix(firstLine, limit)
			notice = firstLineTruncatedNotice(len(kept), len(firstLine))
			if totalLines > 1 {
				notice += "; " + linesOmittedCount(totalLines-1, totalLines)
			}
			firstLine = kept + "..."
		} else {
			notice = linesOmittedCount(totalLines-1, totalLines) + showingLinesNotice + keptLineRanges(totalLines, 1, 0)
		}
		content = firstLine + "\n" + truncationMarkerNotice(notice, savedPath)
	} else {
		kept := truncateLines(materializeLineRange(output, offs, 0, head))
		if omitted := totalLines - head - tail; omitted > 0 {
			kept = append(kept, truncationMarker(omitted, totalLines, keptLineRanges(totalLines, head, tail), savedPath))
		}
		kept = append(kept, truncateLines(materializeLineRange(output, offs, totalLines-tail, totalLines))...)
		content = strings.Join(kept, "\n")
	}

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
		ArtifactReference: reference,
	}
}

// truncationMarker returns the omission notice inserted between the kept
// sections. omitted and totalLines describe the full output; ranges names the
// kept line sections (empty when they cannot be described).
func truncationMarker(omitted, totalLines int, ranges, savedPath string) string {
	notice := linesOmittedCount(omitted, totalLines)
	if ranges != "" {
		notice += showingLinesNotice + ranges
	}
	return truncationMarkerNotice(notice, savedPath)
}

// truncationMarkerNotice wraps notice as an omission marker followed by the
// artifact reference when the full output was saved.
func truncationMarkerNotice(notice, savedPath string) string {
	if ref := artifactReference(savedPath); ref != "" {
		return fmt.Sprintf("%s%s. %s] ...", truncationMarkerOpen, notice, ref)
	}
	return fmt.Sprintf("%s%s] ...", truncationMarkerOpen, notice)
}

func linesOmittedCount(omitted, totalLines int) string {
	return fmt.Sprintf("%d of %d%s", omitted, totalLines, linesOmittedNotice)
}

// firstLineTruncatedNotice reports that the only line shown inline was cut to
// kept of total bytes.
func firstLineTruncatedNotice(kept, total int) string {
	return fmt.Sprintf("%s%d of %d%s", firstLineTruncatedHead, kept, total, bytesNotice)
}

// keptLineRanges renders the kept head/tail sections as 1-based inclusive line
// numbers, e.g. "1-48 and 517-588".
func keptLineRanges(total, head, tail int) string {
	switch {
	case head > 0 && tail > 0:
		return lineRangeLabel(1, head) + " and " + lineRangeLabel(total-tail+1, total)
	case head > 0:
		return lineRangeLabel(1, head)
	case tail > 0:
		return lineRangeLabel(total-tail+1, total)
	default:
		return ""
	}
}

func lineRangeLabel(from, to int) string {
	if from == to {
		return fmt.Sprintf("%d", from)
	}
	return fmt.Sprintf("%d-%d", from, to)
}

// truncateLines shortens every line that exceeds MaxLineLength UTF-8 bytes,
// aligned to a code-unit boundary. The output's last line keeps its trailing
// newline (see buildLineOffsets); the newline is not line content, so it
// neither counts toward the limit nor gets cut.
func truncateLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		body, newline := strings.CutSuffix(l, "\n")
		if len(body) > MaxLineLength {
			out[i] = truncateStringToValidUTF8Prefix(body, MaxLineLength) + "..."
			if newline {
				out[i] += "\n"
			}
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
	return fmt.Sprintf("%s%s. %s", ArtifactReferencePrefix, savedPath, ArtifactReadGuidance)
}

func shortArtifactReference(savedPath string) string {
	if strings.TrimSpace(savedPath) == "" {
		return ""
	}
	return ArtifactReferencePrefix + savedPath + "."
}

// ExtractArtifactReferences returns canonical truncated-output references from
// content. It returns only the bounded reference text, never the surrounding
// line, so an ordinary long line that happens to contain the marker cannot
// bypass a caller's summary limit.
func ExtractArtifactReferences(content string) []string {
	var refs []string
	seen := make(map[string]struct{})
	for len(content) > 0 {
		line := content
		if idx := strings.IndexByte(content, '\n'); idx >= 0 {
			line = content[:idx]
			content = content[idx+1:]
		} else {
			content = ""
		}
		ref, ok := extractArtifactReferenceLine(line)
		if !ok {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func extractArtifactReferenceLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	before, rest, ok := strings.Cut(trimmed, ArtifactReferencePrefix)
	if !ok {
		return "", false
	}
	guidedSuffix := ". " + ArtifactReadGuidance
	if path, _, ok := strings.Cut(rest, guidedSuffix); ok {
		if before != "" && !isTruncationMarkerReferencePrefix(before) {
			return "", false
		}
		path = strings.TrimSpace(path)
		if !validArtifactReferencePath(path) {
			return "", false
		}
		return ArtifactReferencePrefix + path + guidedSuffix, true
	}
	if before != "" || !strings.HasSuffix(rest, ".") {
		return "", false
	}
	path := strings.TrimSpace(strings.TrimSuffix(rest, "."))
	if !validArtifactReferencePath(path) {
		return "", false
	}
	return ArtifactReferencePrefix + path + ".", true
}

func validArtifactReferencePath(path string) bool {
	return path != "" && len(path) <= maxArtifactReferencePathBytes && !strings.ContainsAny(path, "\r\n")
}

// isTruncationMarkerReferencePrefix reports whether the text preceding an
// artifact reference is one of Chord's own omission notices.
func isTruncationMarkerReferencePrefix(prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	if !strings.HasPrefix(prefix, truncationMarkerOpen) || !strings.HasSuffix(prefix, ".") {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(prefix, truncationMarkerOpen), ".")
	if rest, ok := strings.CutPrefix(body, firstLineTruncatedHead); ok {
		rest, ok = cutCountPair(rest, bytesNotice)
		if !ok {
			return false
		}
		if rest == "" {
			return true
		}
		body, ok = strings.CutPrefix(rest, "; ")
		if !ok {
			return false
		}
	}
	rest, ok := cutCountPair(body, linesOmittedNotice)
	// Everything after the counts is descriptive; the digits already make this
	// shape specific enough to reject ordinary prose.
	return ok && (rest == "" || strings.HasPrefix(rest, showingLinesNotice))
}

// cutCountPair parses a leading "<digits> of <digits><unit>" and returns the
// text after unit.
func cutCountPair(s, unit string) (string, bool) {
	first, rest, ok := strings.Cut(s, " of ")
	if !ok || !isDecimalDigits(first) {
		return "", false
	}
	second, rest, ok := strings.Cut(rest, unit)
	if !ok || !isDecimalDigits(second) {
		return "", false
	}
	return rest, true
}

func isDecimalDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
	if err := privatefs.EnsureDir(sessionDir, toolOutputsDir); err != nil {
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

	if err := writeArtifactFile(sessionDir, p, output); err != nil {
		if existing, readErr := os.ReadFile(p); readErr == nil && string(existing) == output {
			return p
		}
		return ""
	}
	return p
}

func writeArtifactFile(sessionDir, path string, output string) error {
	f, err := privatefs.OpenFile(sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_EXCL)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(output); err != nil {
		return err
	}
	return nil
}
