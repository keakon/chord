package agent

import (
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/llm"
)

// Compaction-archive message index.
//
// A compaction archive (history-N.md) can span hundreds of messages; the
// checkpoint wrapper tells the model to read the archive's index and then
// only the line ranges it needs instead of reading the whole file through
// truncation. The index lists every message segment with its start line, a
// short first-line snippet, and a LARGE marker for oversized tool output.
//
// The scanner below parses the markdown produced by session.ExportToMarkdown
// (internal/session/export.go). That writer is the source of truth for the
// format; the scanner keys off the exported convformat labels and the
// `---` block separators, so it stays in step as long as the writer keeps
// starting each message segment with one of the recognised markers.
const (
	// archiveIndexHeading marks the index section prepended to an archive.
	archiveIndexHeading = "# Index"
	// archiveIndexIntro is the fixed guidance under the heading (two lines).
	archiveIndexIntro = "Message segments in this file, newest at the bottom. Read only the line ranges you need; a single read call is capped around 2000 lines, so slice by this index instead of reading the whole file."
	// archiveIndexLargeLines flags tool-result segments at or above this many
	// lines as LARGE so the model does not read them by accident.
	archiveIndexLargeLines = 40
	// archiveIndexSnippetRunes caps the quoted first-line snippet.
	archiveIndexSnippetRunes = 60
)

// archiveSegment is one message-level segment of an archive's markdown.
type archiveSegment struct {
	// mdLine is the 1-based start line inside the archive body (before the
	// index is prepended). Final line numbers add the index block height.
	mdLine int
	// kind is one of "User", "User (terminal)", "Assistant", "Tool result".
	kind string
	// name is the tool name for Tool result segments.
	name string
	// lines is the number of body lines the segment spans.
	lines int
	// snippet is the quoted first content line, when the segment has one.
	snippet string
}

// buildCompactionArchiveIndexedMarkdown returns md with a message-segment
// index prepended. Segment start lines in the index reference line numbers in
// the returned document. Returns md unchanged when no segment is found.
func buildCompactionArchiveIndexedMarkdown(md string) string {
	segs := scanCompactionArchiveSegments(md)
	if len(segs) == 0 {
		return md
	}
	index := renderCompactionArchiveIndex(segs, 0)
	shift := strings.Count(index, "\n") + 2 // blank separator line between index and body
	index = renderCompactionArchiveIndex(segs, shift)
	return index + "\n\n" + md
}

// scanCompactionArchiveSegments walks the archive markdown and returns one
// segment per message block. A new segment starts at a recognised message
// label line (User:, Assistant:, TERMINAL (!):, # Tool result: <name>) that
// directly follows a `---` block separator, which is how the export writer
// opens every message block. Requiring both conditions keeps pasted content
// that happens to contain a label or a divider from splitting segments.
func scanCompactionArchiveSegments(md string) []archiveSegment {
	lines := strings.Split(md, "\n")
	var segs []archiveSegment
	dividerPending := false
	for i, line := range lines {
		if line == "---" {
			dividerPending = true
			continue
		}
		if !dividerPending {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue // skip blank lines between the divider and the label
		}
		dividerPending = false
		kind, name, ok := archiveSegmentStartKind(line)
		if !ok {
			continue // not a message boundary; content noise after a divider
		}
		segs = append(segs, archiveSegment{mdLine: i + 1, kind: kind, name: name})
	}
	// Fill each segment's span and snippet now that every boundary is known.
	for s := range segs {
		start := segs[s].mdLine - 1
		end := len(lines) // exclusive; the export trailer is not a message
		if s+1 < len(segs) {
			end = segs[s+1].mdLine - 1
		}
		// Trim trailing blank lines (including the phantom empty element from
		// a trailing newline) so line counts describe real content.
		for end > start && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		segs[s].lines = end - start
		// Snippets come from the body, skipping the block label line itself.
		segs[s].snippet = firstContentSnippet(lines[start+1 : end])
	}
	return segs
}

// archiveSegmentStartKind classifies a message start line. ok is false for
// lines that are not message boundaries.
func archiveSegmentStartKind(line string) (kind, name string, ok bool) {
	switch {
	case line == convformat.LabelUser:
		return "User", "", true
	case line == convformat.LabelAssistant:
		return "Assistant", "", true
	case line == convformat.LabelLocalShell:
		return "User (terminal)", "", true
	case strings.HasPrefix(line, "# Tool result: "):
		return "Tool result", strings.TrimPrefix(line, "# Tool result: "), true
	case strings.HasPrefix(line, "# Tool call: "):
		// Tool calls render inside their assistant block; one only heads a
		// segment when the writer's block starts with it, so still indexed.
		return "Tool call", strings.TrimPrefix(line, "# Tool call: "), true
	default:
		return "", "", false
	}
}

// firstContentSnippet returns the first content line of a segment body for the
// index entry: non-empty and not the Thinking: label itself (its body lines
// are ordinary content — an assistant message that leads with reasoning quotes
// its first reasoning line, which still identifies the segment).
func firstContentSnippet(body []string) string {
	for _, line := range body {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line == convformat.LabelThinking {
			continue
		}
		return quoteArchiveSnippet(strings.TrimSpace(line))
	}
	return ""
}

// quoteArchiveSnippet renders a snippet as a quoted single line, collapsed to
// a rune-safe length.
func quoteArchiveSnippet(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	if s == "" {
		return ""
	}
	return `"` + llm.TruncateStringRunes(s, archiveIndexSnippetRunes, "…") + `"`
}

// renderCompactionArchiveIndex composes the index block. Line numbers are the
// segment mdLine plus shift (0 for measuring the block's own height, then the
// real shift once the block height is known).
func renderCompactionArchiveIndex(segs []archiveSegment, shift int) string {
	var sb strings.Builder
	sb.WriteString(archiveIndexHeading)
	sb.WriteByte('\n')
	sb.WriteString(archiveIndexIntro)
	sb.WriteByte('\n')
	for _, seg := range segs {
		sb.WriteString("\n- ")
		sb.WriteString(renderArchiveIndexEntry(seg, shift))
	}
	return sb.String()
}

// renderArchiveIndexEntry renders one index line, e.g.
// "123: Tool result: Read (2100 lines, LARGE)".
func renderArchiveIndexEntry(seg archiveSegment, shift int) string {
	var sb strings.Builder
	sb.WriteString(strconv.Itoa(seg.mdLine + shift))
	sb.WriteString(": ")
	sb.WriteString(seg.kind)
	switch seg.kind {
	case "User", "User (terminal)", "Assistant":
		if seg.snippet != "" {
			sb.WriteString(": ")
			sb.WriteString(seg.snippet)
		}
	case "Tool result", "Tool call":
		if seg.name != "" {
			sb.WriteString(": ")
			sb.WriteString(seg.name)
		}
		if seg.lines > 0 {
			sb.WriteString(" (")
			sb.WriteString(strconv.Itoa(seg.lines))
			sb.WriteString(" line")
			if seg.lines != 1 {
				sb.WriteString("s")
			}
			if seg.lines >= archiveIndexLargeLines {
				sb.WriteString(", LARGE")
			}
			sb.WriteString(")")
		}
	}
	return sb.String()
}
