package sessionimport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/imageutil"
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
	openCodePartTypeFile           = "file"
	openCodePartTypePatch          = "patch"
	// openCodePartTypeCompaction is the compaction marker part a v1 export
	// writes inside a user message. A flat export carries the same token as
	// the type of a message of its own (openCodeEventCompaction): the source
	// spells the marker one way in both shapes, so the two names alias one
	// value instead of drifting apart.
	openCodePartTypeCompaction = "compaction"

	openCodeRoleUser      = "user"
	openCodeRoleHuman     = "human"
	openCodeRoleAssistant = "assistant"
	openCodeRoleAI        = "ai"
	openCodeRoleModel     = "model"

	openCodeEventModelSwitched      = "model-switched"
	openCodeEventModelSwitchedSnake = "model_switched"
	openCodeEventModelSwitchedEvent = "model_switched_event"
	openCodeEventCompaction         = openCodePartTypeCompaction
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

	openCodeUnsupportedToolHeaderPrefix = "[tool call"
	openCodeToolFallbackHeader          = "[tool]"
)

// convertOpenCodeExport converts an OpenCode `opencode export <sessionID>` JSON
// file into a Chord main transcript. Recognized tool parts are split into
// Chord tool-call/result messages; a tool part with no Chord mapping stays a
// tool call under the name and arguments the source agent used, so the actions
// that ran survive the import. Only a part too malformed to name or pair a call
// stays as readable fallback text.
//
// A compaction the source session applied is mapped, not replayed: the paired
// summary becomes a Chord context-summary checkpoint standing immediately
// before the oldest message the compaction kept, the messages the compaction
// kept beside the summary stay as history, and the head the source had already
// folded away is replaced by that checkpoint — the shape the source model
// itself saw. See openCodeCompactionPlan.
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

	plan := planOpenCodeCompaction(file.Messages, report)

	var out []message.Message
	for idx, raw := range file.Messages {
		// The checkpoint is placed before absorption is consulted: when the
		// tail boundary could not be located, the checkpoint stands where the
		// marker itself was, and the flat-export marker is one of the messages
		// it speaks for.
		if plan.insertsCheckpointBefore(idx) {
			out = append(out, plan.checkpoint)
		}
		if plan.absorbs(idx) {
			continue
		}
		msgs, skipped, toolRenderedCount, unsupportedToolCount, structuredToolCalls, structuredToolResults, reasoningSkipped, warns, err := convertOpenCodeMessage(raw, reasoningMode)
		for _, w := range warns {
			report.warnf("opencode message[%d]: %s", idx, w)
		}
		if err != nil {
			return nil, fmt.Errorf("opencode import: message[%d]: %w", idx, err)
		}
		if toolRenderedCount > 0 {
			report.ToolEntriesRendered += toolRenderedCount
			report.KeptSourceToolCalls += unsupportedToolCount
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

	kind := strings.ToLower(strings.TrimSpace(pickStringField(obj, "type", "kind", "role")))
	role := openCodeRawMessageRole(obj)

	// A compaction carried as a message of its own is owned by the
	// cross-message compaction pass: the newest usable one is absorbed into
	// the checkpoint before conversion, and every other one has already been
	// reported there, so nothing converts on its own.
	if kind == openCodeEventCompaction {
		return nil, true, 0, 0, 0, 0, false, nil, nil
	}

	// Ignore model-switched and other non-conversation events; the importer is
	// report-only for these.
	switch kind {
	case openCodeEventModelSwitched, openCodeEventModelSwitchedSnake, openCodeEventModelSwitchedEvent, openCodeEventSession, openCodeEvent:
		return nil, true, 0, 0, 0, 0, false, []string{"skipped non-conversation event type=" + kind}, nil
	}

	if role != message.RoleUser && role != message.RoleAssistant {
		// Many OpenCode exports are message-like but not chat roles (e.g. shell). Import as assistant-visible text.
		if roleText := openCodeRawRoleText(obj); roleText != "" {
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

// openCodeRawMessage is one decoded export message with the identity fields
// the role and id lookups need, resolved across both export shapes.
type openCodeRawMessage struct {
	obj  map[string]json.RawMessage
	info map[string]json.RawMessage
	id   string
	role message.Role
}

// openCodeMessageInfo returns the identity fields of a raw export message: the
// nested "info" object in a v1 export, the message itself in a flat one.
func openCodeMessageInfo(obj map[string]json.RawMessage) map[string]json.RawMessage {
	if raw, ok := obj["info"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) == nil && len(nested) > 0 {
			return nested
		}
	}
	return obj
}

// openCodeRawMessageRole resolves the conversation role of a raw export
// message across both export shapes, the same way the message converter does.
// The declared role text is tried first; the fallback reads the "role" key too
// because a flat export may spell the role there with a token the role mapping
// does not know (human, ai, model), and leaving that key out would import the
// user's turn as assistant text.
func openCodeRawMessageRole(obj map[string]json.RawMessage) message.Role {
	if role := openCodeMessageRole(openCodeRawRoleText(obj)); role != "" {
		return role
	}
	switch strings.ToLower(strings.TrimSpace(pickStringField(obj, "type", "kind", "role"))) {
	case openCodeRoleUser, openCodeRoleHuman:
		return message.RoleUser
	case openCodeRoleAssistant, openCodeRoleAI, openCodeRoleModel:
		return message.RoleAssistant
	}
	return ""
}

// openCodeRawRoleText returns the role text a raw export message declares,
// which is what the unknown-role warning reports.
func openCodeRawRoleText(obj map[string]json.RawMessage) string {
	roleText := strings.ToLower(strings.TrimSpace(pickStringField(obj, "role", "sender")))
	if roleText == "" {
		roleText = strings.ToLower(strings.TrimSpace(pickNestedStringField(obj, "info", "role", "sender")))
	}
	return roleText
}

// openCodeSummaryText joins the text parts of a summary message into the body
// the source paired with it.
func openCodeSummaryText(obj map[string]json.RawMessage) string {
	if rawParts, ok := obj["parts"]; ok {
		return openCodeTextPartsBody(rawParts, nil)
	}
	return strings.TrimSpace(pickStringField(obj, "text", "content"))
}

// openCodeRawUserText extracts the user-visible text of a raw export message:
// its text parts in a v1 export, its text field in a flat one. Ignored and
// synthetic parts are left out — the source itself does not count them as the
// user's words.
func openCodeRawUserText(obj map[string]json.RawMessage) string {
	if rawParts, ok := obj["parts"]; ok {
		return openCodeTextPartsBody(rawParts, openCodePartCountsAsUserWords)
	}
	if rawContent, ok := obj["content"]; ok {
		if text := openCodeContentArrayUserText(rawContent); text != "" {
			return text
		}
	}
	return strings.TrimSpace(pickStringField(obj, "text", "content"))
}

func openCodeContentArrayUserText(rawContent json.RawMessage) string {
	var content []any
	if json.Unmarshal(rawContent, &content) != nil {
		return ""
	}
	var texts []string
	for _, item := range content {
		switch block := item.(type) {
		case string:
			if text := strings.TrimSpace(block); text != "" {
				texts = append(texts, text)
			}
		case map[string]any:
			if !openCodePartCountsAsUserWords(block) {
				continue
			}
			text, typ, ok := extractTextFromBlock(block)
			if !ok || (typ != "" && !strings.EqualFold(strings.TrimSpace(typ), openCodePartTypeText)) {
				continue
			}
			if text = strings.TrimSpace(text); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

// openCodeTextPartsBody joins the text parts of a decoded parts array into one
// body, or "" when the array cannot be decoded. keep optionally rejects a part
// that is a text part but should not count; nil keeps every text part.
func openCodeTextPartsBody(rawParts json.RawMessage, keep func(part map[string]any) bool) string {
	var parts []map[string]any
	if json.Unmarshal(rawParts, &parts) != nil {
		return ""
	}
	var texts []string
	for _, part := range parts {
		if typ, _ := part["type"].(string); !strings.EqualFold(strings.TrimSpace(typ), openCodePartTypeText) {
			continue
		}
		if keep != nil && !keep(part) {
			continue
		}
		if s, ok := part["text"].(string); ok && strings.TrimSpace(s) != "" {
			texts = append(texts, strings.TrimSpace(s))
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n\n"))
}

// openCodePartCountsAsUserWords reports whether a text part is the user's own
// words rather than something the source recorded beside them.
func openCodePartCountsAsUserWords(part map[string]any) bool {
	if ignored, _ := part["ignored"].(bool); ignored {
		return false
	}
	if synthetic, _ := part["synthetic"].(bool); synthetic {
		return false
	}
	return true
}

// openCodePartString reads a string field from a decoded part under any of the
// given keys.
func openCodePartString(part map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := part[key].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// openCodeRawBoolField reports whether a raw object field is the boolean true,
// which is how a summary message is flagged (ordinary messages carry a summary
// object instead).
func openCodeRawBoolField(info map[string]json.RawMessage, key string) bool {
	raw, ok := info[key]
	if !ok {
		return false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

// openCodeRawIsPresent reports whether a raw field carries a value rather than
// an explicit null.
func openCodeRawIsPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// openCodeHasError reports whether a raw message records an error.
func openCodeHasError(info map[string]json.RawMessage) bool {
	raw, ok := info["error"]
	return ok && openCodeRawIsPresent(raw)
}

// openCodeTimeCompleted reports whether a raw message's time records a
// completion, which is what makes a summary usable.
func openCodeTimeCompleted(info map[string]json.RawMessage) bool {
	raw, ok := info["time"]
	if !ok {
		return false
	}
	var timeObj map[string]json.RawMessage
	if json.Unmarshal(raw, &timeObj) != nil {
		return false
	}
	completed, ok := timeObj["completed"]
	return ok && openCodeRawIsPresent(completed)
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
					b.WriteString("[non-text content block]\n")
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
	var attachments []message.ContentPart
	flushText := func() {
		content := strings.TrimSpace(strings.Join(rendered, "\n\n"))
		rendered = nil
		pending := attachments
		attachments = nil
		if content == "" && len(pending) == 0 {
			return
		}
		msgs = append(msgs, message.Message{Role: role, Content: content, Parts: pending, Provenance: importedOpenCodeProvenance()})
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
			callMsg, resultMsg, ok, mapped := openCodeStructuredToolMessages(part)
			if ok {
				flushText()
				msgs = append(msgs, callMsg)
				if mapped {
					structuredToolCalls++
				} else {
					// No Chord tool performs this work, so the part is kept
					// verbatim under the name the source agent used; the call
					// still ran, and only its mapping is missing.
					unsupportedToolCount++
				}
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
			switch {
			case name == "":
				warns = append(warns, "tool part missing name; imported as text")
			case strings.TrimSpace(openCodeToolCallID(part)) == "":
				warns = append(warns, "tool part missing call id; imported as text")
			default:
				warns = append(warns, "tool arguments could not be encoded; imported as text")
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
		case openCodePartTypeCompaction:
			// A compaction marker is UI-only metadata: the summary it pairs
			// with is what the model saw, and the cross-message compaction
			// pass maps that pair onto a checkpoint. Left in the transcript it
			// would land as a raw JSON user bubble that claims to be user
			// input.
		case openCodePartTypePatch:
			// A patch part is OpenCode's own ledger of the files an edit
			// touched (a hash and a path list); the edit itself is already the
			// tool call beside it, so importing the ledger only restates it.
		case openCodePartTypeFile:
			// An attached file carries its bytes inline as a data URL, so the
			// payload is the attachment the model received and becomes a Chord
			// image/PDF part rather than megabytes of base64 in the text.
			attach, marker := openCodeFilePartAttachment(part)
			if attach != nil {
				flushText()
				attachments = append(attachments, *attach)
				continue
			}
			if marker != "" {
				rendered = append(rendered, marker)
			}
		case "":
			warns = append(warns, "part missing type; skipped")
		default:
			payload, _ := json.MarshalIndent(part, "", "  ")
			rendered = append(rendered, "["+typ+" part]\n"+string(payload))
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

// openCodeStructuredToolMessages turns a tool part into a Chord tool-call
// message plus its result. A part whose tool maps onto a Chord tool keeps the
// mapping and the tool's own argument shape — the closest tool is picked by
// what the arguments recorded, so a replacement imports as the replacement it
// was instead of being repackaged as another tool's input. A part no candidate
// reads stays a real call under the name and arguments the source agent used:
// it ran, so the history keeps the action instead of rewriting it into text or
// wrapping it in importer metadata. The model reads the preserved form as the
// completed action it was.
//
// The returned mapped flag tells the caller which of the two happened, so it can
// count the call as structured or as an unmapped-but-kept one.
func openCodeStructuredToolMessages(part map[string]any) (call message.Message, result *message.Message, ok bool, mapped bool) {
	name := openCodeToolName(part)
	if strings.TrimSpace(name) == "" {
		return message.Message{}, nil, false, false
	}
	callID := openCodeToolCallID(part)
	if strings.TrimSpace(callID) == "" {
		return message.Message{}, nil, false, false
	}
	args := openCodeToolArgs(part)
	if importedToolClosestIs(name, tools.NameShell) {
		args = importedShellTimeoutToSeconds(args)
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		return message.Message{}, nil, false, false
	}
	if chordName, norm, found := resolveImportedTool(name, rawArgs); found {
		callMsg, resultMsg := openCodeToolMessages(chordName, callID, norm, part)
		return callMsg, resultMsg, true, true
	}
	// No candidate could read the arguments, so the source name and arguments
	// are the pair that belong together and both are kept as they were issued
	// rather than mixing one with the other.
	callMsg, resultMsg := openCodeToolMessages(name, callID, rawArgs, part)
	return callMsg, resultMsg, true, false
}

func openCodeToolMessages(name, callID string, args json.RawMessage, part map[string]any) (message.Message, *message.Message) {
	call := message.Message{
		Role: message.RoleAssistant,
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: name,
			Args: args,
		}},
		Provenance: importedOpenCodeProvenance(),
	}
	output, hasOutput := openCodeToolOutputValue(part)
	status := openCodeToolStatus(part, hasOutput)
	if !hasOutput && status == "" {
		// A call the source never finished (streaming or still running) has no
		// result to pair with it.
		return call, nil
	}
	result := message.Message{
		Role:       message.RoleTool,
		ToolCallID: callID,
		Content:    output,
		ToolStatus: status,
		Provenance: importedOpenCodeProvenance(),
	}
	return call, &result
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

// openCodeFilePartAttachment reads an OpenCode file part into a Chord binary
// content part. The export carries the bytes inline as a data URL, so decoding
// them keeps the attachment the model actually received. A part with no
// decodable inline bytes — a path or remote URL the import cannot resolve —
// returns a short marker instead, so a payload is never inlined as text.
func openCodeFilePartAttachment(part map[string]any) (*message.ContentPart, string) {
	mime, _ := firstString(part, "mime", "mimeType", "mediaType")
	url, _ := firstString(part, "url", "data", "uri", "src")
	name, _ := firstString(part, "filename", "name", "path")
	payload, dataMime, ok := decodeOpenCodeDataURL(url)
	if mime == "" {
		mime = dataMime
	}
	partType := openCodeAttachmentPartType(mime)
	if ok && partType != "" {
		if limit := openCodeAttachmentLimit(mime); len(payload) > limit {
			// An inline payload is not bounded by anything on the producer
			// side, so cap it with the limits Chord already applies to a
			// pasted image or PDF rather than writing an attachment the
			// preview path would refuse to read back.
			return nil, openCodeAttachmentMarker(name, mime, url) + oversizeAttachmentSuffix(len(payload), limit)
		}
		return &message.ContentPart{
			Type:     partType,
			MimeType: mime,
			Data:     payload,
			FileName: openCodeAttachmentFileName(name, mime),
		}, ""
	}
	return nil, openCodeAttachmentMarker(name, mime, url)
}

// openCodeAttachmentLimit bounds the inline payload an import carries, using
// the same limits the clipboard reader enforces, so an imported attachment is
// never larger than one a user could have pasted.
func openCodeAttachmentLimit(mime string) int {
	if mime == "application/pdf" {
		return imageutil.MaxPDFBytes
	}
	return imageutil.MaxImageSourceBytes
}

// oversizeAttachmentSuffix explains why an inline payload was not carried, so
// the marker names the limit that dropped it instead of only the file.
func oversizeAttachmentSuffix(size, limit int) string {
	return fmt.Sprintf("\n[dropped: %d bytes over the %d-byte import limit]", size, limit)
}

// decodeOpenCodeDataURL splits an inline `data:` payload into its bytes and
// declared media type. Only base64 data URLs carry an attachment; anything else
// is left to the caller.
func decodeOpenCodeDataURL(url string) ([]byte, string, bool) {
	const prefix = "data:"
	if !strings.HasPrefix(url, prefix) {
		return nil, "", false
	}
	meta, payload, found := strings.Cut(strings.TrimPrefix(url, prefix), ",")
	if !found || !strings.Contains(meta, ";base64") {
		return nil, "", false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	mime, _, _ := strings.Cut(meta, ";")
	return data, strings.ToLower(strings.TrimSpace(mime)), true
}

func openCodeAttachmentPartType(mime string) message.ContentPartType {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return message.ContentPartImage
	case mime == "application/pdf":
		return message.ContentPartPDF
	}
	return ""
}

// openCodeAttachmentFileName keeps the name the source recorded, adding the
// extension its media type implies when the name has none, so a persisted
// attachment file is recognizable without inspecting its bytes.
func openCodeAttachmentFileName(name, mime string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if filepath.Ext(name) == "" {
		return name + openCodeAttachmentExt(mime)
	}
	return name
}

func openCodeAttachmentExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	default:
		return ".png"
	}
}

// openCodeAttachmentMarker names an attachment whose bytes the import cannot
// carry inline. A path or remote URL is kept so the reference stays resolvable;
// an inline payload is dropped because it has no standalone location.
func openCodeAttachmentMarker(name, mime, url string) string {
	var b strings.Builder
	b.WriteString("[attachment]")
	if name != "" {
		b.WriteString(" ")
		b.WriteString(name)
	}
	if mime != "" {
		b.WriteString(" (")
		b.WriteString(mime)
		b.WriteString(")")
	}
	if url != "" && !strings.HasPrefix(url, "data:") {
		b.WriteString("\n")
		b.WriteString(url)
	}
	return b.String()
}

func openCodeToolArgs(part map[string]any) map[string]any {
	// state.input is the current schema; it is an object for completed calls.
	// For unfinished calls the runtime may store a string or array instead;
	// wrap those so the arguments stay a valid JSON object rather than
	// silently becoming {}.
	if state, ok := part["state"].(map[string]any); ok {
		if input, ok := state["input"]; ok {
			if obj, ok := input.(map[string]any); ok {
				return obj
			}
			return map[string]any{"input": input}
		}
	}
	if input, ok := part["input"]; ok {
		if obj, ok := input.(map[string]any); ok {
			return obj
		}
		return map[string]any{"input": input}
	}
	if args, ok := part["args"]; ok {
		if obj, ok := args.(map[string]any); ok {
			return obj
		}
		return map[string]any{"input": args}
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
		if s, ok := state["result"].(string); ok {
			return s, true
		}
		// Current exports carry the result as a Tool.Content array, and a
		// failed tool as a structured error, instead of a plain output string.
		if s, ok := openCodeToolStateText(state["content"]); ok {
			return s, true
		}
		if s, ok := openCodeToolStateError(state["error"]); ok {
			return s, true
		}
	}
	if s, ok := part["output"].(string); ok {
		return s, true
	}
	if s, ok := part["result"].(string); ok {
		return s, true
	}
	if s, ok := openCodeToolStateText(part["content"]); ok {
		return s, true
	}
	if s, ok := openCodeToolStateError(part["error"]); ok {
		return s, true
	}
	return "", false
}

// openCodeToolStateText joins a Tool.Content array — {"type":"text","text"} or
// {"type":"file","uri","name"} — into the flat text a Chord tool result holds.
func openCodeToolStateText(v any) (string, bool) {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return "", false
	}
	var parts []string
	for _, item := range items {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			if s, ok := block["text"].(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, s)
			}
		case "file":
			// uri is the payload's required location; name is an optional
			// display label, so keeping one never replaces the other. An
			// imported result is flat text, so both facts survive together
			// instead of the attachment silently losing where the file is.
			name, _ := block["name"].(string)
			name = strings.TrimSpace(name)
			uri, _ := block["uri"].(string)
			uri = strings.TrimSpace(uri)
			switch {
			case name != "" && uri != "":
				parts = append(parts, fmt.Sprintf("%s (%s)", name, uri))
			case name != "":
				parts = append(parts, name)
			case uri != "":
				parts = append(parts, uri)
			}
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

// openCodeToolStateError reads the structured error a failed tool carries, so
// the failure reaches the transcript instead of only its status.
func openCodeToolStateError(v any) (string, bool) {
	err, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	if s, ok := err["message"].(string); ok && strings.TrimSpace(s) != "" {
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
		return joinNonEmpty("[shell]", pretty), warns
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
