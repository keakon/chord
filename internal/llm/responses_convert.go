package llm

import (
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// convertMessagesToResponses converts internal messages to Responses API input format.
func convertMessagesToResponses(systemPrompt string, msgs []message.Message) []responsesInputItem {
	return convertMessagesToResponsesWithItemIDs(systemPrompt, msgs, false)
}

func convertMessagesToResponsesWithItemIDs(systemPrompt string, msgs []message.Message, includeItemIDs bool) []responsesInputItem {
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

	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			content := make([]responsesContentBlock, 0, max(1, len(msg.Parts)))
			if len(msg.Parts) > 0 {
				for _, p := range msg.Parts {
					switch p.Type {
					case "image":
						content = append(content, responsesContentBlock{
							Type:     "input_image",
							ImageURL: "data:" + p.MimeType + ";base64," + encodeBase64Cached(p.Data),
							Detail:   "auto",
						})
					case "pdf":
						content = append(content, responsesContentBlock{
							Type:     "input_file",
							Filename: defaultPDFFilename(p.FileName),
							FileData: "data:" + defaultPDFMediaType(p.MimeType) + ";base64," + encodeBase64Cached(p.Data),
						})
					default:
						content = append(content, responsesContentBlock{Type: "input_text", Text: p.Text})
					}
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
				for _, item := range msg.ResponsesOutput {
					converted, ok := convertResponsesOutputItem(item, includeItemIDs)
					if ok {
						result = append(result, converted)
					}
				}
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
			// Tool calls become function_call items. API expects arguments as a string.
			for _, tc := range validToolCalls {
				result = append(result, responsesInputItem{
					Type:      "function_call",
					Name:      tc.Name,
					CallID:    tc.ID,
					Arguments: string(tc.Args),
				})
			}

		case "tool":
			// Skip tool results with empty call id — they correspond to malformed
			// tool calls (e.g. from GLM) that were also skipped above.
			if msg.ToolCallID == "" {
				log.Warn("skipping function_call_output with empty call_id in history")
				continue
			}
			// Tool results become function_call_output items. When the tool result
			// carries image/file parts, Responses accepts output content blocks.
			output := responsesToolOutput(msg)
			result = append(result, responsesInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: output,
			})
		}
	}

	return result
}

func convertResponsesOutputItem(item message.ResponsesOutputItem, includeItemID bool) (responsesInputItem, bool) {
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
		if strings.TrimSpace(item.EncryptedContent) == "" && (!includeItemID || strings.TrimSpace(item.ID) == "") {
			return responsesInputItem{}, false
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
				ImageURL: "data:" + p.MimeType + ";base64," + encodeBase64Cached(p.Data),
				Detail:   "auto",
			})
		case "pdf":
			content = append(content, responsesContentBlock{
				Type:     "input_file",
				Filename: defaultPDFFilename(p.FileName),
				FileData: "data:" + defaultPDFMediaType(p.MimeType) + ";base64," + encodeBase64Cached(p.Data),
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
			Type:       "function",
			Name:       t.Name,
			Parameters: t.InputSchema,
		})
	}
	return result
}

// responsesOutputToInputItems always retains item IDs; the incremental
// baseline is normalized for the store mode in one place, codexWSBuildBaseline.
func responsesOutputToInputItems(output []responsesOutputEntry) []responsesInputItem {
	if len(output) == 0 {
		return nil
	}
	items := make([]responsesInputItem, 0, len(output))
	for _, out := range output {
		converted, ok := convertResponsesOutputItem(responsesOutputEntryToMessageItem(out), true)
		if ok {
			items = append(items, converted)
		}
	}
	return items
}

func responsesOutputEntryToMessageItem(out responsesOutputEntry) message.ResponsesOutputItem {
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

func responsesToolCallsToInputItems(calls []message.ToolCall) []responsesInputItem {
	if len(calls) == 0 {
		return nil
	}
	items := make([]responsesInputItem, 0, len(calls))
	for _, tc := range calls {
		if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(tc.Name) == "" {
			continue
		}
		items = append(items, responsesInputItem{
			Type:      "function_call",
			Name:      tc.Name,
			CallID:    tc.ID,
			Arguments: string(tc.Args),
		})
	}
	return items
}

func responsesResponseToInputItems(resp *message.Response) []responsesInputItem {
	if resp == nil {
		return nil
	}
	if len(resp.ResponsesOutput) > 0 {
		items := make([]responsesInputItem, 0, len(resp.ResponsesOutput))
		for _, output := range resp.ResponsesOutput {
			converted, ok := convertResponsesOutputItem(output, true)
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
	items = append(items, responsesToolCallsToInputItems(resp.ToolCalls)...)
	return items
}
