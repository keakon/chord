package requesttrim

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

// LooksLikeSearchResult recognizes search-result shapes in tool output.
func LooksLikeSearchResult(ctx Context) bool {
	switch ctx.ToolName {
	case tools.NameGrep, tools.NameGlob:
		return true
	default:
		return looksLikeSearchResultContent(ctx.Content)
	}
}

func looksLikeSearchResultContent(content string) bool {
	matches := 0
	checked := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		checked++
		if _, _, _, ok := parseSearchResultLine(trimmed); ok {
			matches++
		}
		if matches >= 2 {
			return false
		}
		if checked >= 24 {
			return false
		}
		return true
	})
	return matches >= 2
}

func parseSearchResultLine(line string) (path, lineNo, snippet string, ok bool) {
	trimmed := strings.TrimSpace(line)
	path, lineNo, snippet, ok = tools.ParseGrepMatchLine(trimmed)
	if !ok || !strings.HasPrefix(trimmed, "\"") && !looksLikeSearchResultPath(path) {
		return "", "", "", false
	}
	return path, lineNo, strings.TrimSpace(snippet), true
}

// looksLikeSearchResultPath rejects the `path` half of a `path:line:text`
// candidate when it cannot plausibly be a file path. Without it a timestamped
// log line — `12:34:56 INFO ...`, or `2026-09-06 12:34:56 ...` — parses as a
// hit at line 34 of a file named "12", two such lines make the whole output
// look like a search result, and the summary then asserts a match count and a
// preserved query for output that never was a search.
func looksLikeSearchResultPath(path string) bool {
	if path == "" || strings.ContainsAny(path, " \t") {
		return false
	}
	digitsOnly := true
	for i := 0; i < len(path); i++ {
		if !isASCIIDigit(path[i]) {
			digitsOnly = false
			break
		}
	}
	if digitsOnly {
		return false
	}
	// Real hits name a file: either a directory component or an extension.
	return strings.ContainsAny(path, "/\\.")
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func reduceSearchLikeOutputSummary(ctx Context) string {
	toolName := ToolNameOrUnknown(ctx.ToolName)
	snippetLines := summarizeSearchResultLocations(ctx.Content, searchSummaryByteBudget)
	if len(snippetLines) == 0 {
		snippetLines = summarizeSearchResultLines(ctx.Content, 6)
	}
	if len(snippetLines) == 0 {
		snippetLines = []string{"- (no preserved matches)"}
	}
	scope := reduceSearchScope(ctx)
	return fmt.Sprintf("[Older %s results summarized for this request to save context; %s; matches=%d]\n%s", toolName, scope, countSearchResultMatches(ctx.Content), strings.Join(snippetLines, "\n"))
}

// countSearchResultMatches counts the path:line:text entries of a search
// output, so notes, footers and the path-line-text surrounding lines of a
// context_lines search are not reported as matches. Outputs without such
// entries (glob, rg -l) list one result per line and fall back to counting
// non-empty lines.
func countSearchResultMatches(content string) int {
	matches := 0
	forEachLine(content, func(line string) bool {
		if _, _, _, ok := parseSearchResultLine(line); ok {
			matches++
		}
		return true
	})
	if matches == 0 {
		return countMeaningfulLines(content)
	}
	return matches
}

const (
	// searchSummaryByteBudget bounds the location listing of a reduced search
	// result. Within it every matched file keeps its line numbers: the
	// path:line list — not the snippets — is what a multi-site task acts on,
	// and session data shows >3000B search outputs carry ~75 matches at the
	// median, which fits comfortably as locations but not as snippet groups.
	searchSummaryByteBudget      = 1400
	searchSummaryMaxLinesPerFile = 8
	searchSummarySnippetGroups   = 3
)

type searchLocationGroup struct {
	path    string
	lines   []string
	matches int
	snippet string
}

// summarizeSearchResultLocations renders search output as a location-first
// listing: every matched path with its line numbers (capped per file), the
// first few groups keeping one snippet as a content reminder, all within
// byteBudget. Outputs without path:line matches (glob, rg -l) become a plain
// budget-bounded line list. Returns nil when nothing was parseable.
func summarizeSearchResultLocations(content string, byteBudget int) []string {
	if byteBudget <= 0 {
		return nil
	}
	groups := make([]searchLocationGroup, 0)
	groupByPath := make(map[string]int)
	var plain []string
	plainTotal := 0
	totalMatches := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		path, lineNo, snippet, ok := parseSearchResultLine(trimmed)
		if !ok {
			plainTotal++
			if len(plain) < 64 {
				plain = append(plain, TextSnippet(trimmed, SummaryLineSnippetChars))
			}
			return true
		}
		totalMatches++
		idx, seen := groupByPath[path]
		if !seen {
			idx = len(groups)
			groupByPath[path] = idx
			g := searchLocationGroup{path: path}
			// Only the leading snippet groups ever render their snippet;
			// building one per path is wasted allocation on large outputs.
			if idx < searchSummarySnippetGroups {
				g.snippet = TextSnippet(snippet, 100)
			}
			groups = append(groups, g)
		}
		g := &groups[idx]
		g.matches++
		if len(g.lines) < searchSummaryMaxLinesPerFile && (len(g.lines) == 0 || g.lines[len(g.lines)-1] != lineNo) {
			g.lines = append(g.lines, lineNo)
		}
		return true
	})
	if len(groups) == 0 {
		if len(plain) == 0 {
			return nil
		}
		out := make([]string, 0, len(plain))
		used := 0
		for _, line := range plain {
			cost := len(line) + 3
			if used+cost > byteBudget {
				break
			}
			out = append(out, "- "+strings.ReplaceAll(line, "\n", " "))
			used += cost
		}
		for omitted := plainTotal - len(out); omitted > 0; omitted++ {
			marker := fmt.Sprintf("- ... (+%d lines omitted) ...", omitted)
			if used+len(marker)+1 <= byteBudget {
				out = append(out, marker)
				break
			}
			if len(out) == 0 {
				break
			}
			last := out[len(out)-1]
			out = out[:len(out)-1]
			used -= len(last) + 1
		}
		return out
	}
	out := make([]string, 0, min(len(groups), 32))
	used := 0
	renderedMatches := 0
	for i := range groups {
		g := &groups[i]
		line := "- " + g.path + ": " + strings.Join(g.lines, ", ")
		if extra := g.matches - len(g.lines); extra > 0 {
			line += fmt.Sprintf(" (+%d more)", extra)
		}
		if len(out) < searchSummarySnippetGroups && g.snippet != "" {
			line += " — " + strings.ReplaceAll(g.snippet, "\n", " ")
		}
		cost := len(line) + 1
		if used+cost > byteBudget {
			break
		}
		out = append(out, line)
		used += cost
		renderedMatches += g.matches
	}
	// Non-match lines mixed into a path:line output (tool truncation notes,
	// invalid-regex warnings) are not rendered; account for them in the tail
	// so the reader cannot mistake the listed matches for the complete output.
	otherLinesNote := ""
	if plainTotal > 0 {
		otherLinesNote = fmt.Sprintf(", %d other lines", plainTotal)
	}
	for {
		omittedFiles := len(groups) - len(out)
		if omittedFiles == 0 && plainTotal == 0 {
			return out
		}
		var marker string
		if omittedFiles > 0 {
			marker = fmt.Sprintf("- ... (+%d files, %d matches%s omitted) ...", omittedFiles, totalMatches-renderedMatches, otherLinesNote)
		} else {
			marker = fmt.Sprintf("- ... (+%d other lines omitted) ...", plainTotal)
		}
		if used+len(marker)+1 <= byteBudget {
			out = append(out, marker)
			return out
		}
		if len(out) == 0 {
			return out
		}
		last := out[len(out)-1]
		out = out[:len(out)-1]
		used -= len(last) + 1
		renderedMatches -= groups[len(out)].matches
	}
}

func reduceSearchScope(ctx Context) string {
	switch ctx.ToolName {
	case tools.NameGrep:
		var parsed struct {
			Pattern  string   `json:"pattern"`
			Paths    []string `json:"paths"`
			Includes []string `json:"includes"`
		}
		_ = json.Unmarshal([]byte(ctx.Meta.Args), &parsed)
		paths := reduceSearchList(parsed.Paths, ".")
		includes := reduceSearchList(parsed.Includes, "")
		return fmt.Sprintf("pattern=%q paths=%q includes=%q", strings.TrimSpace(parsed.Pattern), paths, includes)
	case tools.NameGlob:
		var parsed struct {
			Patterns []string `json:"patterns"`
			Path     string   `json:"path"`
		}
		_ = json.Unmarshal([]byte(ctx.Meta.Args), &parsed)
		path := strings.TrimSpace(parsed.Path)
		if path == "" {
			path = "."
		}
		return fmt.Sprintf("patterns=%q path=%q", reduceSearchList(parsed.Patterns, ""), path)
	default:
		return "query preserved"
	}
}

func reduceSearchList(values []string, fallback string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, ",")
}

type searchSummaryGroup struct {
	Path    string
	Matches []string
}

func summarizeSearchResultLines(content string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	groups := make([]searchSummaryGroup, 0)
	groupByPath := make(map[string]int)
	fallback := make([]string, 0, limit)
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		path, lineNo, snippet, ok := parseSearchResultLine(trimmed)
		if !ok {
			if len(fallback) < limit {
				fallback = append(fallback, renderSummaryLine(trimmed))
			}
			return true
		}
		idx, ok := groupByPath[path]
		if !ok {
			idx = len(groups)
			groupByPath[path] = idx
			groups = append(groups, searchSummaryGroup{Path: path})
		}
		if len(groups[idx].Matches) < 2 {
			groups[idx].Matches = append(groups[idx].Matches, fmt.Sprintf("%s: %s", lineNo, TextSnippet(snippet, 140)))
		}
		return true
	})
	if len(groups) == 0 {
		return fallback
	}
	out := make([]string, 0, limit)
	for _, group := range groups {
		if len(group.Matches) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("- %s: %s", group.Path, strings.Join(group.Matches, "; ")))
		if len(out) >= limit {
			break
		}
	}
	if omitted := len(groups) - len(out); omitted > 0 && len(out) > 0 {
		out[len(out)-1] += fmt.Sprintf("; ... (+%d files)", omitted)
	}
	return out
}
