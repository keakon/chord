package llm

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

// A Chat Completions endpoint that fronts a thinking model keeps the model's
// provider-bound replay state somewhere the OpenAI schema leaves open, and
// expects that state back when the conversation continues. Two carrier shapes
// are in use:
//
//   - Gemini: tool_calls[].extra_content.google.thought_signature, the shape
//     Google's own compatibility endpoint returns. LiteLLM mirrors the same
//     value under provider_specific_fields.thought_signature, and some
//     gateways also return a top-level tool_calls[].thought_signature.
//   - Claude: message-level thinking_blocks holding the Anthropic blocks
//     (LiteLLM's convention), mirrored under provider_specific_fields.
//
// Chord reads every shape and writes only the documented one: the blob has to
// round-trip verbatim, and a shape the gateway cannot map back turns the
// replay into a 400.

// openAIToolCallExtraContent is the vendor-scoped passthrough object on a tool
// call; Gemini thought signatures ride under google.thought_signature.
type openAIToolCallExtraContent struct {
	Google *openAIGoogleThoughtSignature `json:"google,omitempty"`
}

type openAIGoogleThoughtSignature struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// openAIProviderSpecificFields mirrors provider_specific_fields, the object
// LiteLLM uses for native fields that have no OpenAI schema slot. Chord only
// reads it.
type openAIProviderSpecificFields struct {
	ThoughtSignature string             `json:"thought_signature,omitempty"`
	ThinkingBlocks   []anthropicContent `json:"thinking_blocks,omitempty"`
}

// openAIToolCallThoughtSignature returns the Gemini thought signature carried
// by a tool call, checking every shape gateways use. Later chunks may repeat
// the signature; callers keep the last non-empty value.
func openAIToolCallThoughtSignature(tc openAIToolCall) string {
	if tc.ExtraContent != nil && tc.ExtraContent.Google != nil {
		if sig := strings.TrimSpace(tc.ExtraContent.Google.ThoughtSignature); sig != "" {
			return sig
		}
	}
	if sig := strings.TrimSpace(tc.ThoughtSignature); sig != "" {
		return sig
	}
	if tc.ProviderSpecificFields != nil {
		return strings.TrimSpace(tc.ProviderSpecificFields.ThoughtSignature)
	}
	return ""
}

// openAIThinkingBlocksFromCarriers returns the Anthropic thinking blocks a chat
// message or stream delta carries, preferring the documented message-level
// field over the provider_specific_fields mirror.
func openAIThinkingBlocksFromCarriers(blocks []anthropicContent, psf *openAIProviderSpecificFields) []anthropicContent {
	if len(blocks) > 0 {
		return blocks
	}
	if psf != nil {
		return psf.ThinkingBlocks
	}
	return nil
}

// thinkingBlocksFromCarrier converts the carrier blocks into the durable replay
// shape. Blocks with neither text nor signature are dropped: they cannot be
// replayed and would only add noise to the session record.
func thinkingBlocksFromCarrier(blocks []anthropicContent) []message.ThinkingBlock {
	var out []message.ThinkingBlock
	for _, block := range blocks {
		switch block.Type {
		case "redacted_thinking":
			data := cloneLongLivedLLMString(block.Data)
			if strings.TrimSpace(data) == "" {
				continue
			}
			out = append(out, message.ThinkingBlock{Data: data})
		case "thinking":
			thinking := cloneLongLivedLLMString(block.Thinking)
			signature := cloneLongLivedLLMString(block.Signature)
			if strings.TrimSpace(thinking) == "" && strings.TrimSpace(signature) == "" {
				continue
			}
			out = append(out, message.ThinkingBlock{Thinking: thinking, Signature: signature})
		}
	}
	return out
}

// carrierBlocksFromThinkingBlocks is the inverse: whatever the normalization
// kept for this target is serialized verbatim back into the carrier shape.
func carrierBlocksFromThinkingBlocks(blocks []message.ThinkingBlock) []anthropicContent {
	var out []anthropicContent
	for _, block := range blocks {
		if !block.Replayable() {
			continue
		}
		if block.Data != "" {
			out = append(out, anthropicContent{Type: "redacted_thinking", Data: block.Data})
			continue
		}
		out = append(out, anthropicContent{
			Type:      "thinking",
			Thinking:  block.Thinking,
			Signature: block.Signature,
		})
	}
	return out
}

// applyChatGeminiThoughtSignature writes the Gemini signature carrier onto the
// first tool call of the step. A step without tool calls has no carrier on this
// wire: the chat schema has no slot for a thought-part signature.
func applyChatGeminiThoughtSignature(msg *openAIMessage, signature string) bool {
	signature = strings.TrimSpace(signature)
	if signature == "" || len(msg.ToolCalls) == 0 {
		return false
	}
	first := &msg.ToolCalls[0]
	if openAIToolCallThoughtSignature(*first) != "" {
		return false
	}
	first.ExtraContent = &openAIToolCallExtraContent{
		Google: &openAIGoogleThoughtSignature{ThoughtSignature: signature},
	}
	return true
}

// chatGeminiRequiresSignaturePlaceholder reports whether this Chat Completions
// request has to fill missing active-loop signatures. The endpoint must read
// the Gemini native thinking fields (the resolved dialect), and the upstream
// must be a Gemini 3 either by name or through the explicit gemini-3 selector.
// A family-only gemini pin is not enough.
func chatGeminiRequiresSignaturePlaceholder(model string, dialect nativeThinkingDialect) bool {
	if dialect != nativeThinkingGemini && dialect != nativeThinkingGemini3 {
		return false
	}
	return dialect == nativeThinkingGemini3 || isGemini3Model(model)
}

// ensureChatGeminiActiveLoopSignatures mirrors
// ensureGeminiActiveLoopSignatures for the Chat Completions wire: Gemini 3
// rejects function-call history whose thought signature is missing, so the
// active loop's assistant steps get the documented placeholder. The native
// wire applies the same rule through the parts it serializes. The caller
// decides whether this target needs the placeholder; an aliased Gemini 3 only
// reveals itself through an explicitly pinned gemini dialect, which the model
// name cannot express.
func ensureChatGeminiActiveLoopSignatures(messages []openAIMessage) {
	activeStart := 0
	for i := range messages {
		if messages[i].Role != "user" || messages[i].Transient {
			continue
		}
		activeStart = i
	}
	for i := activeStart; i < len(messages); i++ {
		msg := &messages[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		if openAIToolCallThoughtSignature(msg.ToolCalls[0]) != "" {
			continue
		}
		msg.ToolCalls[0].ExtraContent = &openAIToolCallExtraContent{
			Google: &openAIGoogleThoughtSignature{ThoughtSignature: geminiSkipThoughtSignatureValidator},
		}
	}
}
