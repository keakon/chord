package llm

import (
	"fmt"

	"github.com/keakon/chord/internal/message"
)

func appendOpenAIToolMedia(blocks []openAIContentBlock, msg message.Message) []openAIContentBlock {
	var parts []message.ContentPart
	for _, part := range msg.Parts {
		if part.Type == message.ContentPartImage || part.Type == message.ContentPartPDF {
			parts = append(parts, part)
		}
	}
	media := openAIContentParts(parts)
	if len(media) == 0 {
		return blocks
	}
	blocks = append(blocks, openAIContentBlock{Type: "text", Text: fmt.Sprintf("Attachments from tool result %s:", msg.ToolCallID)})
	return append(blocks, media...)
}

// openAIContentParts encodes user attachments and request-only tool media
// using the same normalization and file resolver.
func openAIContentParts(parts []message.ContentPart) []openAIContentBlock {
	var blocks []openAIContentBlock
	for _, p := range parts {
		switch p.Type {
		case "image":
			data, mime, ok := binaryPartForWire(p)
			if !ok {
				continue
			}
			blocks = append(blocks, openAIContentBlock{
				Type: "image_url",
				ImageURL: &openAIImageURL{
					URL: binaryPartDataURL(mime, data),
				},
			})
		case "pdf":
			data, mime, ok := binaryPartForWire(p)
			if !ok {
				continue
			}
			blocks = append(blocks, openAIContentBlock{
				Type: "file",
				File: &openAIFile{
					Filename: defaultPDFFilename(p.FileName),
					FileData: binaryPartDataURL(defaultPDFMediaType(mime), data),
				},
			})
		default: // "text"
			// Fold adjacent pure-text parts into a single text block
			// so a text-only message takes one block. Insert a
			// newline only when neither side provides one, keeping
			// separately-authored segments from being glued
			// together. image/file blocks are never folded.
			if p.Text == "" {
				continue
			}
			if last := len(blocks) - 1; last >= 0 && blocks[last].Type == "text" {
				blocks[last].Text = joinAdjacentPartText(blocks[last].Text, p.Text)
			} else {
				blocks = append(blocks, openAIContentBlock{Type: "text", Text: p.Text})
			}
		}
	}
	return blocks
}
