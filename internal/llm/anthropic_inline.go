package llm

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// anthropicInlineToolAdditionType is the content-block type the inline-tools
// beta uses inside a mid-conversation role:system message to add one tool.
const anthropicInlineToolAdditionType = "tool_addition"

// AnthropicInlineToolsBeta is the anthropic-beta feature flag that enables
// mid-conversation tool_addition blocks on requests carrying declaration
// messages.
const AnthropicInlineToolsBeta = "inline-tools-2026-09-15"

// anthropicInlineDeclarationBlocks renders a declaration message into the
// content blocks the inline-tools beta accepts: an optional text block from the
// message content followed by one tool_addition block per declared tool. Only
// that mixture is legal on the wire, so anything else fails closed — callers
// fold non-renderable declaration messages into the top-level tools before a
// request is built.
func anthropicInlineDeclarationBlocks(msg message.Message) ([]anthropicContent, error) {
	if msg.Role != message.RoleSystem {
		return nil, fmt.Errorf("tool declaration message has role %q; only system messages carry inline declarations", msg.Role)
	}
	if len(msg.ToolCalls) > 0 || len(msg.ResponsesOutput) > 0 || len(msg.Parts) > 0 || msg.NativeTools != nil || len(msg.ThinkingBlocks) > 0 {
		return nil, fmt.Errorf("tool declaration message carries payload the Anthropic inline shape cannot render")
	}
	blocks := make([]anthropicContent, 0, len(msg.MCPTools)+1)
	if text := strings.TrimSpace(msg.Content); text != "" {
		blocks = append(blocks, anthropicContent{Type: "text", Text: text})
	}
	for _, def := range msg.MCPTools {
		blocks = append(blocks, anthropicContent{
			Type: anthropicInlineToolAdditionType,
			Tool: &anthropicInlineTool{
				Type: "tool_definition",
				Definition: anthropicTool{
					Name:        def.Name,
					Description: def.Description,
					InputSchema: def.InputSchema,
				},
			},
		})
	}
	return blocks, nil
}

// anthropicInlineDeclarationsRenderable reports whether every declaration
// message in the request fits the Anthropic inline shape (text plus tool
// change blocks only). A false result means the per-target projection must
// fold the declarations into the top-level tools array for this request.
func anthropicInlineDeclarationsRenderable(messages []message.Message) bool {
	for i := range messages {
		if len(messages[i].MCPTools) == 0 {
			continue
		}
		if _, err := anthropicInlineDeclarationBlocks(messages[i]); err != nil {
			log.Warnf("folding MCP declarations into top-level tools for Anthropic target: %v", err)
			return false
		}
	}
	return true
}

// Validate the rendered groups, since normalization may remove an assistant
// or merge tool results. Moving historical declarations here would change the
// prefix of preserved thinking, so reject an invalid projection before I/O.
func validateAnthropicInlinePlacement(messages []anthropicMessage) error {
	for i := 0; i < len(messages); i++ {
		if messages[i].Role != string(message.RoleSystem) {
			continue
		}
		if i == 0 || messages[i-1].Role != string(message.RoleUser) {
			return &APIError{StatusCode: 400, Message: "invalid_request_error: Anthropic inline tools require a user turn before the declaration; check compat.anthropic.mcp_inline_tools"}
		}
		for i+1 < len(messages) && messages[i+1].Role == string(message.RoleSystem) {
			i++
		}
		if i+1 < len(messages) && messages[i+1].Role != string(message.RoleAssistant) {
			return &APIError{StatusCode: 400, Message: "invalid_request_error: Anthropic inline tools require an assistant turn or end of request after the declaration; check compat.anthropic.mcp_inline_tools"}
		}
	}
	return nil
}
