package llm

import (
	"github.com/keakon/golog/log"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// convertMessagesToResponses converts internal messages to Responses API input format.
func convertMessagesToResponses(systemPrompt string, msgs []message.Message) []responsesInputItem {
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
			// Output text content.
			if msg.Content != "" {
				result = append(result, responsesInputItem{
					Type: "message",
					Role: "assistant",
					Content: []responsesContentBlock{
						{Type: "output_text", Text: msg.Content},
					},
				})
			}
			// Tool calls become function_call items. API expects arguments as a string.
			for _, tc := range msg.ToolCalls {
				// Skip tool calls with empty id or name — malformed responses from
				// some models (e.g. GLM) that omit these fields cause 400 errors
				// (Responses API requires a non-empty call_id).
				if tc.ID == "" || tc.Name == "" {
					log.Warnf("skipping function_call with empty id or name in history tool=%v id=%v", tc.Name, tc.ID)
					continue
				}
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
			// Tool results become function_call_output items.
			// Use pointer so omitempty keeps the field (even when "") for this type,
			// while omitting it for message/function_call items (where Output is nil).
			output := msg.Content
			result = append(result, responsesInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: &output,
			})
		}
	}

	return result
}

// convertToolsToResponses converts internal tool definitions to Responses API format.
func convertToolsToResponses(tools []message.ToolDefinition) []responsesTool {
	if len(tools) == 0 {
		return nil
	}

	result := make([]responsesTool, len(tools))
	for i, t := range tools {
		result[i] = responsesTool{
			Type:       "function",
			Name:       t.Name,
			Parameters: t.InputSchema,
		}
	}
	return result
}

func responsesOutputToInputItems(output []responsesOutputEntry) []responsesInputItem {
	if len(output) == 0 {
		return nil
	}
	items := make([]responsesInputItem, 0, len(output))
	for _, out := range output {
		switch out.Type {
		case "function_call":
			callID := out.CallID
			if callID == "" {
				callID = out.ID
			}
			items = append(items, responsesInputItem{
				Type:      "function_call",
				Name:      out.Name,
				CallID:    callID,
				Arguments: out.Arguments,
			})
		case "message":
			role := out.Role
			if strings.TrimSpace(role) == "" {
				role = "assistant"
			}
			content := make([]responsesContentBlock, 0, len(out.Content))
			for _, c := range out.Content {
				switch c.Type {
				case "output_text", "text":
					content = append(content, responsesContentBlock{Type: "output_text", Text: c.Text})
				}
			}
			if len(content) == 0 {
				continue
			}
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    role,
				Content: content,
			})
		}
	}
	return items
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
	items := make([]responsesInputItem, 0, 1+len(resp.ToolCalls))
	if strings.TrimSpace(resp.Content) != "" {
		items = append(items, responsesInputItem{
			Type: "message",
			Role: "assistant",
			Content: []responsesContentBlock{
				{Type: "output_text", Text: resp.Content},
			},
		})
	}
	items = append(items, responsesToolCallsToInputItems(resp.ToolCalls)...)
	return items
}
