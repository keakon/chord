package requesttrim

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

func reduceNumberedSourceOutputSummary(ctx Context) string {
	lines := summarizeHeadTailLines(ctx.Content, 6)
	if len(lines) == 0 {
		lines = []string{"- (no preserved source excerpt)"}
	}
	meaningfulLines, rangeNote := numberedSourceStats(ctx.Content)
	return fmt.Sprintf("[Older %s output summarized as numbered source for this request; lines=%d bytes=%d%s]\n%s", ToolNameOrUnknown(ctx.ToolName), meaningfulLines, len(ctx.Content), rangeNote, strings.Join(lines, "\n"))
}

// numberedSourceStats reports the meaningful line count and first→last source
// range in one pass. The range is empty when no line numbers could be parsed.
func numberedSourceStats(content string) (int, string) {
	meaningful := 0
	first, last := 0, 0
	found := false
	forEachLine(content, func(line string) bool {
		if strings.TrimSpace(line) == "" {
			return true
		}
		meaningful++
		num, ok := parseNumberedSourceLine(line)
		if !ok {
			return true
		}
		if !found {
			first = num
			found = true
		}
		last = num
		return true
	})
	if !found {
		return meaningful, ""
	}
	if first == last {
		return meaningful, fmt.Sprintf(" range=%d", first)
	}
	return meaningful, fmt.Sprintf(" range=%d-%d", first, last)
}

// parseNumberedSourceLine extracts the line number from "123 content".
// A number must be followed by whitespace so identifiers like "123abc" are
// not mistaken for source line numbers. Content after the separator may be
// empty because nl still numbers blank source lines.
func parseNumberedSourceLine(line string) (num int, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	digits := 0
	for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits == 0 || digits == len(trimmed) {
		return 0, false
	}
	if trimmed[digits] != ' ' && trimmed[digits] != '\t' {
		return 0, false
	}
	n, err := strconv.Atoi(trimmed[:digits])
	if err != nil {
		return 0, false
	}
	return n, true
}

func looksLikeNumberedSourceOutput(content string) bool {
	matches := 0
	checked := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == "" {
			return true
		}
		checked++
		if numberedSourceLineRe.MatchString(trimmed) {
			matches++
		}
		if matches >= 4 {
			return false
		}
		if checked >= 16 {
			return false
		}
		return true
	})
	return matches >= 4
}

// IsReadLike reports whether a tool produces file or fetched-content evidence.
func IsReadLike(name string) bool {
	return tools.IsReadLike(name)
}

// ReadRequest describes the path and range requested by a read call.
type ReadRequest struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func parseReadRequestSummary(argsJSON string) ReadRequest {
	if strings.TrimSpace(argsJSON) == "" {
		return ReadRequest{}
	}
	var parsed ReadRequest
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		return ReadRequest{}
	}
	parsed.Path = strings.TrimSpace(parsed.Path)
	return parsed
}

// ReadRange describes a parsed read header. OK distinguishes missing or invalid metadata.
type ReadRange struct {
	Start int
	End   int
	Total int
	OK    bool
}

func parseDisplayedReadRange(content string) ReadRange {
	firstLine, rest, hasMore := strings.Cut(content, "\n")
	firstLine = strings.TrimSpace(firstLine)
	if standard := ParseReadResultHeaderRange(firstLine); standard.OK {
		return standard
	}
	if strings.HasPrefix(firstLine, "(showing ") {
		if legacy := parseLegacyDisplayedReadRange(firstLine); legacy.OK {
			return legacy
		}
	}
	if !hasMore || (!strings.Contains(rest, "READ_RESULT") && !strings.Contains(rest, "(showing ")) {
		return ReadRange{}
	}

	var found ReadRange
	forEachLine(rest, func(line string) bool {
		line = strings.TrimSpace(line)
		if standard := ParseReadResultHeaderRange(line); standard.OK {
			found = standard
			return false
		}
		if !strings.HasPrefix(line, "(showing ") {
			return true
		}
		if legacy := parseLegacyDisplayedReadRange(line); legacy.OK {
			found = legacy
			return false
		}
		return true
	})
	return found
}

// ReadResultShowsWholeFile checks whether read metadata reports an untruncated file.
func ReadResultShowsWholeFile(content string) bool {
	displayed := parseDisplayedReadRange(content)
	if displayed.OK {
		return displayed.Start == 1 && displayed.End == displayed.Total && !strings.Contains(content, "truncated="+tools.ReadTruncatedBudget)
	}
	return strings.Contains(content, "READ_RESULT lines=none total=0")
}

// ParseReadResultHeaderRange parses the standard read metadata line without a
// regexp or field-slice allocation. A full reduction scan calls this for every
// read result, so the common path should remain proportional to header length.
func ParseReadResultHeaderRange(line string) ReadRange {
	const prefix = "READ_RESULT"
	if !strings.HasPrefix(line, prefix) || (len(line) > len(prefix) && line[len(prefix)] != ' ' && line[len(prefix)] != '\t') {
		return ReadRange{}
	}
	var out ReadRange
	haveRange := false
	haveTotal := false
	for rest := line[len(prefix):]; len(rest) > 0; {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		field := rest
		if end := strings.IndexAny(rest, " \t"); end >= 0 {
			field = rest[:end]
			rest = rest[end:]
		} else {
			rest = ""
		}
		switch {
		case strings.HasPrefix(field, "lines="):
			rangeText := field[len("lines="):]
			dash := strings.IndexByte(rangeText, '-')
			if dash <= 0 || dash == len(rangeText)-1 {
				continue
			}
			start, startErr := strconv.Atoi(rangeText[:dash])
			end, endErr := strconv.Atoi(rangeText[dash+1:])
			if startErr == nil && endErr == nil && start >= 1 && end >= start {
				out.Start, out.End = start, end
				haveRange = true
			}
		case strings.HasPrefix(field, "total="):
			total, err := strconv.Atoi(field[len("total="):])
			if err == nil && total >= 0 {
				out.Total = total
				haveTotal = true
			}
		}
	}
	out.OK = haveRange && haveTotal
	return out
}

func parseLegacyDisplayedReadRange(line string) ReadRange {
	var out ReadRange
	if n, _ := fmt.Sscanf(line, "(showing lines %d-%d of %d total", &out.Start, &out.End, &out.Total); n == 3 {
		out.OK = true
		return out
	}
	if n, _ := fmt.Sscanf(line, "(showing line %d of %d total", &out.Start, &out.Total); n == 2 {
		out.End = out.Start
		out.OK = true
		return out
	}
	return ReadRange{}
}

// readReductionTruncatedKind picks the READ_RESULT truncation marker and a
// trailing note for a trimmed read output based on its conversation-level
// validity. Reads reach reduction only when invalidated or superseded; the
// distinction matters to the model: stale content must not be trusted, while
// superseded content exists fresher later in context. When the prior content is
// gone, "re-read the file" is not a recovery route at all — re-reading returns
// the replacement — so that case does not offer it.
func readReductionTruncatedKind(ctx Context) (kind, note string) {
	if ctx.ReadSuperseded && !ctx.ReadInvalidated {
		return tools.ReadTruncatedSuperseded, "[A newer read of this range appears later in this conversation; prefer that output.]"
	}
	if ctx.ReadPriorContentLost {
		return tools.ReadTruncatedStale, "[File replaced after this read; re-reading returns the current content, not the version above.]"
	}
	return tools.ReadTruncatedStale, "[File modified after this read; the content above may be outdated. Re-read before relying on it.]"
}

func reduceReadOutputSummary(ctx Context) string {
	content := ctx.Content
	displayed := parseDisplayedReadRange(content)
	body := stripReadResultHeaderLine(content)
	kind, note := readReductionTruncatedKind(ctx)

	// Preferred path: a READ_RESULT range is present, so rebuild the same header
	// the read tool emits, marked with the validity-specific truncation kind and
	// keeping only leading lines.
	if displayed.OK && strings.TrimSpace(body) != "" {
		headLines, headEnd := reduceReadHeadLines(body, displayed.Start, displayed.End, reduceSnippetChars)
		if len(headLines) > 0 {
			linesField := fmt.Sprintf("%d-%d", displayed.Start, headEnd)
			header := tools.FormatReadResultHeader(linesField, displayed.Total, kind, "", "")
			out := header
			out += "\n" + strings.Join(headLines, "\n")
			if note != "" {
				out += "\n" + note
			}
			return out
		}
	}

	// Fallback for read output without a parseable range (e.g. legacy sessions
	// or non-paged content): keep the path hint and a short excerpt.
	request := ctx.Meta.ParsedReadRequest()
	snippet := strings.TrimSpace(TextSnippet(content, reduceSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	if note != "" {
		snippet += "\n" + note
	}
	requestedRange := ""
	if request.Offset > 0 || request.Limit > 0 {
		requestedRange = fmt.Sprintf("; requested range: offset=%d limit=%d", request.Offset, request.Limit)
	}
	if request.Path == "" {
		return "[Older " + tools.NameRead + " output truncated for this request to save context" + requestedRange + "]\n" + snippet
	}
	return fmt.Sprintf("[Older %s output truncated for this request to save context; path=%q%s]\n%s", tools.NameRead, request.Path, requestedRange, snippet)
}

// stripReadResultHeaderLine returns content without its leading READ_RESULT
// metadata line, leaving only the raw source body.
func stripReadResultHeaderLine(content string) string {
	if before, after, ok := strings.Cut(content, "\n"); ok {
		if ParseReadResultHeaderRange(strings.TrimSpace(before)).OK {
			return after
		}
	}
	return content
}

// reduceReadHeadLines keeps whole leading lines of a read body within a rough
// character budget, never splitting a line. startLine is the 1-based source
// line of the first body line; endLine bounds the head so it never claims more
// than the original range. It returns the surviving head lines and the source
// line number of the last kept line.
func reduceReadHeadLines(body string, startLine, endLine, budget int) ([]string, int) {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) == 0 {
		return nil, startLine
	}
	maxLines := endLine - startLine + 1
	if maxLines > 0 && maxLines < len(lines) {
		lines = lines[:maxLines]
	}

	kept := 0
	used := 0
	for kept < len(lines) {
		cost := len(lines[kept]) + 1
		if kept > 0 && used+cost > budget {
			break
		}
		used += cost
		kept++
	}
	return lines[:kept], startLine + kept - 1
}

func reduceReadLikeOutputSummary(ctx Context) string {
	switch strings.TrimSpace(ctx.ToolName) {
	case tools.NameRead:
		return reduceReadOutputSummary(ctx)
	case tools.NameWebFetch:
		return reduceWebFetchOutputSummary(ctx.Meta.Args, ctx.Content)
	default:
		return staleOutputOmittedMarker(ctx.ToolName)
	}
}

func reduceWebFetchOutputSummary(argsJSON, content string) string {
	var parsed struct {
		URL       string `json:"url"`
		Raw       bool   `json:"raw"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &parsed)
	snippetSource := stripWebFetchResultHeaders(content)
	snippet := strings.TrimSpace(TextSnippet(snippetSource, reduceSnippetChars))
	if snippet == "" {
		snippet = "(no preserved excerpt)"
	}
	h := contentFingerprint(content)
	return fmt.Sprintf(
		"[Older %s output truncated for this request to save context; url=%q raw=%t timeout_ms=%d content_fnv1a64=%016x]\n%s",
		tools.NameWebFetch,
		strings.TrimSpace(parsed.URL),
		parsed.Raw,
		parsed.TimeoutMs,
		h,
		snippet,
	)
}

// contentFingerprint returns a compact non-cryptographic fingerprint of an
// output, used only to detect whether a re-fetch returns changed content.
// It runs on the request hot path: fnv1a-64 over the raw bytes, formatted
// inline by the caller so no intermediate string is allocated.
func contentFingerprint(content string) uint64 {
	const (
		fnvOffset64 = 14695981039346656037
		fnvPrime64  = 1099511628211
	)
	h := uint64(fnvOffset64)
	for i := 0; i < len(content); i++ {
		h ^= uint64(content[i])
		h *= fnvPrime64
	}
	return h
}

func stripWebFetchResultHeaders(content string) string {
	if _, after, ok := strings.Cut(content, "\n\n"); ok {
		return after
	}
	return content
}
