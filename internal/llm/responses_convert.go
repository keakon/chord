package llm

import (
	"encoding/json"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

// responsesReasoningContentBlock is a reasoning item content part carrying
// plaintext chain-of-thought (DeepSeek-family Responses replay). Text is always
// serialized so a synthesized item keeps the reasoning_text shape even when no
// plaintext reasoning is available.
type responsesReasoningContentBlock struct {
	Type string `json:"type"` // "reasoning_text"
	Text string `json:"text"`
}

// fillResponsesReasoningForReplay inserts an empty reasoning item before each
// assistant turn that has no reasoning item. DeepSeek V4 Responses thinking
// mode requires reasoning_text for replayed function-call turns; the empty
// item is the optimistic first shape for turns whose native reasoning was
// dropped across a model switch or compaction. A backend that requires the
// actual text rejects it, and the retry ladder then textifies the tool
// trajectory instead. Input that already carries reasoning on every turn is
// returned untouched, so the common case copies nothing.
func fillResponsesReasoningForReplay(items []responsesInputItem) []responsesInputItem {
	insertAt := responsesReplayTurnsMissingReasoning(items)
	if len(insertAt) == 0 {
		return items
	}
	out := make([]responsesInputItem, 0, len(items)+len(insertAt))
	next := 0
	for i, item := range items {
		if next < len(insertAt) && insertAt[next] == i {
			out = append(out, emptyResponsesReasoningItem())
			next++
		}
		out = append(out, item)
	}
	return out
}

// responsesReplayTurnsMissingReasoning returns the item indices before which a
// synthesized reasoning item must be inserted: the start of every assistant
// turn that opens without one. Returning positions instead of a rebuilt slice
// keeps the no-op case allocation-free on long replayed conversations.
//
// Turn-start detection covers both call-item shapes: replayed apply_patch is a
// custom_tool_call under a freeform target (see toCustomApplyPatchCallItem)
// and a function_call otherwise, so reasoning continuity must recognize both —
// a freeform target with reasoning active otherwise omits the empty
// reasoning_text the backend requires for turns that open with a custom tool
// call.
func responsesReplayTurnsMissingReasoning(items []responsesInputItem) []int {
	var insertAt []int
	inTurn := false
	for i, item := range items {
		startsTurn := (item.Type == "message" && item.Role == "assistant") || isResponsesCallItemType(item.Type)
		if startsTurn && !inTurn {
			insertAt = append(insertAt, i)
		}
		switch item.Type {
		case "reasoning", "function_call", "custom_tool_call":
			inTurn = true
		case "message":
			inTurn = item.Role == "assistant"
		default:
			inTurn = false
		}
	}
	return insertAt
}

// emptyResponsesReasoningItem builds the placeholder reasoning item replayed
// for turns whose native reasoning was dropped. It deliberately carries no id:
// the item references no server-stored state, and a synthetic id would be
// rejected by backends that resolve ids against a stored response.
func emptyResponsesReasoningItem() responsesInputItem {
	return responsesInputItem{
		Type:    "reasoning",
		Content: []responsesReasoningContentBlock{{Type: "reasoning_text"}},
		Summary: &[]responsesReasoningSummaryPayload{},
	}
}

// isResponsesCallItemType reports whether t is a call item that pairs with an
// immediately following output item in the Responses wire. Replayed apply_patch
// alternates between function_call (JSON shape) and custom_tool_call (freeform)
// depending on the request target, so call-shape logic must recognize both.
func isResponsesCallItemType(t string) bool {
	return t == "function_call" || t == "custom_tool_call"
}

// applyPatchFreeformInput extracts the raw Codex patch text from stored
// apply_patch arguments. Stored args are either the canonical {"patch": ...}
// object, a JSON string wrapping it, or bare patch text (legacy freeform
// history); the returned text is what a freeform custom_tool_call must carry in
// its input field.
func applyPatchFreeformInput(raw json.RawMessage) string {
	text := unwrapJSONString(raw)
	if json.Valid(text) {
		var obj struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(text, &obj); err == nil && obj.Patch != "" {
			return obj.Patch
		}
	}
	return string(text)
}

// appendResponsesTextBlock folds a text part into the trailing input_text block
// when there is one, otherwise starts a new one. Blank parts never create a
// block of their own. Adjacent pure-text parts are merged so a message that is
// only text takes the canonical single-block shape instead of emitting one
// block per part; a newline is inserted between parts only when neither side
// already provides one, keeping separately-authored segments (git status, user
// input, pasted content, <file> refs) from being glued together. image/pdf
// parts are never merged into a text block.
func appendResponsesTextBlock(content *[]responsesContentBlock, text string) {
	if text == "" {
		return
	}
	if last := len(*content) - 1; last >= 0 && (*content)[last].Type == "input_text" {
		prev := &(*content)[last]
		prev.Text = joinAdjacentPartText(prev.Text, text)
		return
	}
	*content = append(*content, responsesContentBlock{Type: "input_text", Text: text})
}

// findResponsesCallIndex returns the index of the tool call with the given ID
// in calls, or -1 when absent.
func findResponsesCallIndex(calls []message.ToolCall, id string) int {
	for i, tc := range calls {
		if tc.ID == id {
			return i
		}
	}
	return -1
}

// responsesApplyPatchCallIDs returns the set of apply_patch call IDs anywhere
// in history (both legacy ToolCalls and native ResponsesOutput). The tool-result
// replay shape must follow the call it pairs with, even for orphan outputs that
// are handled outside the interleaving assistant blocks.
func responsesApplyPatchCallIDs(msgs []message.Message) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.Name == toolname.ApplyPatch && tc.ID != "" {
				ids[tc.ID] = struct{}{}
			}
		}
		for _, item := range m.ResponsesOutput {
			if item.Name == toolname.ApplyPatch && item.CallID != "" {
				ids[item.CallID] = struct{}{}
			}
		}
	}
	return ids
}

// toCustomApplyPatchCallItem rewrites a replayed apply_patch function_call item
// into the freeform custom_tool_call shape (raw patch text in input) when the
// current request target emits apply_patch freeform.
func toCustomApplyPatchCallItem(item responsesInputItem, freeform bool) responsesInputItem {
	if !freeform || item.Name != toolname.ApplyPatch {
		return item
	}
	item.Type = "custom_tool_call"
	item.Status = "completed"
	item.Input = applyPatchFreeformInput(json.RawMessage(item.Arguments))
	item.Arguments = ""
	return item
}

// toMatchingResponsesToolOutput types a replayed tool result to pair with the
// call it belongs to: apply_patch replays as custom_tool_call_output under a
// freeform target, everything else stays function_call_output. isApplyPatch is
// true when the paired call was an apply_patch call.
func toMatchingResponsesToolOutput(item responsesInputItem, isApplyPatch, freeform bool) responsesInputItem {
	if freeform && isApplyPatch {
		item.Type = "custom_tool_call_output"
	}
	return item
}

func convertMessagesToResponsesWithItemIDs(systemPrompt string, msgs []message.Message, includeItemIDs bool, freeform bool) []responsesInputItem {
	// Always return a non-nil slice to ensure JSON marshaling produces [] instead of null.
	result := make([]responsesInputItem, 0)

	// Add system prompt as first item.
	// Some reasoning models require "developer" role instead of "system".
	// For now, use "system" and let the caller override if needed.
	if systemPrompt != "" {
		result = append(result, responsesInputItem{
			Type: "message",
			Role: "system",
			Content: []responsesContentBlock{
				{Type: "input_text", Text: systemPrompt},
			},
		})
	}

	applyPatchCallIDs := responsesApplyPatchCallIDs(msgs)
	for i := 0; i < len(msgs); i++ {
		msg := msgs[i]
		if len(msg.MCPTools) > 0 {
			result = append(result, responsesInputItem{
				Type:  "additional_tools",
				Role:  responsesAdditionalToolsRole,
				Tools: convertToolsToResponses(msg.MCPTools),
			})
			continue
		}
		switch msg.Role {
		case "user":
			content := make([]responsesContentBlock, 0, max(1, len(msg.Parts)))
			if len(msg.Parts) > 0 {
				for _, p := range msg.Parts {
					switch p.Type {
					case "image":
						content = append(content, responsesContentBlock{
							Type:     "input_image",
							ImageURL: "data:" + p.MimeType + ";base64," + encodeBase64Cached(binaryPartPayload(p)),
							Detail:   "auto",
						})
					case "pdf":
						content = append(content, responsesContentBlock{
							Type:     "input_file",
							Filename: defaultPDFFilename(p.FileName),
							FileData: "data:" + defaultPDFMediaType(p.MimeType) + ";base64," + encodeBase64Cached(binaryPartPayload(p)),
						})
					default:
						appendResponsesTextBlock(&content, p.Text)
					}
				}
				if len(content) == 0 {
					content = append(content, responsesContentBlock{Type: "input_text", Text: ""})
				}
			} else {
				content = append(content, responsesContentBlock{Type: "input_text", Text: msg.Content})
			}
			result = append(result, responsesInputItem{
				Type:    "message",
				Role:    "user",
				Content: content,
			})

		case "assistant":
			if len(msg.ResponsesOutput) > 0 {
				// Native ResponsesOutput replays provider-ordered items; tool
				// results are separate messages that follow. A trailing run of
				// function_calls (no interleaved reasoning after the last
				// non-call item) can each be followed immediately by its
				// adjacent tool result to keep call-then-output wire order,
				// which Chat-Completions-converting gateways require. When the
				// provider interleaved reasoning between calls, earlier calls
				// keep their provider position and their tool results append
				// after the sequence via the "tool" case below.
				lastNonCall := -1
				for j, item := range msg.ResponsesOutput {
					if item.Type != "function_call" {
						lastNonCall = j
					}
				}
				adjacent := msgs[i+1:]
				outputs, consumed := collectAdjacentResponsesToolOutputs(adjacent, trailingResponsesCallIDs(msg.ResponsesOutput, lastNonCall))
				for idx, item := range msg.ResponsesOutput {
					converted, ok := convertResponsesOutputItem(item, includeItemIDs, freeform)
					if !ok {
						continue
					}
					result = append(result, converted)
					if idx <= lastNonCall || !isResponsesCallItemType(converted.Type) {
						continue
					}
					if output, ok := outputs[item.CallID]; ok {
						_, isApplyPatch := applyPatchCallIDs[item.CallID]
						result = append(result, toMatchingResponsesToolOutput(output, isApplyPatch, freeform))
					}
				}
				i += consumed
				continue
			}
			contentText := assistantContentForReplay(msg)
			validToolCalls := make([]message.ToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" || tc.Name == "" {
					log.Warnf("skipping function_call with empty id or name in history tool=%v id=%v", tc.Name, tc.ID)
					continue
				}
				validToolCalls = append(validToolCalls, tc)
			}
			if contentText == "" && len(validToolCalls) == 0 {
				log.Warn("skipping empty/reasoning-only assistant message in Responses history")
				continue
			}
			// Output text content.
			if contentText != "" {
				result = append(result, responsesInputItem{
					Type: "message",
					Role: "assistant",
					Content: []responsesContentBlock{
						{Type: "output_text", Text: contentText},
					},
				})
			}
			if len(validToolCalls) == 0 {
				continue
			}
			// Interleave each function_call with its matching tool result so
			// every call is immediately followed by its function_call_output.
			// This is the canonical Responses input shape for parallel tool
			// calls and is required by Chat-Completions-converting gateways
			// (grouped calls first then outputs get rejected with "assistant
			// message with 'tool_calls' must be followed by tool messages").
			// Only consume adjacent tool results that match one of these
			// calls; the first unmatched/blank one (and everything after) is
			// left for the "tool" case below to keep orphan results intact.
			outputs := make(map[string]responsesInputItem, len(validToolCalls))
			matched := 0
			for i+1 < len(msgs) && msgs[i+1].Role == "tool" && matched < len(validToolCalls) {
				tm := msgs[i+1]
				if tm.ToolCallID == "" || findResponsesCallIndex(validToolCalls, tm.ToolCallID) < 0 {
					break
				}
				i++
				matched++
				if _, ok := outputs[tm.ToolCallID]; !ok {
					outputs[tm.ToolCallID] = responsesInputItem{
						Type:   "function_call_output",
						CallID: tm.ToolCallID,
						Output: responsesToolOutput(tm),
					}
				}
			}
			// Tool calls become call items replayed in the shape the current
			// request declares for the tool — apply_patch as custom_tool_call
			// under a freeform target, function_call otherwise — each followed
			// by its matching output. API expects arguments as a string.
			for _, tc := range validToolCalls {
				result = append(result, toCustomApplyPatchCallItem(responsesInputItem{
					Type:      "function_call",
					Name:      tc.Name,
					CallID:    tc.ID,
					Arguments: string(tc.Args),
				}, freeform))
				if out, ok := outputs[tc.ID]; ok {
					result = append(result, toMatchingResponsesToolOutput(out, tc.Name == toolname.ApplyPatch, freeform))
				}
			}

		case "tool":
			// Tool results not consumed by the interleave above (orphan or
			// blank-call results) keep the plain function_call_output shape.
			// Skip tool results with empty call id — they correspond to
			// malformed tool calls (e.g. from GLM) that were also skipped.
			if msg.ToolCallID == "" {
				log.Warn("skipping function_call_output with empty call_id in history")
				continue
			}
			// Tool results become output items typed to pair with the call they
			// answer: an apply_patch call replayed as custom_tool_call under a
			// freeform target must be answered by a custom_tool_call_output, or
			// the server rejects the mismatched pair. When the tool result
			// carries image/file parts, Responses accepts output content blocks.
			_, isApplyPatch := applyPatchCallIDs[msg.ToolCallID]
			result = append(result, toMatchingResponsesToolOutput(responsesInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: responsesToolOutput(msg),
			}, isApplyPatch, freeform))
		}
	}

	return result
}

func trailingResponsesCallIDs(output []message.ResponsesOutputItem, lastNonCall int) map[string]struct{} {
	if len(output) == 0 {
		return nil
	}
	callIDs := make(map[string]struct{})
	for idx, item := range output {
		if idx <= lastNonCall || item.Type != "function_call" || strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" {
			continue
		}
		callIDs[item.CallID] = struct{}{}
	}
	return callIDs
}

func collectAdjacentResponsesToolOutputs(msgs []message.Message, allowed map[string]struct{}) (map[string]responsesInputItem, int) {
	if len(msgs) == 0 || len(allowed) == 0 {
		return nil, 0
	}
	outputs := make(map[string]responsesInputItem)
	consumed := 0
	for _, tm := range msgs {
		if tm.Role != "tool" || tm.ToolCallID == "" {
			break
		}
		if _, ok := allowed[tm.ToolCallID]; !ok {
			break
		}
		consumed++
		if _, ok := outputs[tm.ToolCallID]; ok {
			continue
		}
		outputs[tm.ToolCallID] = responsesInputItem{
			Type:   "function_call_output",
			CallID: tm.ToolCallID,
			Output: responsesToolOutput(tm),
		}
	}
	return outputs, consumed
}

func convertResponsesOutputItem(item message.ResponsesOutputItem, includeItemID bool, freeform bool) (responsesInputItem, bool) {
	converted := responsesInputItem{
		Type:             item.Type,
		Role:             item.Role,
		Name:             item.Name,
		CallID:           item.CallID,
		Arguments:        item.Arguments,
		Phase:            item.Phase,
		EncryptedContent: item.EncryptedContent,
	}
	if includeItemID {
		converted.ID = item.ID
	}
	switch item.Type {
	case "reasoning":
		content := make([]responsesReasoningContentBlock, 0, len(item.Content))
		for _, entry := range item.Content {
			if entry.Type == "reasoning_text" {
				content = append(content, responsesReasoningContentBlock{Type: entry.Type, Text: entry.Text})
			}
		}
		if strings.TrimSpace(item.EncryptedContent) == "" && len(content) == 0 && (!includeItemID || strings.TrimSpace(item.ID) == "") {
			return responsesInputItem{}, false
		}
		if len(content) > 0 {
			converted.Content = content
		}
		summary := make([]responsesReasoningSummaryPayload, 0, len(item.Summary))
		for _, entry := range item.Summary {
			summary = append(summary, responsesReasoningSummaryPayload{Type: entry.Type, Text: entry.Text})
		}
		converted.Summary = &summary
	case "message":
		content := make([]responsesContentBlock, 0, len(item.Content))
		for _, entry := range item.Content {
			content = append(content, responsesContentBlock{Type: entry.Type, Text: entry.Text, Refusal: entry.Refusal})
		}
		if len(content) == 0 {
			return responsesInputItem{}, false
		}
		converted.Content = content
	case "function_call":
		if strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" {
			return responsesInputItem{}, false
		}
		converted.Arguments = string(toolArgumentsFallback(item.Name, json.RawMessage(item.Arguments)))
		// Freeform-targeted requests replay apply_patch history in the custom
		// tool shape so the model sees the same wire form it is asked to emit.
		converted = toCustomApplyPatchCallItem(converted, freeform)
	case "custom_tool_call":
		// Normalized to function_call at the wire boundary; this case is only
		// reachable from hand-constructed or legacy data. Canonicalize the args
		// so replay and tool execution still see the {patch} object.
		converted.Type = "function_call"
		converted.Arguments = string(canonicalApplyPatchArgs(json.RawMessage(item.Arguments)))
		if strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" {
			return responsesInputItem{}, false
		}
		converted = toCustomApplyPatchCallItem(converted, freeform)
	default:
		return responsesInputItem{}, false
	}
	return converted, true
}

func responsesToolOutput(msg message.Message) any {
	if len(msg.Parts) == 0 {
		return msg.Content
	}
	content := make([]responsesContentBlock, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		switch p.Type {
		case "image":
			content = append(content, responsesContentBlock{
				Type:     "input_image",
				ImageURL: "data:" + p.MimeType + ";base64," + encodeBase64Cached(binaryPartPayload(p)),
				Detail:   "auto",
			})
		case "pdf":
			content = append(content, responsesContentBlock{
				Type:     "input_file",
				Filename: defaultPDFFilename(p.FileName),
				FileData: "data:" + defaultPDFMediaType(p.MimeType) + ";base64," + encodeBase64Cached(binaryPartPayload(p)),
			})
		default:
			if p.Text == "" {
				continue
			}
			content = append(content, responsesContentBlock{Type: "input_text", Text: p.Text})
		}
	}
	if len(content) == 0 {
		return msg.Content
	}
	return content
}

// convertToolsToResponses converts tool definitions to Responses API format.
// Tools are expected to be in a stable order from Registry.ListDefinitions().
func convertToolsToResponses(tools []message.ToolDefinition) []responsesTool {
	result := make([]responsesTool, 0, len(tools))
	if len(tools) == 0 {
		return result
	}

	for _, t := range tools {
		result = append(result, responsesTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
			Strict:      new(false),
		})
	}
	return result
}

// responsesOutputToInputItems always retains item IDs; the incremental
// baseline is normalized for the store mode in one place, codexWSBuildBaseline.
func responsesOutputToInputItems(output []responsesOutputEntry, freeform bool) []responsesInputItem {
	if len(output) == 0 {
		return nil
	}
	items := make([]responsesInputItem, 0, len(output))
	for _, out := range output {
		converted, ok := convertResponsesOutputItem(responsesOutputEntryToMessageItem(out), true, freeform)
		if ok {
			items = append(items, converted)
		}
	}
	return items
}

func responsesOutputEntryToMessageItem(out responsesOutputEntry) message.ResponsesOutputItem {
	out = normalizeResponsesOutputEntry(out)
	role := out.Role
	if strings.TrimSpace(role) == "" && out.Type == "message" {
		role = "assistant"
	}
	callID := out.CallID
	if strings.TrimSpace(callID) == "" && out.Type == "function_call" {
		callID = out.ID
	}
	item := message.ResponsesOutputItem{
		Type:             out.Type,
		ID:               out.ID,
		CallID:           callID,
		Role:             role,
		Name:             out.Name,
		Arguments:        out.Arguments,
		Phase:            out.Phase,
		EncryptedContent: out.EncryptedContent,
	}
	for _, content := range out.Content {
		item.Content = append(item.Content, message.ResponsesOutputContent{Type: content.Type, Text: content.Text, Refusal: content.Refusal})
	}
	for _, summary := range out.Summary {
		item.Summary = append(item.Summary, message.ResponsesReasoningSummary{Type: summary.Type, Text: summary.Text})
	}
	return item
}

func responsesToolCallsToInputItems(calls []message.ToolCall, freeform bool) []responsesInputItem {
	if len(calls) == 0 {
		return nil
	}
	items := make([]responsesInputItem, 0, len(calls))
	for _, tc := range calls {
		if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(tc.Name) == "" {
			continue
		}
		converted := responsesInputItem{
			Type:      "function_call",
			Name:      tc.Name,
			CallID:    tc.ID,
			Arguments: string(tc.Args),
		}
		items = append(items, toCustomApplyPatchCallItem(converted, freeform))
	}
	return items
}

func responsesResponseToInputItems(resp *message.Response, freeform bool) []responsesInputItem {
	if resp == nil {
		return nil
	}
	if len(resp.ResponsesOutput) > 0 {
		items := make([]responsesInputItem, 0, len(resp.ResponsesOutput))
		for _, output := range resp.ResponsesOutput {
			converted, ok := convertResponsesOutputItem(output, true, freeform)
			if ok {
				items = append(items, converted)
			}
		}
		return items
	}
	items := make([]responsesInputItem, 0, 1+len(resp.ToolCalls))
	if contentText := assistantContentForReplay(message.Message{Content: resp.Content, StopReason: resp.StopReason}); strings.TrimSpace(contentText) != "" {
		items = append(items, responsesInputItem{
			Type: "message",
			Role: "assistant",
			Content: []responsesContentBlock{
				{Type: "output_text", Text: contentText},
			},
		})
	}
	items = append(items, responsesToolCallsToInputItems(resp.ToolCalls, freeform)...)
	return items
}
