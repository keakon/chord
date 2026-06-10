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

const (
	openCodePartTypeText           = "text"
	openCodePartTypeTool           = "tool"
	openCodePartTypeToolInvocation = "tool-invocation"
	openCodePartTypeReasoning      = "reasoning"
	openCodePartTypeThinking       = "thinking"
	openCodePartTypeStepStart      = "step-start"
	openCodePartTypeStepFinish     = "step-finish"

	openCodeRoleUser      = "user"
	openCodeRoleHuman     = "human"
	openCodeRoleAssistant = "assistant"
	openCodeRoleAI        = "ai"
	openCodeRoleModel     = "model"

	openCodeEventModelSwitched      = "model-switched"
	openCodeEventModelSwitchedSnake = "model_switched"
	openCodeEventModelSwitchedEvent = "model_switched_event"
	openCodeEventCompaction         = "compaction"
	openCodeEventSession            = "session-event"
	openCodeEvent                   = "event"

	openCodeStatusSuccess   = "success"
	openCodeStatusSucceeded = "succeeded"
	openCodeStatusComplete  = "complete"
	openCodeStatusCompleted = "completed"
	openCodeStatusDone      = "done"
	openCodeStatusError     = "error"
	openCodeStatusFailed    = "failed"
	openCodeStatusFailure   = "failure"
	openCodeStatusCancelled = "cancelled"
	openCodeStatusCanceled  = "canceled"

	openCodeUnsupportedToolHeaderPrefix = "[Imported unsupported tool"
	openCodeToolFallbackHeader          = "[Imported tool]"
)

// convertOpenCodeExport converts an OpenCode `opencode export <sessionID>` JSON
// file into a Chord main transcript. Recognized tool parts are split into
// Chord tool-call/result messages; unrecognized parts stay as readable fallback
// text so their source payload is not lost.
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
		msgs, skipped, toolRenderedCount, unsupportedToolCount, structuredToolCalls, structuredToolResults, reasoningSkipped, warns, err := convertOpenCodeMessage(raw, reasoningMode)
		for _, w := range warns {
			report.warnf("opencode message[%d]: %s", idx, w)
		}
		if err != nil {
			return nil, fmt.Errorf("opencode import: message[%d]: %w", idx, err)
		}
		if toolRenderedCount > 0 {
			report.ToolEntriesRendered += toolRenderedCount
			report.UnsupportedToolCalls += unsupportedToolCount
			report.StructuredToolCalls += structuredToolCalls
			report.StructuredToolResults += structuredToolResults
		}
		if reasoningSkipped {
			report.ReasoningBlocksSkipped++
		}
		if skipped {
			report.SkippedEntries++
			continue
		}
		if len(msgs) == 0 {
			report.SkippedEntries++
			continue
		}
		out = append(out, msgs...)
	}

	return out, nil
}

func convertOpenCodeMessage(raw json.RawMessage, reasoningMode string) (msgs []message.Message, skipped bool, toolRenderedCount int, unsupportedToolCount int, structuredToolCalls int, structuredToolResults int, reasoningSkipped bool, warns []string, err error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false, 0, 0, 0, 0, false, nil, fmt.Errorf("parse message object: %w", err)
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
		case openCodeRoleUser, openCodeRoleHuman:
			role = message.RoleUser
		case openCodeRoleAssistant, openCodeRoleAI, openCodeRoleModel:
			role = message.RoleAssistant
		}
	}

	// Ignore model-switched/compaction markers; the importer is report-only for these.
	switch kind {
	case openCodeEventModelSwitched, openCodeEventModelSwitchedSnake, openCodeEventModelSwitchedEvent, openCodeEventCompaction, openCodeEventSession, openCodeEvent:
		return nil, true, 0, 0, 0, 0, false, []string{"skipped non-conversation event type=" + kind}, nil
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
		partsMsgs, partsToolCount, partsUnsupportedToolCount, partsStructuredToolCalls, partsStructuredToolResults, partsReasoningSkipped, partWarns, partErr := convertOpenCodePartsOrdered(rawParts, role, reasoningMode)
		warns = append(warns, partWarns...)
		if partErr != nil {
			return nil, false, 0, 0, 0, 0, false, warns, partErr
		}
		toolRenderedCount = partsToolCount
		unsupportedToolCount = partsUnsupportedToolCount
		structuredToolCalls = partsStructuredToolCalls
		structuredToolResults = partsStructuredToolResults
		reasoningSkipped = partsReasoningSkipped
		if len(partsMsgs) == 0 {
			return nil, true, toolRenderedCount, unsupportedToolCount, structuredToolCalls, structuredToolResults, reasoningSkipped, warns, nil
		}
		return partsMsgs, false, toolRenderedCount, unsupportedToolCount, structuredToolCalls, structuredToolResults, reasoningSkipped, warns, nil
	} else {
		if rawContent, ok := obj["content"]; ok && openCodeRawPartsContainToolish(rawContent) {
			partsMsgs, partsToolCount, partsUnsupportedToolCount, partsStructuredToolCalls, partsStructuredToolResults, partsReasoningSkipped, partWarns, partErr := convertOpenCodePartsOrdered(rawContent, role, reasoningMode)
			warns = append(warns, partWarns...)
			if partErr != nil {
				return nil, false, 0, 0, 0, 0, false, warns, partErr
			}
			if len(partsMsgs) == 0 {
				return nil, true, partsToolCount, partsUnsupportedToolCount, partsStructuredToolCalls, partsStructuredToolResults, partsReasoningSkipped, warns, nil
			}
			return partsMsgs, false, partsToolCount, partsUnsupportedToolCount, partsStructuredToolCalls, partsStructuredToolResults, partsReasoningSkipped, warns, nil
		}

		var contentTypes []string
		var w []string
		var e error
		contentText, contentTypes, w, e = extractOpenCodeContent(obj)
		warns = append(warns, w...)
		if e != nil {
			return nil, false, 0, 0, 0, 0, false, warns, e
		}

		// Reasoning handling: OpenCode exports often include separate reasoning.
		// We never map it to ThinkingBlocks (no Anthropic signature). In strict mode
		// we drop it; in visible mode we surface it as normal text.
		reasoningText, rWarns, rErr := extractReasoningText(obj, contentTypes)
		warns = append(warns, rWarns...)
		if rErr != nil {
			return nil, false, 0, 0, 0, 0, false, warns, rErr
		}
		if strings.TrimSpace(reasoningText) != "" {
			switch reasoningMode {
			case ReasoningVisible:
				contentText = joinNonEmpty(contentText, importedReasoningMarker, reasoningText)
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
		return nil, true, toolRenderedCount, unsupportedToolCount, structuredToolCalls, structuredToolResults, reasoningSkipped, warns, nil
	}

	return []message.Message{{Role: role, Content: contentText, Provenance: importedOpenCodeProvenance()}}, false, toolRenderedCount, unsupportedToolCount, structuredToolCalls, structuredToolResults, reasoningSkipped, warns, nil
}

func openCodeRawPartsContainToolish(raw json.RawMessage) bool {
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return false
	}
	for _, part := range parts {
		typ, _ := part["type"].(string)
		switch strings.ToLower(strings.TrimSpace(typ)) {
		case openCodePartTypeTool, openCodePartTypeToolInvocation:
			return true
		}
	}
	return false
}

func openCodeMessageRole(roleText string) message.Role {
	switch roleText {
	case openCodeRoleUser:
		return message.RoleUser
	case openCodeRoleAssistant:
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

func convertOpenCodePartsOrdered(raw json.RawMessage, role message.Role, reasoningMode string) (msgs []message.Message, toolCount int, unsupportedToolCount int, structuredToolCalls int, structuredToolResults int, reasoningSkipped bool, warns []string, err error) {
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, 0, 0, 0, 0, false, nil, fmt.Errorf("parse parts array: %w", err)
	}
	var rendered []string
	flushText := func() {
		content := strings.TrimSpace(strings.Join(rendered, "\n\n"))
		if content != "" {
			msgs = append(msgs, message.Message{Role: role, Content: content, Provenance: importedOpenCodeProvenance()})
		}
		rendered = nil
	}
	for _, part := range parts {
		typ, _ := part["type"].(string)
		typ = strings.ToLower(strings.TrimSpace(typ))
		switch typ {
		case openCodePartTypeText:
			if s, _, ok := extractTextFromBlock(part); ok && strings.TrimSpace(s) != "" {
				rendered = append(rendered, strings.TrimSpace(s))
			}
		case openCodePartTypeTool, openCodePartTypeToolInvocation:
			toolCount++
			callMsg, resultMsg, ok := openCodeStructuredToolMessages(part)
			if ok {
				flushText()
				msgs = append(msgs, callMsg)
				structuredToolCalls++
				if resultMsg != nil {
					msgs = append(msgs, *resultMsg)
					structuredToolResults++
				}
				continue
			}
			renderedTool := renderOpenCodeUnsupportedToolPart(part)
			// Report the first reason openCodeStructuredToolMessages gave up,
			// mirroring its check order, so the warning names the actual cause.
			name := strings.TrimSpace(openCodeToolName(part))
			_, mapped := openCodeToolMapping[strings.ToLower(name)]
			switch {
			case name == "":
				warns = append(warns, "tool part missing name; imported as text")
			case strings.TrimSpace(openCodeToolCallID(part)) == "":
				warns = append(warns, "tool part missing call id; imported as text")
			case !mapped:
				warns = append(warns, "unsupported tool "+name+"; imported as text")
			default:
				warns = append(warns, "tool arguments could not be normalized; imported as text")
			}
			unsupportedToolCount++
			rendered = append(rendered, renderedTool)
		case openCodePartTypeReasoning, openCodePartTypeThinking:
			if s, ok := part["text"].(string); ok && strings.TrimSpace(s) != "" {
				switch reasoningMode {
				case ReasoningVisible:
					rendered = append(rendered, importedReasoningMarker, strings.TrimSpace(s))
				case ReasoningOff, ReasoningStrict:
					reasoningSkipped = true
				default:
					reasoningSkipped = true
				}
			}
		case openCodePartTypeStepStart, openCodePartTypeStepFinish:
			// OpenCode step markers are UI-only and intentionally skipped.
		case "":
			warns = append(warns, "part missing type; skipped")
		default:
			payload, _ := json.MarshalIndent(part, "", "  ")
			rendered = append(rendered, "[Imported OpenCode "+typ+" part]\n"+string(payload))
			warns = append(warns, "unsupported part type "+typ+"; imported as text")
		}
	}
	flushText()
	return msgs, toolCount, unsupportedToolCount, structuredToolCalls, structuredToolResults, reasoningSkipped, warns, nil
}

func extractReasoningText(obj map[string]json.RawMessage, contentTypes []string) (string, []string, error) {
	// Some exports include a dedicated reasoning field.
	if s := pickStringField(obj, "reasoning", "thought", "thinking"); strings.TrimSpace(s) != "" {
		return s, nil, nil
	}
	// If content blocks include a "reasoning" type, extract it too.
	for _, typ := range contentTypes {
		if typ == openCodePartTypeReasoning || typ == openCodePartTypeThinking {
			// content already extracted as plain text, so nothing extra to do.
			return "", nil, nil
		}
	}
	return "", nil, nil
}

func openCodeStructuredToolMessages(part map[string]any) (message.Message, *message.Message, bool) {
	name := openCodeToolName(part)
	if strings.TrimSpace(name) == "" {
		return message.Message{}, nil, false
	}
	callID := openCodeToolCallID(part)
	if strings.TrimSpace(callID) == "" {
		return message.Message{}, nil, false
	}
	args := openCodeToolArgs(part)
	if chordName, ok := openCodeToolMapping[strings.ToLower(name)]; ok {
		rawArgs, _ := json.Marshal(args)
		if norm := codexNormalizeToolArgs(name, chordName, rawArgs); norm != nil {
			call := message.Message{
				Role: message.RoleAssistant,
				ToolCalls: []message.ToolCall{{
					ID:   callID,
					Name: chordName,
					Args: norm,
				}},
				Provenance: importedOpenCodeProvenance(),
			}
			output, hasOutput := openCodeToolOutputValue(part)
			status := openCodeToolStatus(part, hasOutput)
			if !hasOutput && status == "" {
				return call, nil, true
			}
			result := message.Message{
				Role:       message.RoleTool,
				ToolCallID: callID,
				Content:    output,
				ToolStatus: status,
				Provenance: importedOpenCodeProvenance(),
			}
			return call, &result, true
		}
	}
	return message.Message{}, nil, false
}

func renderOpenCodeUnsupportedToolPart(part map[string]any) string {
	name := openCodeToolName(part)
	payload, _ := json.MarshalIndent(part, "", "  ")
	var b strings.Builder
	b.WriteString(openCodeUnsupportedToolHeaderPrefix)
	if name != "" {
		b.WriteString(": ")
		b.WriteString(name)
	}
	b.WriteString("]\n")
	b.Write(payload)
	return b.String()
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

func openCodeToolOutputValue(part map[string]any) (string, bool) {
	if state, ok := part["state"].(map[string]any); ok {
		if s, ok := state["output"].(string); ok {
			return s, true
		}
		if result, ok := state["result"].(string); ok {
			return result, true
		}
	}
	if s, ok := part["output"].(string); ok {
		return s, true
	}
	if s, ok := part["result"].(string); ok {
		return s, true
	}
	return "", false
}

func openCodeToolStatus(part map[string]any, hasOutput bool) string {
	status := ""
	if state, ok := part["state"].(map[string]any); ok {
		if s, ok := state["status"].(string); ok {
			status = s
		}
	}
	if status == "" {
		if s, ok := part["status"].(string); ok {
			status = s
		}
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case openCodeStatusSuccess, openCodeStatusSucceeded, openCodeStatusComplete, openCodeStatusCompleted, openCodeStatusDone:
		return message.ToolStatusSuccess
	case openCodeStatusError, openCodeStatusFailed, openCodeStatusFailure:
		return message.ToolStatusError
	case openCodeStatusCancelled, openCodeStatusCanceled:
		return message.ToolStatusCancelled
	default:
		if hasOutput {
			return message.ToolStatusSuccess
		}
		return ""
	}
}

func extractToolishText(obj map[string]json.RawMessage, contentTypes []string) (string, []string) {
	// Common OpenCode tool shapes: {"tool":{...}}, {"shell":{...}}, or content blocks of type "tool".
	var warns []string
	if raw, ok := obj["tool"]; ok && len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		pretty := indentJSON(raw)
		return joinNonEmpty(openCodeToolFallbackHeader, pretty), warns
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
				if strings.ToLower(strings.TrimSpace(t)) != openCodePartTypeTool {
					continue
				}
				payload, _ := json.MarshalIndent(block, "", "  ")
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString(openCodeToolFallbackHeader)
				b.WriteString("\n")
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
				if strings.ToLower(strings.TrimSpace(typ)) == openCodePartTypeTool {
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
	if s, ok := part["name"].(string); ok {
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
