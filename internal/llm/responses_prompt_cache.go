package llm

import "github.com/keakon/chord/internal/message"

// responsesCacheControl is shared by the request mode and explicit content
// markers. Implicit mode keeps historical eligible message endings available
// for lookup alongside our durable explicit boundaries.
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

// responsesPromptCacheOptions avoids writing a one-request suffix while keeping
// implicit lookup available for append-only or wholly opaque input. Existing
// durable explicit markers survive in every full-input continuation.
func responsesPromptCacheOptions(messages []message.Message, input []responsesInputItem) *responsesCacheControl {
	mode := "implicit"
	if promptCacheDurableMessageCount(messages) < len(messages) {
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
	}
	return &responsesCacheControl{Mode: mode}
}
