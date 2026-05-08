package sessionimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

type openCodeExportFile struct {
	Info     json.RawMessage   `json:"info"`
	Messages []json.RawMessage `json:"messages"`
}

// convertOpenCodeExport converts an OpenCode `opencode export <sessionID>` JSON
// file into a Chord main transcript.
//
// Tool history is always imported as text (no structured tools).
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
		msg, skipped, toolRendered, reasoningSkipped, warns, err := convertOpenCodeMessage(raw, reasoningMode)
		for _, w := range warns {
			report.warnf("opencode message[%d]: %s", idx, w)
		}
		if err != nil {
			return nil, fmt.Errorf("opencode import: message[%d]: %w", idx, err)
		}
		if skipped {
			report.SkippedEntries++
			continue
		}
		if toolRendered {
			report.ToolEntriesRendered++
		}
		if reasoningSkipped {
			report.ReasoningBlocksSkipped++
		}
		if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 {
			report.SkippedEntries++
			continue
		}
		out = append(out, msg)
	}

	return out, nil
}

func convertOpenCodeMessage(raw json.RawMessage, reasoningMode string) (msg message.Message, skipped bool, toolRendered bool, reasoningSkipped bool, warns []string, err error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return msg, false, false, false, nil, fmt.Errorf("parse message object: %w", err)
	}

	kind := pickStringField(obj, "type", "kind", "role")
	kind = strings.ToLower(strings.TrimSpace(kind))

	role := pickStringField(obj, "role", "sender")
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		switch kind {
		case "user", "human":
			role = "user"
		case "assistant", "ai", "model":
			role = "assistant"
		}
	}

	// Ignore model-switched/compaction markers; the importer is report-only for these.
	switch kind {
	case "model-switched", "model_switched", "model_switched_event", "compaction", "session-event", "event":
		return msg, true, false, false, []string{"skipped non-conversation event type=" + kind}, nil
	}

	if role != "user" && role != "assistant" {
		// Many OpenCode exports are message-like but not chat roles (e.g. shell). Import as assistant-visible text.
		if role == "" {
			role = "assistant"
		} else {
			role = "assistant"
			warns = append(warns, fmt.Sprintf("unknown role %q; imported as assistant text", role))
		}
	}

	contentText, contentTypes, w, e := extractOpenCodeContent(obj)
	warns = append(warns, w...)
	if e != nil {
		return msg, false, false, false, warns, e
	}

	// Reasoning handling: OpenCode exports often include separate reasoning.
	// We never map it to ThinkingBlocks (no Anthropic signature). In strict mode
	// we drop it; in visible mode we surface it as normal text.
	reasoningText, rWarns, rErr := extractReasoningText(obj, contentTypes)
	warns = append(warns, rWarns...)
	if rErr != nil {
		return msg, false, false, false, warns, rErr
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
		toolRendered = true
	}

	contentText = strings.TrimSpace(contentText)
	if contentText == "" {
		return msg, true, toolRendered, reasoningSkipped, warns, nil
	}

	return message.Message{Role: role, Content: contentText}, false, toolRendered, reasoningSkipped, warns, nil
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

func extractToolishText(obj map[string]json.RawMessage, contentTypes []string) (string, []string) {
	// Common OpenCode shapes: {"tool":{...}}, {"shell":{...}}, or content blocks of type "tool".
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
