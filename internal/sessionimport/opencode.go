package sessionimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type openCodeExportFile struct {
	Info     json.RawMessage   `json:"info"`
	Messages []json.RawMessage `json:"messages"`
}

// convertOpenCodeExport converts an OpenCode `opencode export <sessionID>` JSON
// file into a Chord main transcript. OpenCode current-export tool parts keep
// call state/result inside the same part, so unsupported tools are preserved as
// text cards instead of Chord ToolCalls that would have no matching tool result
// message.
func convertOpenCodeExport(data []byte, reasoningMode string, report *ImportReport) ([]message.Message, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("opencode import: empty input")
	}

	var file openCodeExportFile
	if err := json.Unmarshal(data, &file); err != nil {
		// Some versions may export just the messages array; tolerate that.
		var arr []json.RawMessage
		if err2 := json.Unmarshal(data, &arr); err2 != nil {
			return nil, fmt.Errorf("opencode import: parse JSON: %w", err)
		}
		file.Messages = arr
	}

	if len(file.Info) > 0 {
		// Best-effort extract source session id.
		var info map[string]any
		if err := json.Unmarshal(file.Info, &info); err == nil {
			if v, ok := firstString(info, "id", "session_id", "sessionId", "sessionID"); ok {
				report.SourceSessionID = v
			}
		}
	}

	var out []message.Message
	for idx, raw := range file.Messages {
		msg, skipped, toolRenderedCount, unsupportedToolCount, reasoningSkipped, warns, err := convertOpenCodeMessage(raw, reasoningMode)
		for _, w := range warns {
			report.warnf("opencode message[%d]: %s", idx, w)
		}
		if err != nil {
			return nil, fmt.Errorf("opencode import: message[%d]: %w", idx, err)
		}
		if toolRenderedCount > 0 {
			report.ToolEntriesRendered += toolRenderedCount
			report.UnsupportedToolCalls += unsupportedToolCount
		}
		if reasoningSkipped {
			report.ReasoningBlocksSkipped++
		}
		if skipped {
			report.SkippedEntries++
			continue
		}
		if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 {
			report.SkippedEntries++
			continue
		}
		out = append(out, msg)
	}

	return out, nil
}

func convertOpenCodeMessage(raw json.RawMessage, reasoningMode string) (msg message.Message, skipped bool, toolRenderedCount int, unsupportedToolCount int, reasoningSkipped bool, warns []string, err error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return msg, false, 0, 0, false, nil, fmt.Errorf("parse message object: %w", err)
	}

	kind := pickStringField(obj, "type", "kind", "role")
	kind = strings.ToLower(strings.TrimSpace(kind))

	roleText := pickStringField(obj, "role", "sender")
	roleText = strings.ToLower(strings.TrimSpace(roleText))
	if roleText == "" {
		roleText = pickNestedStringField(obj, "info", "role", "sender")
		roleText = strings.ToLower(strings.TrimSpace(roleText))
	}
	role := openCodeMessageRole(roleText)
	if role == "" {
		switch kind {
		case "user", "human":
			role = message.RoleUser
		case "assistant", "ai", "model":
			role = message.RoleAssistant
		}
	}

	// Ignore model-switched/compaction markers; the importer is report-only for these.
	switch kind {
	case "model-switched", "model_switched", "model_switched_event", "compaction", "session-event", "event":
		return msg, true, 0, 0, false, []string{"skipped non-conversation event type=" + kind}, nil
	}

	if role != message.RoleUser && role != message.RoleAssistant {
		// Many OpenCode exports are message-like but not chat roles (e.g. shell). Import as assistant-visible text.
		if roleText != "" {
			warns = append(warns, fmt.Sprintf("unknown role %q; imported as assistant text", roleText))
		}
		role = message.RoleAssistant
	}

	var contentText string
	if rawParts, ok := obj["parts"]; ok {
		partsText, partsToolCount, partsUnsupportedToolCount, partsReasoningSkipped, partWarns, partErr := extractOpenCodePartsOrdered(rawParts, reasoningMode)
		warns = append(warns, partWarns...)
		if partErr != nil {
			return msg, false, 0, 0, false, warns, partErr
		}
		contentText = partsText
		toolRenderedCount = partsToolCount
		unsupportedToolCount = partsUnsupportedToolCount
		reasoningSkipped = partsReasoningSkipped
	} else {
		var contentTypes []string
		var w []string
		var e error
		contentText, contentTypes, w, e = extractOpenCodeContent(obj)
		warns = append(warns, w...)
		if e != nil {
			return msg, false, 0, 0, false, warns, e
		}

		// Reasoning handling: OpenCode exports often include separate reasoning.
		// We never map it to ThinkingBlocks (no Anthropic signature). In strict mode
		// we drop it; in visible mode we surface it as normal text.
		reasoningText, rWarns, rErr := extractReasoningText(obj, contentTypes)
		warns = append(warns, rWarns...)
		if rErr != nil {
			return msg, false, 0, 0, false, warns, rErr
		}
		if strings.TrimSpace(reasoningText) != "" {
			switch reasoningMode {
			case ReasoningVisible:
				contentText = joinNonEmpty(contentText, "[Imported reasoning]", reasoningText)
			case ReasoningOff:
				reasoningSkipped = true
			case ReasoningStrict:
				reasoningSkipped = true
			default:
				reasoningSkipped = true
			}
		}

		toolText, toolWarns := extractToolishText(obj, contentTypes)
		warns = append(warns, toolWarns...)
		if strings.TrimSpace(toolText) != "" {
			contentText = joinNonEmpty(contentText, toolText)
			toolRenderedCount = countOpenCodeToolishParts(obj)
			unsupportedToolCount = toolRenderedCount
			if toolRenderedCount == 0 {
				toolRenderedCount = 1
				unsupportedToolCount = 1
			}
		}
	}

	contentText = strings.TrimSpace(contentText)
	if contentText == "" {
		return msg, true, toolRenderedCount, unsupportedToolCount, reasoningSkipped, warns, nil
	}

	return message.Message{Role: role, Content: contentText, Provenance: importedOpenCodeProvenance()}, false, toolRenderedCount, unsupportedToolCount, reasoningSkipped, warns, nil
}

func openCodeMessageRole(roleText string) message.Role {
	switch roleText {
	case "user":
		return message.RoleUser
	case "assistant":
		return message.RoleAssistant
	default:
		return ""
	}
}

func extractOpenCodeContent(obj map[string]json.RawMessage) (text string, contentTypes []string, warns []string, err error) {
	// Fast paths for common shapes.
	if v := pickStringField(obj, "text", "content"); strings.TrimSpace(v) != "" {
		return v, nil, nil, nil
	}

	if raw, ok := obj["content"]; ok {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return "", nil, nil, nil
		}
		// content may be an array of blocks.
		var arr []any
		if err := json.Unmarshal(raw, &arr); err == nil {
			var b strings.Builder
			for _, it := range arr {
				switch v := it.(type) {
				case string:
					b.WriteString(v)
					b.WriteString("\n")
				case map[string]any:
					t, typ, ok := extractTextFromBlock(v)
					if ok {
						if typ != "" {
							contentTypes = append(contentTypes, typ)
						}
						b.WriteString(t)
						b.WriteString("\n")
						continue
					}
					// Non-text block: keep a short marker so ordering isn't lost.
					pretty, _ := json.Marshal(v)
					b.WriteString("[Imported non-text content block]\n")
					b.Write(pretty)
					b.WriteString("\n")
					warns = append(warns, "content contained non-text block; rendered as JSON")
				default:
					warns = append(warns, "content contained unsupported block type")
				}
			}
			return strings.TrimSpace(b.String()), contentTypes, warns, nil
		}

		// content may be an object.
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			if t, _, ok := extractTextFromBlock(m); ok {
				return strings.TrimSpace(t), contentTypes, warns, nil
			}
			pretty, _ := json.MarshalIndent(m, "", "  ")
			warns = append(warns, "content object not recognized; rendered as JSON")
			return string(pretty), contentTypes, warns, nil
		}
	}
	return "", contentTypes, warns, nil
}

func extractTextFromBlock(m map[string]any) (text string, typ string, ok bool) {
	if t, ok := m["type"].(string); ok {
		typ = strings.ToLower(strings.TrimSpace(t))
	}
	if s, ok := m["text"].(string); ok {
		return s, typ, true
	}
	if s, ok := m["content"].(string); ok {
		return s, typ, true
	}
	if s, ok := m["value"].(string); ok {
		return s, typ, true
	}
	return "", typ, false
}

func extractOpenCodePartsOrdered(raw json.RawMessage, reasoningMode string) (text string, toolCount int, unsupportedToolCount int, reasoningSkipped bool, warns []string, err error) {
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", 0, 0, false, nil, fmt.Errorf("parse parts array: %w", err)
	}
	var rendered []string
	for _, part := range parts {
		typ, _ := part["type"].(string)
		typ = strings.ToLower(strings.TrimSpace(typ))
		switch typ {
		case "text":
			if s, ok := part["text"].(string); ok && strings.TrimSpace(s) != "" {
				rendered = append(rendered, strings.TrimSpace(s))
			}
		case "tool", "tool-invocation":
			toolCount++
			renderedTool, unsupported := renderOpenCodeToolPart(part)
			if unsupported {
				unsupportedToolCount++
			}
			rendered = append(rendered, renderedTool)
		case "reasoning", "thinking":
			if s, ok := part["text"].(string); ok && strings.TrimSpace(s) != "" {
				switch reasoningMode {
				case ReasoningVisible:
					rendered = append(rendered, "[Imported reasoning]", strings.TrimSpace(s))
				case ReasoningOff, ReasoningStrict:
					reasoningSkipped = true
				default:
					reasoningSkipped = true
				}
			}
		case "step-start", "step-finish":
			// OpenCode step markers are UI-only and intentionally skipped.
		case "":
			warns = append(warns, "part missing type; skipped")
		default:
			payload, _ := json.MarshalIndent(part, "", "  ")
			rendered = append(rendered, "[Imported OpenCode "+typ+" part]\n"+string(payload))
			warns = append(warns, "unsupported part type "+typ+"; imported as text")
		}
	}
	if toolCount > 0 {
		warns = append(warns, "tool parts were imported as text")
	}
	return strings.TrimSpace(strings.Join(rendered, "\n\n")), toolCount, unsupportedToolCount, reasoningSkipped, warns, nil
}

func extractReasoningText(obj map[string]json.RawMessage, contentTypes []string) (string, []string, error) {
	// Some exports include a dedicated reasoning field.
	if s := pickStringField(obj, "reasoning", "thought", "thinking"); strings.TrimSpace(s) != "" {
		return s, nil, nil
	}
	// If content blocks include a "reasoning" type, extract it too.
	for _, typ := range contentTypes {
		if typ == "reasoning" || typ == "thinking" {
			// content already extracted as plain text, so nothing extra to do.
			return "", nil, nil
		}
	}
	return "", nil, nil
}

func renderOpenCodeToolPart(part map[string]any) (string, bool) {
	name := openCodeToolName(part)
	args := openCodeToolArgs(part)
	if chordName, ok := openCodeToolMapping[strings.ToLower(name)]; ok {
		rawArgs, _ := json.Marshal(args)
		if norm := codexNormalizeToolArgs(name, chordName, rawArgs); norm != nil {
			payload := map[string]any{
				"tool":   chordName,
				"args":   json.RawMessage(norm),
				"callID": openCodeToolCallID(part),
			}
			if output := openCodeToolOutput(part); output != "" {
				payload["output"] = output
			}
			pretty, _ := json.MarshalIndent(payload, "", "  ")
			return "[Imported tool: " + chordName + "]\n" + string(pretty), false
		}
	}
	payload, _ := json.MarshalIndent(part, "", "  ")
	var b strings.Builder
	b.WriteString("[Imported unsupported tool")
	if name != "" {
		b.WriteString(": ")
		b.WriteString(name)
	}
	b.WriteString("]\n")
	b.Write(payload)
	return b.String(), true
}

var openCodeToolMapping = map[string]string{
	"bash":         tools.NameShell,
	"shell":        tools.NameShell,
	"exec":         tools.NameShell,
	"exec_command": tools.NameShell,
	"read":         tools.NameRead,
	"read_file":    tools.NameRead,
	"file_read":    tools.NameRead,
	"write":        tools.NameWrite,
	"write_file":   tools.NameWrite,
	"file_write":   tools.NameWrite,
	"edit":         tools.NameEdit,
	"edit_file":    tools.NameEdit,
	"apply_patch":  tools.NameEdit,
	"update":       tools.NameEdit,
	"delete":       tools.NameDelete,
	"remove":       tools.NameDelete,
	"delete_file":  tools.NameDelete,
	"file_delete":  tools.NameDelete,
	"grep":         tools.NameGrep,
	"search":       tools.NameGrep,
	"glob":         tools.NameGlob,
	"list_files":   tools.NameGlob,
}

func openCodeToolArgs(part map[string]any) map[string]any {
	if state, ok := part["state"].(map[string]any); ok {
		if input, ok := state["input"].(map[string]any); ok {
			return input
		}
	}
	if input, ok := part["input"].(map[string]any); ok {
		return input
	}
	if args, ok := part["args"].(map[string]any); ok {
		return args
	}
	return map[string]any{}
}

func openCodeToolCallID(part map[string]any) string {
	for _, key := range []string{"callID", "callId", "toolCallID", "toolCallId", "id"} {
		if s, ok := part[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func openCodeToolOutput(part map[string]any) string {
	if state, ok := part["state"].(map[string]any); ok {
		if s, ok := state["output"].(string); ok {
			return s
		}
		if result, ok := state["result"].(string); ok {
			return result
		}
	}
	if s, ok := part["output"].(string); ok {
		return s
	}
	return ""
}

func extractToolishText(obj map[string]json.RawMessage, contentTypes []string) (string, []string) {
	// Common OpenCode tool shapes: {"tool":{...}}, {"shell":{...}}, or content blocks of type "tool".
	var warns []string
	if raw, ok := obj["tool"]; ok && len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		pretty := indentJSON(raw)
		return joinNonEmpty("[Imported tool]", pretty), warns
	}
	if raw, ok := obj["shell"]; ok && len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		pretty := indentJSON(raw)
		return joinNonEmpty("[Imported shell]", pretty), warns
	}
	// If content includes tool blocks, extract via a second pass by looking for raw "content" and scanning for type=tool.
	if raw, ok := obj["content"]; ok {
		var arr []map[string]any
		if err := json.Unmarshal(raw, &arr); err == nil {
			var b strings.Builder
			for _, block := range arr {
				t, _ := block["type"].(string)
				if strings.ToLower(strings.TrimSpace(t)) != "tool" {
					continue
				}
				payload, _ := json.MarshalIndent(block, "", "  ")
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString("[Imported tool]\n")
				b.Write(payload)
			}
			if b.Len() > 0 {
				warns = append(warns, "tool blocks were imported as text")
				return b.String(), warns
			}
		}
	}
	_ = contentTypes
	return "", warns
}

func countOpenCodeToolishParts(obj map[string]json.RawMessage) int {
	count := 0
	if raw, ok := obj["tool"]; ok && len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		count++
	}
	if raw, ok := obj["shell"]; ok && len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		count++
	}
	if raw, ok := obj["content"]; ok {
		var arr []map[string]any
		if err := json.Unmarshal(raw, &arr); err == nil {
			for _, block := range arr {
				typ, _ := block["type"].(string)
				if strings.ToLower(strings.TrimSpace(typ)) == "tool" {
					count++
				}
			}
		}
	}
	return count
}

func openCodeToolName(part map[string]any) string {
	if s, ok := part["tool"].(string); ok {
		return strings.TrimSpace(s)
	}
	if s, ok := part["toolName"].(string); ok {
		return strings.TrimSpace(s)
	}
	if inv, ok := part["toolInvocation"].(map[string]any); ok {
		if s, ok := inv["toolName"].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func indentJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(bytes.TrimSpace(raw))
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(bytes.TrimSpace(raw))
	}
	return string(b)
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

func pickStringField(obj map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	return ""
}

func pickNestedStringField(obj map[string]json.RawMessage, field string, keys ...string) string {
	raw, ok := obj[field]
	if !ok {
		return ""
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil {
		return ""
	}
	return pickStringField(nested, keys...)
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					return s, true
				}
			}
		}
	}
	return "", false
}
