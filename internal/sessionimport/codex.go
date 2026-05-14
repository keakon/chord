package sessionimport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// convertCodexRollout converts a Codex rollout JSONL file (typically under
// ~/.codex/sessions/**/rollout-*.jsonl) into a Chord main transcript.
//
// Tool history is always imported as text (no structured tools).
func convertCodexRollout(data []byte, reasoningMode string, report *ImportReport) ([]message.Message, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("codex import: empty input")
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Codex tool outputs can be large; raise the scanner buffer conservatively.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

	var out []message.Message
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var lineObj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &lineObj); err != nil {
			return nil, fmt.Errorf("codex import: line %d: parse JSON: %w", lineNo, err)
		}
		itemObj, err := normalizeCodexLine(lineObj)
		if err != nil {
			report.warnf("codex line %d: skipped unsupported item (not an object)", lineNo)
			report.SkippedEntries++
			continue
		}

		// Best-effort extract session id from any meta-ish entry.
		if report.SourceSessionID == "" {
			if sid, ok := extractCodexSessionID(itemObj); ok {
				// Only accept when it looks like a session identifier.
				if strings.Contains(sid, "sess") || strings.Contains(sid, "session") || len(sid) > 8 {
					report.SourceSessionID = sid
				}
			}
		}

		msg, skipped, toolRendered, reasoningSkipped, warns, err := convertCodexItem(itemObj, reasoningMode)
		for _, w := range warns {
			report.warnf("codex line %d: %s", lineNo, w)
		}
		if err != nil {
			return nil, fmt.Errorf("codex import: line %d: %w", lineNo, err)
		}
		if toolRendered {
			report.ToolEntriesRendered++
		}
		if reasoningSkipped {
			report.ReasoningBlocksSkipped++
		}
		if skipped {
			report.SkippedEntries++
			continue
		}
		if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 {
			report.SkippedEntries++
			continue
		}
		if len(out) > 0 && len(msg.Parts) == 0 && len(out[len(out)-1].Parts) == 0 {
			prev := out[len(out)-1]
			if prev.Role == msg.Role && strings.TrimSpace(prev.Content) == strings.TrimSpace(msg.Content) {
				report.SkippedEntries++
				report.warnf("codex line %d: skipped duplicate adjacent %s message", lineNo, msg.Role)
				continue
			}
		}
		out = append(out, msg)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("codex import: scan JSONL: %w", err)
	}

	return out, nil
}

func normalizeCodexLine(lineObj map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if itemRaw := lineObj["item"]; len(bytes.TrimSpace(itemRaw)) != 0 {
		return readJSONAsMap(itemRaw)
	}
	if payloadRaw := lineObj["payload"]; len(bytes.TrimSpace(payloadRaw)) != 0 {
		payloadObj, err := readJSONAsMap(payloadRaw)
		if err != nil {
			return nil, err
		}
		if rawType, ok := lineObj["type"]; ok {
			payloadObj["__event_type"] = rawType
		}
		if _, ok := payloadObj["type"]; !ok {
			if rawType, ok := lineObj["type"]; ok {
				payloadObj["type"] = rawType
			}
		}
		return payloadObj, nil
	}
	return lineObj, nil
}

func extractCodexSessionID(itemObj map[string]json.RawMessage) (string, bool) {
	if sid, ok := pickFirstStringRaw(itemObj, "session_id", "sessionId", "sessionID", "id"); ok {
		return sid, true
	}
	if payloadRaw, ok := itemObj["payload"]; ok {
		if payloadObj, err := readJSONAsMap(payloadRaw); err == nil {
			if sid, ok := pickFirstStringRaw(payloadObj, "session_id", "sessionId", "sessionID", "id"); ok {
				return sid, true
			}
		}
	}
	return "", false
}

func convertCodexItem(item map[string]json.RawMessage, reasoningMode string) (msg message.Message, skipped bool, toolRendered bool, reasoningSkipped bool, warns []string, err error) {
	// Attempt to detect and import canonical chat messages.
	role := strings.ToLower(strings.TrimSpace(pickStringField(item, "role")))
	if role == "" {
		role = strings.ToLower(strings.TrimSpace(pickStringField(item, "sender")))
	}
	text := pickStringField(item, "content", "text", "message")
	if strings.TrimSpace(text) == "" {
		if contentRaw, ok := item["content"]; ok {
			if contentText := flattenCodexContentText(contentRaw); strings.TrimSpace(contentText) != "" {
				text = contentText
			}
		}
	}

	// Some entries nest the actual payload under well-known keys.
	if role == "" && strings.TrimSpace(text) == "" {
		for _, k := range []string{"EventMsg", "event", "ResponseItem", "response", "data", "payload"} {
			raw, ok := item[k]
			if !ok {
				continue
			}
			obj, e := readJSONAsMap(raw)
			if e != nil {
				continue
			}
			if r := strings.ToLower(strings.TrimSpace(pickStringField(obj, "role", "sender"))); r != "" {
				role = r
			}
			if t := pickStringField(obj, "content", "text", "message"); strings.TrimSpace(t) != "" {
				text = t
			} else if contentRaw, ok := obj["content"]; ok {
				if contentText := flattenCodexContentText(contentRaw); strings.TrimSpace(contentText) != "" {
					text = contentText
				}
			}
		}
	}

	if role == "user" || role == "assistant" || role == "developer" {
		text = strings.TrimSpace(text)
		if text == "" {
			return msg, true, false, false, nil, nil
		}
		if role == "developer" {
			return msg, true, false, false, []string{"skipped developer message"}, nil
		}
		return message.Message{Role: role, Content: text}, false, false, false, warns, nil
	}

	// Tool calls / outputs in Codex rollouts are typically separate response items.
	// We render them as assistant-visible text to avoid cross-provider protocol issues.
	if raw, ok := item["FunctionCall"]; ok {
		toolRendered = true
		return message.Message{Role: "assistant", Content: renderImportedToolMarker("tool call", raw)}, false, toolRendered, false, nil, nil
	}
	if raw, ok := item["FunctionCallOutput"]; ok {
		toolRendered = true
		return message.Message{Role: "assistant", Content: renderImportedToolMarker("tool result", raw)}, false, toolRendered, false, nil, nil
	}
	if raw, ok := item["CustomToolCall"]; ok {
		toolRendered = true
		return message.Message{Role: "assistant", Content: renderImportedToolMarker("custom tool call", raw)}, false, toolRendered, false, nil, nil
	}
	if raw, ok := item["CustomToolCallOutput"]; ok {
		toolRendered = true
		return message.Message{Role: "assistant", Content: renderImportedToolMarker("custom tool result", raw)}, false, toolRendered, false, nil, nil
	}

	// Some schemas use a discriminator instead of nested keys.
	kind := strings.ToLower(strings.TrimSpace(pickStringField(item, "type", "kind")))
	switch kind {
	case "reasoning":
		reasoningText := pickStringField(item, "content", "text", "reasoning")
		if strings.TrimSpace(reasoningText) == "" {
			if contentRaw, ok := item["content"]; ok {
				reasoningText = flattenCodexContentText(contentRaw)
			}
		}
		if strings.TrimSpace(reasoningText) == "" {
			reasoningSkipped = true
			return msg, true, false, reasoningSkipped, []string{"skipped reasoning entry without visible plaintext"}, nil
		}
		switch reasoningMode {
		case ReasoningVisible:
			return message.Message{Role: "assistant", Content: joinNonEmpty("[Imported reasoning]", reasoningText)}, false, false, false, nil, nil
		case ReasoningOff, ReasoningStrict:
			reasoningSkipped = true
			return msg, true, false, reasoningSkipped, nil, nil
		default:
			reasoningSkipped = true
			return msg, true, false, reasoningSkipped, nil, nil
		}
	case "sessionmeta", "session_meta", "meta", "turncontext", "turn_context", "compacted", "compact", "metrics", "task_started", "task_complete", "token_count", "agent_reasoning", "web_search_end":
		return msg, true, false, false, []string{"skipped non-conversation event type=" + kind}, nil
	case "functioncall", "function_call", "toolcall", "tool_call", "web_search_call":
		toolRendered = true
		return message.Message{Role: "assistant", Content: renderImportedToolMarker("tool call", mustJSON(item))}, false, toolRendered, false, nil, nil
	case "functioncalloutput", "function_call_output", "tooloutput", "tool_output":
		toolRendered = true
		return message.Message{Role: "assistant", Content: renderImportedToolMarker("tool result", mustJSON(item))}, false, toolRendered, false, nil, nil
	case "agent_message":
		text = strings.TrimSpace(pickStringField(item, "message", "text", "content"))
		if text == "" {
			return msg, true, false, false, nil, nil
		}
		return message.Message{Role: "assistant", Content: text}, false, false, false, nil, nil
	case "user_message":
		text = strings.TrimSpace(pickStringField(item, "message", "text", "content"))
		if text == "" {
			return msg, true, false, false, nil, nil
		}
		return message.Message{Role: "user", Content: text}, false, false, false, nil, nil
	}

	// Unknown entry: try to keep some visibility by importing a short marker when it looks message-like.
	if strings.TrimSpace(text) != "" {
		warns = append(warns, "unknown role; imported as assistant text")
		return message.Message{Role: "assistant", Content: strings.TrimSpace(text)}, false, false, false, warns, nil
	}

	return msg, true, false, false, nil, nil
}

func flattenCodexContentText(raw json.RawMessage) string {
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		partType := strings.ToLower(strings.TrimSpace(pickStringField(part, "type")))
		switch partType {
		case "input_text", "output_text", "text":
			if text := strings.TrimSpace(pickStringField(part, "text")); text != "" {
				out = append(out, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n\n"))
}

func mustJSON(m map[string]json.RawMessage) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

func pickFirstStringRaw(m map[string]json.RawMessage, keys ...string) (string, bool) {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			s = strings.TrimSpace(s)
			if s != "" {
				return s, true
			}
		}
	}
	return "", false
}
