package llm

import (
	"slices"

	"github.com/keakon/chord/internal/message"
)

// responsesCacheTextPartEnd selects the source endpoint of the last input_text
// block. Zero denotes a string/empty fallback; -1 means there is no text block.
// Attachments after text are outside the endpoint. Check wire validity only for
// attachment-only messages, where invalid parts can produce a text fallback.
func responsesCacheTextPartEnd(msg message.Message) int {
	if len(msg.Parts) == 0 {
		return 0
	}
	for i, part := range slices.Backward(msg.Parts) {
		if !part.IsBinary() && part.Text != "" {
			return i + 1
		}
	}
	for _, part := range msg.Parts {
		if part.IsBinary() {
			if _, _, ok := binaryPartForWire(part); ok {
				return -1
			}
		}
	}
	return 0
}

// responsesCacheControl is the explicit-mode policy. Chord omits the top-level
// field for implicit mode because implicit is the API default.
type responsesCacheControl struct {
	Mode string `json:"mode"`
}

// markResponsesCacheText returns owned blocks so request-only cache metadata
// cannot mutate replayed history. Only input_text accepts an explicit marker;
// opaque reasoning, additional_tools and assistant output_text are untouched.
func markResponsesCacheText(content any) any {
	var blocks []responsesContentBlock
	switch value := content.(type) {
	case string:
		blocks = []responsesContentBlock{{Type: "input_text", Text: value}}
	case []responsesContentBlock:
		blocks = append([]responsesContentBlock(nil), value...)
	default:
		return content
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Type == "input_text" {
			blocks[i].PromptCacheBreakpoint = &responsesCacheControl{Mode: "explicit"}
			break
		}
	}
	return blocks
}

// responsesPromptCacheOptions returns the explicit-mode policy when a transient
// suffix follows durable explicit markers, avoiding a cache write for
// one-request reminders. It returns nil for implicit mode: the API default
// already creates one implicit breakpoint and writes the latest explicit
// markers, so sending the field would only add a compatibility surface.
func responsesPromptCacheOptions(messages []message.Message, input []responsesInputItem) *responsesCacheControl {
	if promptCacheDurableMessageCount(messages) >= len(messages) {
		return nil
	}
	for _, item := range input {
		for _, content := range []any{item.Content, item.Output} {
			if blocks, ok := content.([]responsesContentBlock); ok {
				for _, block := range blocks {
					if block.PromptCacheBreakpoint != nil {
						return &responsesCacheControl{Mode: "explicit"}
					}
				}
			}
		}
	}
	return nil
}
