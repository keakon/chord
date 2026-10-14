package requesttrim

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/keakon/chord/internal/tooloutput"
)

var (
	numberedSourceLineRe = regexp.MustCompile(`^\s*\d+\s+(?:\S|$)`)
	pathListLikelyFileRe = regexp.MustCompile(`(?:^|/)[^/\s]+\.[A-Za-z0-9_+.-]+$`)
	diffHunkHeaderLineRe = regexp.MustCompile(`^@@@?\s+-\d+(?:,\d+)?(?:\s+-\d+(?:,\d+)?)*\s+\+\d+(?:,\d+)?\s+@@@?`)
)

// Marker keyword sets shared by the context-reduction heuristics. They are kept
// as distinct package-level slices (rather than one merged list) because each
// caller scans for a deliberately different signal; centralizing them here keeps
// the variants visible side by side so a future edit is less likely to miss one.
var (
	// highRiskMarkers flag tool output that must be protected from reduction
	// while still recent (stack traces, auth failures, assertion mismatches).
	highRiskMarkers = []string{
		"traceback",
		"panic:",
		"exception",
		"segmentation fault",
		"permission denied",
		"access denied",
		"unauthorized",
		"forbidden",
		"expected:",
		"actual:",
		"assertion failed",
		"assert failed",
		// Go and Rust test runners report a failing case as "--- FAIL: Name",
		// which contains neither "failed" nor any other marker above.
		"--- fail",
		"npm err!",
		"fatal:",
	}
	// buildLogMarkers classify build/test/lint output as a long log worth
	// signal-based summarization rather than blanket omission.
	buildLogMarkers = []string{
		"error",
		"warning",
		// "fail" rather than "failed"/"failure": test runners print bare
		// "FAIL", "--- FAIL:" and "failing", none of which the longer forms
		// match, and a log whose only failure evidence is such a line would
		// otherwise summarize to "no preserved log lines".
		"fail",
		"panic:",
		"traceback",
		"exception",
		"diagnostics:",
	}
	// logLineMarkers select representative lines to preserve from a long log.
	logLineMarkers = []string{
		"error",
		"warning",
		"fail",
		"panic:",
		"traceback",
		"exception",
	}
	// importantLineMarkers select lines to preserve when summarizing an error
	// output, covering both failure and auth/timeout signals.
	importantLineMarkers = []string{
		"error",
		"warning",
		"fail",
		"panic:",
		"traceback",
		"exception",
		"fatal:",
		"expected",
		"actual",
		"assert",
		"permission",
		"denied",
		"unauthorized",
		"forbidden",
		"timeout",
	}
	// shellSuccessWeakLineMarkers select compact evidence from old successful
	// shell output. These weaker substring signals are considered after stronger
	// result markers so early progress lines don't crowd out final status.
	shellSuccessWeakLineMarkers = []string{
		"generated",
		"wrote",
		"written",
		"created",
		"updated",
		"skipped",
		"deprecated",
		"summary",
		"completed",
	}
)

func containsAnyMarker(lower string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func forEachLine(content string, visit func(string) bool) {
	for len(content) > 0 {
		line := content
		if idx := strings.IndexByte(content, '\n'); idx >= 0 {
			line = content[:idx]
			content = content[idx+1:]
		} else {
			content = ""
		}
		if !visit(line) {
			return
		}
	}
}

// staleOutputOmittedMarker is the fallback rendering for an output whose
// class-specific summarizer produced nothing usable. Callers pass whichever
// name their class tracks: the reduction switch reads the recorded call meta,
// while the read-like path reads the resolved tool name.
func staleOutputOmittedMarker(toolName string) string {
	return fmt.Sprintf("[Older %s output omitted from this request to save context.]", ToolNameOrUnknown(toolName))
}

// reduceGenericStaleOutputSummary routes a stale tool output to the best
// content-shaped summary before falling back to a head/tail excerpt. It is a
// defensive router keyed on content, not on the tool name, so the same arms
// stay valid no matter which caller reaches it. Some arms (e.g. numbered
// source) are not reachable from the current classify path because that path
// already screens the same shape, but they are kept so the router behaves
// correctly if a future caller routes here without that pre-screen.
func reduceGenericStaleOutputSummary(ctx Context) string {
	toolName := ToolNameOrUnknown(ctx.ToolName)
	switch {
	case looksLikeSearchResultContent(ctx.Content):
		lines := summarizeSearchResultLines(ctx.Content, 4)
		if len(lines) == 0 {
			break
		}
		return fmt.Sprintf("[Older %s output summarized as search-like content for this request; matches=%d]\n%s", toolName, countMeaningfulLines(ctx.Content), strings.Join(lines, "\n"))
	case looksLikeNumberedSourceOutput(ctx.Content):
		return reduceNumberedSourceOutputSummary(ctx)
	case looksLikePathListOutput(ctx.Content):
		lines := summarizeHeadTailLines(ctx.Content, 6)
		if len(lines) == 0 {
			break
		}
		return fmt.Sprintf("[Older %s path-list output summarized for this request; paths=%d bytes=%d]\n%s", toolName, countMeaningfulLines(ctx.Content), len(ctx.Content), strings.Join(lines, "\n"))
	}
	lines := summarizeHeadTailLines(ctx.Content, 4)
	if len(lines) == 0 {
		lines = []string{"- (no preserved excerpt)"}
	}
	return fmt.Sprintf("[Older %s output summarized for this request to save context; bytes=%d lines=%d]\n%s", toolName, len(ctx.Content), countMeaningfulLines(ctx.Content), strings.Join(lines, "\n"))
}

func summarizeHeadTailLines(content string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	head := limit / 2
	tail := limit - head
	headLines := make([]string, 0, head)
	tailLines := make([]string, 0, tail)
	meaningful := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		meaningful++
		if len(headLines) < head {
			headLines = append(headLines, trimmed)
			return true
		}
		if tail > 0 {
			if len(tailLines) < tail {
				tailLines = append(tailLines, trimmed)
			} else {
				copy(tailLines, tailLines[1:])
				tailLines[tail-1] = trimmed
			}
		}
		return true
	})
	if meaningful == 0 {
		return nil
	}
	if meaningful <= limit {
		out := make([]string, 0, meaningful)
		for _, line := range headLines {
			out = append(out, renderSummaryLine(line))
		}
		for _, line := range tailLines {
			out = append(out, renderSummaryLine(line))
		}
		return out
	}
	out := make([]string, 0, limit+1)
	for _, line := range headLines {
		out = append(out, renderSummaryLine(line))
	}
	omitted := meaningful - limit
	noun := "lines"
	if omitted == 1 {
		noun = "line"
	}
	out = append(out, fmt.Sprintf("- ... (%d %s omitted) ...", omitted, noun))
	for _, line := range tailLines {
		out = append(out, renderSummaryLine(line))
	}
	return out
}

// renderSummaryLine renders one preserved summary line. Artifact reference
// lines ("Full output saved to <path>") stay verbatim: mid-line truncation
// would clip the artifact path and leave the model an unreadable path.
func renderSummaryLine(line string) string {
	if refs := tooloutput.ExtractArtifactReferences(line); len(refs) > 0 {
		return "- " + refs[0]
	}
	return "- " + strings.ReplaceAll(TextSnippet(line, SummaryLineSnippetChars), "\n", " ")
}

func looksLikePathListOutput(content string) bool {
	matches := 0
	checked := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		checked++
		if pathListLikelyFileRe.MatchString(trimmed) {
			matches++
		}
		if matches >= 5 {
			return false
		}
		if checked >= 24 {
			return false
		}
		return true
	})
	return matches >= 5
}

func countMeaningfulLines(content string) int {
	count := 0
	forEachLine(content, func(line string) bool {
		if strings.TrimSpace(line) != "" {
			count++
		}
		return true
	})
	return count
}
