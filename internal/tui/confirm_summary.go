package tui

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

type confirmRiskLevel int

const (
	confirmRiskLow confirmRiskLevel = iota
	confirmRiskMedium
	confirmRiskHigh
)

type confirmSummary struct {
	ToolName       string
	Action         string
	Risk           confirmRiskLevel
	Warnings       []string
	Fields         []confirmSummaryField
	ParseErr       error
	RawJSON        string
	NeedsApproval  []string
	AlreadyAllowed []string
}

type confirmSummaryField struct {
	Label              string
	SummaryValue       string
	DetailValue        string
	Important          bool
	PreserveWhitespace bool
	Multiline          bool
	PreferSoftWrap     bool
}

func (r confirmRiskLevel) String() string {
	switch r {
	case confirmRiskLow:
		return "Low"
	case confirmRiskHigh:
		return "High"
	default:
		return "Medium"
	}
}

func (f confirmSummaryField) value(detailed bool) string {
	if detailed && f.DetailValue != "" {
		return f.DetailValue
	}
	if f.SummaryValue != "" {
		return f.SummaryValue
	}
	return f.DetailValue
}

func (s confirmSummary) summaryFields() []confirmSummaryField {
	fields := make([]confirmSummaryField, 0, len(s.Fields))
	for _, field := range s.Fields {
		if field.Important {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return append([]confirmSummaryField(nil), s.Fields...)
	}
	return fields
}

func buildConfirmSummary(toolName, argsJSON string, needsApproval, alreadyAllowed []string) confirmSummary {
	summary := confirmSummary{
		ToolName: toolName,
		Action:   confirmActionText(toolName),
		Risk:     confirmRiskForTool(toolName),
		RawJSON:  argsJSON,
	}

	parsed, err := parseConfirmArgs(argsJSON)
	if err != nil {
		summary.ParseErr = err
		summary.Warnings = append(summary.Warnings, "Unable to parse arguments; showing raw payload")
		raw := strings.TrimSpace(argsJSON)
		if raw == "" {
			raw = "(empty)"
		}
		summary.Fields = []confirmSummaryField{
			newConfirmPreviewField("Arguments (raw)", raw, true, 4, 12),
		}
		return summary
	}

	switch strings.ToLower(toolName) {
	case "bash":
		buildBashConfirmSummary(&summary, parsed)
	case "edit":
		buildEditConfirmSummary(&summary, parsed)
	case "write":
		buildWriteConfirmSummary(&summary, parsed)
	case "delete":
		buildDeleteConfirmSummary(&summary, parsed, needsApproval, alreadyAllowed)
	case "webfetch":
		buildWebFetchConfirmSummary(&summary, parsed)
	default:
		buildGenericConfirmSummary(&summary, parsed)
	}

	ensureConfirmImportantFields(&summary)
	if len(summary.Fields) == 0 {
		summary.Fields = []confirmSummaryField{newConfirmField("Arguments", "(none)", true)}
	}
	return summary
}

func parseConfirmArgs(argsJSON string) (map[string]any, error) {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, err
	}
	if parsed == nil {
		parsed = map[string]any{}
	}
	return parsed, nil
}

func confirmActionText(toolName string) string {
	switch strings.ToLower(toolName) {
	case "bash":
		return "Execute shell command"
	case "spawn":
		return "Start background process"
	case "spawnstop":
		return "Stop background process"
	case "edit":
		return "Replace text in file"
	case "write":
		return "Write file contents"
	case "delete":
		return "Delete files"
	case "webfetch":
		return "Fetch URL"
	case "read":
		return "Read file"
	case "grep":
		return "Search file contents"
	case "glob":
		return "Find matching files"
	case "lsp":
		return "Query language server"
	default:
		if toolName == "" {
			return "Execute tool"
		}
		return "Execute " + toolName
	}
}

func confirmRiskForTool(toolName string) confirmRiskLevel {
	switch strings.ToLower(toolName) {
	case "bash", "spawn", "spawnstop":
		return confirmRiskHigh
	case "edit", "write", "delete":
		return confirmRiskMedium
	case "read", "grep", "glob", "lsp", "webfetch":
		return confirmRiskLow
	default:
		return confirmRiskMedium
	}
}

func buildBashConfirmSummary(summary *confirmSummary, parsed map[string]any) {
	handled := map[string]bool{}
	summary.Warnings = append(summary.Warnings, "High risk: shell execution")

	if description, ok := confirmString(parsed, "description"); ok && strings.TrimSpace(description) != "" {
		handled["description"] = true
		appendConfirmField(&summary.Fields, newConfirmField("Description", description, true))
	}

	command, _ := confirmString(parsed, "command")
	handled["command"] = true
	if command == "" {
		command = "(missing command)"
	}
	appendConfirmField(&summary.Fields, newConfirmLiteralField("Command", command, true))

	workdir, _ := confirmString(parsed, "workdir")
	handled["workdir"] = true
	if workdir == "" {
		workdir = "current directory"
	}
	appendConfirmField(&summary.Fields, newConfirmField("Workdir", workdir, true))

	requestedTimeout, ok := confirmInt(parsed, "timeout")
	handled["timeout"] = true
	if !ok {
		requestedTimeout = 0
	}

	timeoutInfo := tools.ResolveBashTimeoutValue(requestedTimeout, ok)
	appendConfirmField(&summary.Fields, newConfirmField("Timeout", fmt.Sprintf("%ds", timeoutInfo.EffectiveSec), true))
	if timeoutInfo.Clamped {
		summary.Warnings = append(summary.Warnings, fmt.Sprintf("Requested timeout %ds capped to %ds", timeoutInfo.RequestedSec, timeoutInfo.EffectiveSec))
	} else if timeoutInfo.EffectiveSec > 60 {
		summary.Warnings = append(summary.Warnings, fmt.Sprintf("Long timeout configured (%ds)", timeoutInfo.EffectiveSec))
	}

	appendUnhandledConfirmFields(summary, parsed, handled)
}

func buildEditConfirmSummary(summary *confirmSummary, parsed map[string]any) {
	handled := map[string]bool{}
	summary.Warnings = append(summary.Warnings, "Edits existing file content")

	filePath, ok := confirmString(parsed, "path")
	handled["path"] = true
	if !ok || filePath == "" {
		if targetFile, targetOK := confirmString(parsed, "TargetFile"); targetOK {
			filePath = targetFile
			handled["TargetFile"] = true
		}
	}
	if filePath == "" {
		filePath = "(unspecified)"
	}
	appendConfirmField(&summary.Fields, newConfirmField("File", filePath, true))

	replaceAll, ok := confirmBool(parsed, "replace_all")
	handled["replace_all"] = true
	if !ok {
		replaceAll = false
	}
	appendConfirmField(&summary.Fields, newConfirmField("Replace all", confirmYesNo(replaceAll), true))
	if replaceAll {
		summary.Warnings = append(summary.Warnings, "Will replace all matching occurrences")
	}

	if oldText, ok := confirmString(parsed, "old_string"); ok {
		handled["old_string"] = true
		appendConfirmField(&summary.Fields, newConfirmPreviewField("Old text preview", oldText, true, 2, 6))
	}
	if newText, ok := confirmString(parsed, "new_string"); ok {
		handled["new_string"] = true
		appendConfirmField(&summary.Fields, newConfirmPreviewField("New text preview", newText, true, 2, 6))
	}

	appendUnhandledConfirmFields(summary, parsed, handled)
}

func buildWriteConfirmSummary(summary *confirmSummary, parsed map[string]any) {
	handled := map[string]bool{}
	summary.Warnings = append(summary.Warnings, "Will overwrite file contents")

	filePath, _ := confirmString(parsed, "path")
	handled["path"] = true
	if filePath == "" {
		filePath = "(unspecified)"
	}
	appendConfirmField(&summary.Fields, newConfirmField("File", filePath, true))

	content, ok := confirmString(parsed, "content")
	handled["content"] = true
	if ok {
		appendConfirmField(&summary.Fields, newConfirmPreviewField("Content preview", content, true, 3, 8))
	}

	appendUnhandledConfirmFields(summary, parsed, handled)
}

func buildDeleteConfirmSummary(summary *confirmSummary, parsed map[string]any, needsApproval, alreadyAllowed []string) {
	handled := map[string]bool{}
	summary.Warnings = append(summary.Warnings, "Will permanently remove files")
	summary.NeedsApproval = append([]string(nil), needsApproval...)
	summary.AlreadyAllowed = append([]string(nil), alreadyAllowed...)

	filePaths := confirmStringSlice(parsed, "paths")
	handled["paths"] = true
	count := len(filePaths)
	if count == 0 {
		summary.Action = "Delete files"
		appendConfirmField(&summary.Fields, newConfirmField("Files", "(unspecified)", true))
	} else {
		if count == 1 {
			summary.Action = "Delete file"
			appendConfirmField(&summary.Fields, newConfirmField("File", filePaths[0], true))
		} else {
			summary.Action = fmt.Sprintf("Delete %d files", count)
			appendConfirmField(&summary.Fields, newConfirmLiteralField("Files", strings.Join(filePaths, "\n"), true))
		}
	}

	if reason, ok := confirmString(parsed, "reason"); ok {
		handled["reason"] = true
		appendConfirmField(&summary.Fields, newConfirmLiteralField("Reason", reason, true))
	}

	appendUnhandledConfirmFields(summary, parsed, handled)
}

func buildWebFetchConfirmSummary(summary *confirmSummary, parsed map[string]any) {
	handled := map[string]bool{}
	summary.Warnings = append(summary.Warnings, "Network request")

	url, _ := confirmString(parsed, "url")
	handled["url"] = true
	if url == "" {
		url = "(unspecified)"
	}
	appendConfirmField(&summary.Fields, newConfirmField("URL", url, true))

	timeout, ok := confirmInt(parsed, "timeout")
	handled["timeout"] = true
	if !ok || timeout <= 0 {
		timeout = 30
	}
	appendConfirmField(&summary.Fields, newConfirmField("Timeout", fmt.Sprintf("%ds", timeout), true))

	if raw, ok := confirmBool(parsed, "raw"); ok {
		handled["raw"] = true
		appendConfirmField(&summary.Fields, newConfirmField("Raw response", confirmYesNo(raw), false))
	}

	appendUnhandledConfirmFields(summary, parsed, handled)
}

func buildGenericConfirmSummary(summary *confirmSummary, parsed map[string]any) {
	priority := []string{"path", "paths", "reason", "url", "command", "workdir", "timeout", "limit", "offset", "pattern", "glob"}
	seen := map[string]bool{}
	for _, key := range priority {
		value, ok := parsed[key]
		if !ok {
			continue
		}
		appendConfirmField(&summary.Fields, confirmFieldForKey(key, value, true))
		seen[key] = true
	}

	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		if seen[key] {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		appendConfirmField(&summary.Fields, confirmFieldForKey(key, parsed[key], false))
	}
}

func ensureConfirmImportantFields(summary *confirmSummary) {
	for _, field := range summary.Fields {
		if field.Important {
			return
		}
	}
	for i := range summary.Fields {
		if i >= 6 {
			break
		}
		summary.Fields[i].Important = true
	}
}

func appendUnhandledConfirmFields(summary *confirmSummary, parsed map[string]any, handled map[string]bool) {
	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		if handled[key] {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		appendConfirmField(&summary.Fields, confirmFieldForKey(key, parsed[key], false))
	}
}

func confirmFieldForKey(key string, value any, important bool) confirmSummaryField {
	label := confirmFieldLabel(key)
	switch key {
	case "command":
		return newConfirmLiteralField(label, confirmFormatValue(value), important)
	case "content":
		return newConfirmPreviewField("Content preview", confirmFormatValue(value), important, 3, 8)
	case "old_string":
		return newConfirmPreviewField("Old text preview", confirmFormatValue(value), important, 2, 6)
	case "new_string":
		return newConfirmPreviewField("New text preview", confirmFormatValue(value), important, 2, 6)
	case "paths":
		return newConfirmLiteralField("Files", confirmFormatValue(value), important)
	default:
		return newConfirmField(label, confirmFormatValue(value), important)
	}
}

func confirmFieldLabel(key string) string {
	switch key {
	case "path":
		return "File"
	case "paths":
		return "Files"
	case "reason":
		return "Reason"
	case "url":
		return "URL"
	case "workdir":
		return "Workdir"
	case "replace_all":
		return "Replace all"
	case "old_string":
		return "Old text"
	case "new_string":
		return "New text"
	case "raw":
		return "Raw response"
	default:
		return key
	}
}

func newConfirmField(label, value string, important bool) confirmSummaryField {
	return confirmSummaryField{
		Label:        label,
		SummaryValue: value,
		DetailValue:  value,
		Important:    important,
	}
}

func newConfirmLiteralField(label, value string, important bool) confirmSummaryField {
	return confirmSummaryField{
		Label:              label,
		SummaryValue:       value,
		DetailValue:        value,
		Important:          important,
		PreserveWhitespace: true,
		Multiline:          true,
		PreferSoftWrap:     true,
	}
}

func newConfirmPreviewField(label, value string, important bool, summaryLines, detailLines int) confirmSummaryField {
	return confirmSummaryField{
		Label:              label,
		SummaryValue:       confirmPreviewText(value, summaryLines),
		DetailValue:        confirmPreviewText(value, detailLines),
		Important:          important,
		PreserveWhitespace: true,
		Multiline:          true,
	}
}

func appendConfirmField(dst *[]confirmSummaryField, field confirmSummaryField) {
	value := strings.TrimSpace(field.value(true))
	if value == "" {
		return
	}
	*dst = append(*dst, field)
}

func confirmPreviewText(text string, maxLines int) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if maxLines <= 0 || text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text
	}
	truncated := append([]string(nil), lines[:maxLines]...)
	truncated = append(truncated, "...")
	return strings.Join(truncated, "\n")
}

func confirmFormatValue(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return v
	case []string:
		return strings.Join(v, "\n")
	case bool:
		return strconv.FormatBool(v)
	case float64:
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		buf, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(buf)
	}
}

func confirmString(parsed map[string]any, key string) (string, bool) {
	value, ok := parsed[key]
	if !ok {
		return "", false
	}
	str, ok := value.(string)
	return str, ok
}

func confirmStringSlice(parsed map[string]any, key string) []string {
	value, ok := parsed[key]
	if !ok {
		return nil
	}
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		str, ok := item.(string)
		if !ok || strings.TrimSpace(str) == "" {
			continue
		}
		out = append(out, str)
	}
	return out
}

func confirmBool(parsed map[string]any, key string) (bool, bool) {
	value, ok := parsed[key]
	if !ok {
		return false, false
	}
	b, ok := value.(bool)
	return b, ok
}

func confirmInt(parsed map[string]any, key string) (int, bool) {
	value, ok := parsed[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

func confirmYesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func confirmRiskStyle(risk confirmRiskLevel) string {
	switch risk {
	case confirmRiskLow:
		return ConfirmAllowStyle.Render(risk.String())
	case confirmRiskHigh:
		return ConfirmDenyStyle.Render(risk.String())
	default:
		return ConfirmEditStyle.Render(risk.String())
	}
}

func renderConfirmFields(fields []confirmSummaryField, width int, detailed bool) []string {
	lines := make([]string, 0, len(fields)*2)
	for _, field := range fields {
		lines = append(lines, renderConfirmField(field, width, detailed)...)
	}
	return lines
}

func renderConfirmField(field confirmSummaryField, width int, detailed bool) []string {
	value := field.value(detailed)
	if value == "" {
		value = "(empty)"
	}
	label := field.Label
	combined := label + ": " + firstDisplayLine(value)
	if !field.Multiline && !strings.Contains(value, "\n") && runewidth.StringWidth(combined) <= width {
		return []string{DimStyle.Render(label+": ") + ConfirmToolStyle.Render(value)}
	}

	wrappedWidth := width - 2
	if wrappedWidth < 10 {
		wrappedWidth = width
	}
	var wrapped []string
	switch {
	case field.PreserveWhitespace && field.PreferSoftWrap:
		wrapped = wrapConfirmLiteralText(value, wrappedWidth)
	case field.PreserveWhitespace:
		wrapped = wrapIndentedText(value, wrappedWidth)
	default:
		wrapped = wrapText(value, wrappedWidth)
	}

	lines := []string{DimStyle.Render(label + ":")}
	for _, line := range wrapped {
		lines = append(lines, "  "+ConfirmToolStyle.Render(line))
	}
	return lines
}

func wrapConfirmLiteralText(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if text == "" {
		return []string{""}
	}

	var result []string
	for _, rawLine := range strings.Split(text, "\n") {
		if rawLine == "" {
			result = append(result, "")
			continue
		}
		indentCount := countLeadingWhitespace(rawLine)
		indent := rawLine[:indentCount]
		rest := rawLine[indentCount:]
		indentWidth := ansi.StringWidth(indent)
		available := width - indentWidth
		if available <= 0 {
			available = width
			indent = ""
			indentWidth = 0
		}
		for _, segment := range wrapConfirmLiteralLine(rest, available) {
			if indentWidth > 0 {
				result = append(result, indent+segment)
			} else {
				result = append(result, segment)
			}
		}
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func wrapConfirmLiteralLine(line string, width int) []string {
	if width <= 0 {
		return []string{line}
	}
	if ansi.StringWidth(line) <= width {
		return []string{line}
	}

	var result []string
	var cur strings.Builder
	curWidth := 0

	for i := 0; i < len(line); {
		spaceStart := i
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		spacing := line[spaceStart:i]

		tokenStart := i
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		token := line[tokenStart:i]
		if token == "" {
			continue
		}

		sep := ""
		sepWidth := 0
		if cur.Len() > 0 && spacing != "" {
			sep = spacing
			sepWidth = ansi.StringWidth(spacing)
		}
		tokenWidth := ansi.StringWidth(token)
		if curWidth+sepWidth+tokenWidth <= width {
			if sep != "" {
				cur.WriteString(sep)
				curWidth += sepWidth
			}
			cur.WriteString(token)
			curWidth += tokenWidth
			continue
		}

		if cur.Len() > 0 {
			result = append(result, cur.String())
			cur.Reset()
			curWidth = 0
		}

		if tokenWidth <= width {
			cur.WriteString(token)
			curWidth = tokenWidth
			continue
		}

		wrappedToken := wrapConfirmLiteralToken(token, width)
		if len(wrappedToken) == 0 {
			continue
		}
		if len(wrappedToken) > 1 {
			result = append(result, wrappedToken[:len(wrappedToken)-1]...)
		}
		cur.WriteString(wrappedToken[len(wrappedToken)-1])
		curWidth = ansi.StringWidth(wrappedToken[len(wrappedToken)-1])
	}

	if cur.Len() > 0 {
		result = append(result, cur.String())
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func wrapConfirmLiteralToken(token string, width int) []string {
	if width <= 0 {
		return []string{token}
	}
	if token == "" {
		return []string{""}
	}
	if ansi.StringWidth(token) <= width {
		return []string{token}
	}

	var result []string
	remaining := token
	for ansi.StringWidth(remaining) > width {
		cut := confirmLiteralWrapCut(remaining, width)
		if cut <= 0 || cut >= len(remaining) {
			prefix := ansi.Cut(remaining, 0, width)
			if prefix == "" {
				prefix, remaining = splitFirstRune(remaining)
				result = append(result, prefix)
				continue
			}
			result = append(result, prefix)
			remaining = remaining[len(prefix):]
			continue
		}
		result = append(result, remaining[:cut])
		remaining = remaining[cut:]
	}
	if remaining != "" {
		result = append(result, remaining)
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func confirmLiteralWrapCut(line string, width int) int {
	best := -1
	currentWidth := 0
	for idx, r := range line {
		rw := ansi.StringWidth(string(r))
		if currentWidth+rw > width {
			break
		}
		currentWidth += rw
		next := idx + utf8.RuneLen(r)
		if isConfirmLiteralWrapBoundary(r) {
			best = next
		}
	}
	if best > 0 {
		return best
	}
	prefix := ansi.Cut(line, 0, width)
	return len(prefix)
}

func isConfirmLiteralWrapBoundary(r rune) bool {
	switch r {
	case ' ', '\t', '/', '\\', '=', ':', ',', ';', '|', '&', '(', ')', '[', ']', '{', '}':
		return true
	default:
		return false
	}
}

func splitFirstRune(s string) (string, string) {
	if s == "" {
		return "", ""
	}
	_, size := utf8.DecodeRuneInString(s)
	if size <= 0 {
		return s[:1], s[1:]
	}
	return s[:size], s[size:]
}
