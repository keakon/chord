package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

func (b *Block) toolArgsParsed() (keys []string, vals map[string]string) {
	if b == nil {
		return nil, nil
	}
	if b.toolArgsCacheToolName == b.ToolName && b.toolArgsCacheContent == b.Content {
		return b.toolArgsCacheKeys, b.toolArgsCacheVals
	}
	keys, vals = parseToolArgs(b.Content)
	b.toolArgsCacheToolName = b.ToolName
	b.toolArgsCacheContent = b.Content
	b.toolArgsCacheKeys = append(b.toolArgsCacheKeys[:0], keys...)
	if vals == nil {
		b.toolArgsCacheVals = nil
	} else {
		if b.toolArgsCacheVals == nil {
			b.toolArgsCacheVals = make(map[string]string, len(vals))
		} else {
			for k := range b.toolArgsCacheVals {
				delete(b.toolArgsCacheVals, k)
			}
		}
		for k, v := range vals {
			b.toolArgsCacheVals[k] = v
		}
		vals = b.toolArgsCacheVals
	}
	b.toolHeaderCacheToolName = ""
	b.toolHeaderCacheContent = ""
	b.toolHeaderCacheHeaderParams = ""
	b.toolHeaderCacheHeaderParamsOK = false
	b.toolHeaderCacheHeaderMain = ""
	b.toolHeaderCacheHeaderGray = ""
	b.toolHeaderCacheHeaderPartsOK = false
	b.toolHeaderCacheCollapsedMain = ""
	b.toolHeaderCacheCollapsedGray = ""
	b.toolHeaderCacheCollapsedOK = false
	b.toolHeaderCacheCollapsedReady = false
	b.toolHeaderCacheParamLines = nil
	b.toolHeaderCacheParamLinesOK = false
	keys = b.toolArgsCacheKeys
	return keys, vals
}

func (b *Block) toolHeaderMeta() (paramSummary, mainPart, grayPart, collapsedMain, collapsedGray string, collapsedOK bool, paramLines []string) {
	if b == nil {
		return "", "", "", "", "", false, nil
	}
	keys, vals := b.toolArgsParsed()
	if b.toolHeaderCacheToolName != b.ToolName || b.toolHeaderCacheContent != b.Content {
		b.toolHeaderCacheToolName = b.ToolName
		b.toolHeaderCacheContent = b.Content
		b.toolHeaderCacheHeaderParams = ""
		b.toolHeaderCacheHeaderParamsOK = false
		b.toolHeaderCacheHeaderMain = ""
		b.toolHeaderCacheHeaderGray = ""
		b.toolHeaderCacheHeaderPartsOK = false
		b.toolHeaderCacheCollapsedMain = ""
		b.toolHeaderCacheCollapsedGray = ""
		b.toolHeaderCacheCollapsedOK = false
		b.toolHeaderCacheCollapsedReady = false
		b.toolHeaderCacheParamLines = nil
		b.toolHeaderCacheParamLinesOK = false
	}
	if !b.toolHeaderCacheHeaderParamsOK {
		b.toolHeaderCacheHeaderParams = formatToolHeaderParamsWithParsed(b.ToolName, keys, vals)
		b.toolHeaderCacheHeaderParamsOK = true
	}
	if !b.toolHeaderCacheHeaderPartsOK {
		b.toolHeaderCacheHeaderMain, b.toolHeaderCacheHeaderGray = formatToolHeaderPartsWithParsed(b.ToolName, keys, vals)
		b.toolHeaderCacheHeaderPartsOK = true
	}
	if !b.toolHeaderCacheCollapsedReady {
		b.toolHeaderCacheCollapsedMain, b.toolHeaderCacheCollapsedGray, b.toolHeaderCacheCollapsedOK = formatCollapsedBashHeaderPartsWithParsed(keys, vals)
		b.toolHeaderCacheCollapsedReady = true
	}
	if !b.toolHeaderCacheParamLinesOK {
		b.toolHeaderCacheParamLines = append(b.toolHeaderCacheParamLines[:0], extractToolParamsLinesWithParsed(b.ToolName, keys, vals)...)
		b.toolHeaderCacheParamLinesOK = true
	}
	return b.toolHeaderCacheHeaderParams,
		b.toolHeaderCacheHeaderMain,
		b.toolHeaderCacheHeaderGray,
		b.toolHeaderCacheCollapsedMain,
		b.toolHeaderCacheCollapsedGray,
		b.toolHeaderCacheCollapsedOK,
		b.toolHeaderCacheParamLines
}

// parseToolArgs parses the JSON args into an ordered list of key-value pairs.
func parseToolArgs(argsJSON string) (keys []string, vals map[string]string) {
	vals = map[string]string{}
	if argsJSON == "" {
		return
	}
	dec := json.NewDecoder(strings.NewReader(argsJSON))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return
	}
	for dec.More() {
		kTok, err := dec.Token()
		if err != nil {
			break
		}
		k, ok := kTok.(string)
		if !ok {
			break
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			break
		}
		if s := formatParamValue(v); s != "" {
			keys = append(keys, k)
			vals[k] = s
		}
	}
	return
}

func formatParamValue(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%.0f", val)
	case bool:
		if !val {
			return ""
		}
		return "true"
	default:
		b, _ := json.Marshal(v)
		s := string(b)
		if s == "null" || s == "{}" || s == "[]" {
			return ""
		}
		return s
	}
}

func formatToolHeaderPartsWithParsed(toolName string, keys []string, vals map[string]string) (mainPart, grayPart string) {
	if len(keys) == 0 {
		return "", ""
	}
	switch toolName {
	case "Bash":
		mainPart, grayPart := bashSummaryParts(vals)
		if mainPart == "" {
			return "", ""
		}
		return mainPart, grayPart
	case "Spawn":
		cmd := firstDisplayLine(vals["command"])
		if cmd == "" {
			return "", ""
		}
		if d := vals["description"]; d != "" {
			return cmd, "(" + d + ")"
		}
		return cmd, ""
	case "SpawnStop":
		if id := vals["id"]; id != "" {
			return id, ""
		}
		return "", ""
	case "Grep":
		pattern := vals["pattern"]
		if pattern == "" {
			return "", ""
		}
		var opts []string
		filePath := vals["path"]
		if filePath != "" && filePath != "." {
			opts = append(opts, "path="+filePath)
		}
		if v := vals["glob"]; v != "" {
			opts = append(opts, "glob="+v)
		}
		if len(opts) == 0 {
			return pattern, ""
		}
		return pattern, "(" + strings.Join(opts, ", ") + ")"
	case "Glob":
		pattern := vals["pattern"]
		if pattern == "" {
			return "", ""
		}
		filePath := vals["path"]
		if filePath != "" && filePath != "." {
			return pattern, "(path=" + filePath + ")"
		}
		return pattern, ""
	case "WebFetch":
		url := vals["url"]
		if url == "" {
			return "", ""
		}
		var opts []string
		if vals["raw"] == "true" {
			opts = append(opts, "raw")
		}
		if v := vals["timeout"]; v != "" && v != "0" && v != "30" {
			opts = append(opts, "timeout="+v)
		}
		if len(opts) == 0 {
			return url, ""
		}
		return url, "(" + strings.Join(opts, ", ") + ")"
	case "Delete":
		filePaths := parseDeleteHeaderPaths(vals)
		if len(filePaths) == 0 {
			return "", ""
		}
		if len(filePaths) == 1 {
			return filePaths[0], ""
		}
		return fmt.Sprintf("%d files", len(filePaths)), ""
	case "Skill":
		name := strings.TrimSpace(vals["name"])
		if name == "" {
			return "", ""
		}
		if label := strings.TrimSpace(skillToolSourceLabel(vals["result"])); label != "" {
			return name, "(from " + label + ")"
		}
		if path := strings.TrimSpace(skillToolDisplayPath(vals["result"])); path != "" {
			return name, "(" + path + ")"
		}
		return name, ""
	default:
		return "", ""
	}
}

func formatToolHeaderParamsWithParsed(toolName string, keys []string, vals map[string]string) string {
	if len(keys) == 0 {
		return ""
	}
	switch toolName {
	case "Read":
		path := vals["path"]
		if path == "" {
			return ""
		}
		var opts []string
		if v := vals["limit"]; v != "" && v != "0" {
			opts = append(opts, "limit="+v)
		}
		if v := vals["offset"]; v != "" && v != "0" {
			opts = append(opts, "offset="+v)
		}
		if len(opts) == 0 {
			return path
		}
		return path + " (" + strings.Join(opts, ", ") + ")"
	case "Delete":
		filePaths := parseDeleteHeaderPaths(vals)
		if len(filePaths) == 0 {
			return ""
		}
		if len(filePaths) == 1 {
			return filePaths[0]
		}
		return fmt.Sprintf("%d files", len(filePaths))
	case "Grep":
		pattern := vals["pattern"]
		if pattern == "" {
			return ""
		}
		var opts []string
		filePath := vals["path"]
		if filePath != "" && filePath != "." {
			opts = append(opts, "path="+filePath)
		}
		if v := vals["glob"]; v != "" {
			opts = append(opts, "glob="+v)
		}
		if len(opts) == 0 {
			return pattern
		}
		return pattern + " (" + strings.Join(opts, ", ") + ")"
	case "Glob":
		pattern := vals["pattern"]
		if pattern == "" {
			return ""
		}
		filePath := vals["path"]
		if filePath != "" && filePath != "." {
			return pattern + " (path=" + filePath + ")"
		}
		return pattern
	case "Bash":
		cmd := firstDisplayLine(vals["command"])
		if cmd == "" {
			return ""
		}
		if runewidth.StringWidth(cmd) > 55 {
			cmd = runewidth.Truncate(cmd, 55, "…")
		}
		gray := strings.TrimPrefix(strings.TrimSuffix(bashHeaderGrayPart(vals), ")"), "(")
		if gray == "" {
			return cmd
		}
		return cmd + " (" + gray + ")"
	case "Spawn":
		cmd := firstDisplayLine(vals["command"])
		if cmd == "" {
			return ""
		}
		if runewidth.StringWidth(cmd) > 55 {
			cmd = runewidth.Truncate(cmd, 55, "…")
		}
		if d := vals["description"]; d != "" {
			return cmd + " (" + d + ")"
		}
		return cmd
	case "SpawnStop":
		if id := vals["id"]; id != "" {
			return id
		}
		return ""
	case "Skill":
		name := strings.TrimSpace(vals["name"])
		if name == "" {
			return ""
		}
		return name
	default:
		return ""
	}
}

func formatToolHeaderParams(toolName, argsJSON string) string {
	keys, vals := parseToolArgs(argsJSON)
	return formatToolHeaderParamsWithParsed(toolName, keys, vals)
}

func extractToolParamsWithParsed(keys []string, vals map[string]string, maxWidth int) string {
	if len(keys) == 0 {
		return ""
	}
	if len(keys) == 1 {
		v := vals[keys[0]]
		if maxWidth > 10 && runewidth.StringWidth(v) > maxWidth {
			v = runewidth.Truncate(v, maxWidth, "…")
		}
		return paramValStyle.Render(v)
	}
	const sep = "  "
	const minValWidth = 20
	type entry struct{ key, val, prefix string }
	entries := make([]entry, len(keys))
	fixedWidth := 0
	for i, k := range keys {
		prefix := k + ": "
		entries[i] = entry{k, vals[k], prefix}
		fixedWidth += runewidth.StringWidth(prefix)
	}
	fixedWidth += runewidth.StringWidth(sep) * (len(keys) - 1)
	availForVals := maxWidth - fixedWidth
	if availForVals < minValWidth*len(keys) {
		availForVals = minValWidth * len(keys)
	}
	perVal := availForVals / len(keys)
	if perVal < minValWidth {
		perVal = minValWidth
	}
	type plainPart struct{ prefix, val string }
	plainParts := make([]plainPart, len(entries))
	for i, e := range entries {
		v := e.val
		if runewidth.StringWidth(v) > perVal {
			v = runewidth.Truncate(v, perVal, "…")
		}
		plainParts[i] = plainPart{e.prefix, v}
	}
	totalWidth := 0
	for i, p := range plainParts {
		if i > 0 {
			totalWidth += runewidth.StringWidth(sep)
		}
		totalWidth += runewidth.StringWidth(p.prefix) + runewidth.StringWidth(p.val)
	}
	if maxWidth > 10 && totalWidth > maxWidth {
		overage := totalWidth - maxWidth
		last := &plainParts[len(plainParts)-1]
		vw := runewidth.StringWidth(last.val)
		if vw > overage+1 {
			last.val = runewidth.Truncate(last.val, vw-overage, "…")
		} else {
			last.val = "…"
		}
	}
	var parts []string
	for _, p := range plainParts {
		parts = append(parts, paramKeyStyle.Render(p.prefix)+paramValStyle.Render(p.val))
	}
	return strings.Join(parts, sep)
}

func extractToolParams(argsJSON string, maxWidth int) string {
	keys, vals := parseToolArgs(argsJSON)
	return extractToolParamsWithParsed(keys, vals, maxWidth)
}

func extractToolParamsLinesWithParsed(toolName string, keys []string, vals map[string]string) []string {
	if toolName == "Skill" {
		return nil
	}
	var lines []string
	for _, k := range keys {
		if toolName == "Bash" && k == "command" {
			cmd := vals[k]
			first := firstDisplayLine(cmd)
			if len(keys) == 1 {
				if first != "" {
					lines = append(lines, first)
				}
			} else if first != "" {
				lines = append(lines, fmt.Sprintf("%s: %s", k, first))
			}
			for _, extra := range continuationDisplayLines(cmd) {
				lines = append(lines, "  "+extra)
			}
			continue
		}
		if len(keys) == 1 {
			lines = append(lines, vals[k])
		} else {
			lines = append(lines, fmt.Sprintf("%s: %s", k, vals[k]))
		}
	}
	return lines
}

// appendTodoCallItemLines appends wrapped, styled lines for one todo row (status icon, id, body).
func appendTodoCallItemLines(result *[]string, item todoCallArgItem, contentWidth int) {
	const indent = "    "
	marker, iconSt := todoCallStatusMarker(item.Status)
	idDisp := strings.TrimSpace(item.ID)
	if idDisp != "" && !strings.HasSuffix(idDisp, ".") {
		idDisp += "."
	}
	content := strings.TrimSpace(item.Content)
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = strings.ReplaceAll(content, "\n", " ")
	content = strings.TrimSpace(content)
	prefixCols := runewidth.StringWidth(indent) + runewidth.StringWidth(marker) + 1
	if idDisp != "" {
		prefixCols += runewidth.StringWidth(idDisp) + 1
	}
	wrapW := contentWidth - prefixCols
	if wrapW < 8 {
		wrapW = 8
	}
	wrapped := wrapText(content, wrapW)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	bodyStyle := paramValStyle
	switch item.Status {
	case "in_progress":
		bodyStyle = paramValStyle.Bold(true)
	case "cancelled":
		bodyStyle = DimStyle.Strikethrough(true)
	case "completed":
		bodyStyle = DiffAddStyle
	}
	idSeg := ""
	if idDisp != "" {
		idSeg = paramKeyStyle.Render(idDisp) + " "
	}
	firstLine := indent + iconSt.Render(marker) + " " + idSeg
	firstBody := bodyStyle.Render(wrapped[0])
	firstLine += firstBody
	*result = append(*result, firstLine)
	pad := strings.Repeat(" ", prefixCols-runewidth.StringWidth(indent))
	for _, wline := range wrapped[1:] {
		*result = append(*result, indent+pad+bodyStyle.Render(wline))
	}
	if af := strings.TrimSpace(item.ActiveForm); af != "" && item.Status == "in_progress" {
		af = strings.ReplaceAll(strings.ReplaceAll(af, "\r\n", " "), "\n", " ")
		afPad := strings.Repeat(" ", prefixCols-runewidth.StringWidth(indent))
		for _, wl := range wrapText(af, wrapW) {
			*result = append(*result, indent+afPad+DimStyle.Render("↳ "+wl))
		}
	}
}

type todoCallArgItem struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form,omitempty"`
}

func todoCallStatusMarker(status string) (marker string, st lipgloss.Style) {
	switch status {
	case "completed":
		return "✓", lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.InfoPanelSuccessFg))
	case "in_progress":
		return "▶", lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.InfoPanelWarningFg))
	case "cancelled":
		return "✗", DimStyle
	default:
		return "○", lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.InfoPanelPendingFg))
	}
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
