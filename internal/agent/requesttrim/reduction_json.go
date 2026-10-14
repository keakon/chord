package requesttrim

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

func looksLikeStructuredJSON(content string) bool {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) < 2 {
		return false
	}
	return (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) || (strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))
}

// looksLikeJSONLinesLog reports NDJSON-shaped output (each early line its own
// JSON object), e.g. `go test -json` or structured build logs. That is log
// streaming, not a JSON document the model reads values from over several
// requests, so it earns no extended JSON retention.
func looksLikeJSONLinesLog(content string) bool {
	lines := 0
	jsonLines := 0
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return true
		}
		lines++
		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			jsonLines++
		}
		return lines < 3
	})
	return lines >= 2 && jsonLines == lines
}

func reduceJSONBlobSummary(ctx Context) (string, bool) {
	var decoded any
	dec := json.NewDecoder(strings.NewReader(ctx.Content))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return "", false
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", false
	}
	switch v := decoded.(type) {
	case map[string]any:
		lines := summarizeJSONObjectEntries(v)
		if len(lines) == 0 {
			lines = []string{"- (no preserved entries)"}
		}
		return fmt.Sprintf("[Older %s JSON object summarized to save context; keys=%d]\n%s", ToolNameOrUnknown(ctx.ToolName), len(v), strings.Join(lines, "\n")), true
	case []any:
		items := summarizeJSONArrayItems(v, 3)
		if len(items) == 0 {
			items = []string{"- (no preserved items)"}
		}
		return fmt.Sprintf("[Older %s JSON array summarized to save context; items=%d]\n%s", ToolNameOrUnknown(ctx.ToolName), len(v), strings.Join(items, "\n")), true
	default:
		return "", false
	}
}

// summarizeJSONObjectEntries keeps scalar values before nested containers — the
// exact fields a model most often re-reads across requests — while rendering
// containers as shape markers.
func summarizeJSONObjectEntries(v map[string]any) []string {
	scalarKeys := make([]string, 0, len(v))
	containerKeys := make([]string, 0, len(v))
	for key, value := range v {
		switch value.(type) {
		case []any, map[string]any:
			containerKeys = append(containerKeys, key)
		default:
			scalarKeys = append(scalarKeys, key)
		}
	}
	slices.Sort(scalarKeys)
	slices.Sort(containerKeys)
	const maxEntries = 8
	scalarCount := min(len(scalarKeys), maxEntries)
	containerCount := min(len(containerKeys), maxEntries-scalarCount)
	omitted := len(v) - scalarCount - containerCount
	out := make([]string, 0, scalarCount+containerCount+min(omitted, 1))
	for _, key := range scalarKeys[:scalarCount] {
		out = append(out, "- "+summarizeJSONKey(key)+": "+summarizeJSONValue(v[key]))
	}
	for _, key := range containerKeys[:containerCount] {
		out = append(out, "- "+summarizeJSONKey(key)+": "+summarizeJSONValue(v[key]))
	}
	if omitted > 0 {
		out = append(out, fmt.Sprintf("- ... (+%d more keys omitted) ...", omitted))
	}
	return out
}

func summarizeJSONKey(key string) string {
	return strconv.Quote(TextSnippet(key, SummaryLineSnippetChars))
}

// summarizeJSONValue renders a decoded JSON value as a compact summary.
// Scalar values keep their content (truncated); nested containers collapse to
// a shape marker so the summary stays small while still identifying the value
// kind.
func summarizeJSONValue(value any) string {
	switch t := value.(type) {
	case nil:
		return "null"
	case string:
		return strconv.Quote(TextSnippet(t, SummaryLineSnippetChars))
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return strings.ReplaceAll(TextSnippet(t.String(), SummaryLineSnippetChars), "\n", " ")
	case []any:
		return fmt.Sprintf("[%d items]", len(t))
	case map[string]any:
		return summarizeJSONNestedKeys(t)
	default:
		if raw, err := json.Marshal(value); err == nil {
			return strings.ReplaceAll(TextSnippet(string(raw), SummaryLineSnippetChars), "\n", " ")
		}
		return ""
	}
}

// summarizeJSONNestedKeys renders a nested object's key set as a compact shape
// marker; its values are deliberately omitted (the top-level pass already kept
// the scalar values the model reads directly).
func summarizeJSONNestedKeys(v map[string]any) string {
	if len(v) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	const maxNested = 5
	shown := min(len(keys), maxNested)
	for i := range shown {
		keys[i] = summarizeJSONKey(keys[i])
	}
	s := "{" + strings.Join(keys[:shown], ", ")
	if len(keys) > maxNested {
		s += fmt.Sprintf(", ... (+%d more)", len(keys)-maxNested)
	}
	return s + "}"
}

func summarizeJSONArrayItems(items []any, limit int) []string {
	if limit <= 0 || len(items) == 0 {
		return nil
	}
	if limit > len(items) {
		limit = len(items)
	}
	// Sample first/middle/last rather than a naive head slice: arrays are often
	// time-ordered or sorted, but the representative item can sit anywhere.
	// Keeping both ends plus an interior item covers more shapes than three
	// head items, which systematically miss the tail.
	out := make([]string, 0, limit)
	for i := range limit {
		idx := 0
		if limit > 1 {
			idx = (len(items) - 1) * i / (limit - 1)
		}
		rendered, _ := json.Marshal(items[idx])
		out = append(out, fmt.Sprintf("- [%d] %s", idx, strings.ReplaceAll(TextSnippet(string(rendered), SummaryLineSnippetChars), "\n", " ")))
	}
	return out
}
