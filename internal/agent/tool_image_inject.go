package agent

import (
	"slices"

	"github.com/keakon/chord/internal/message"
)

// projectGeneratedImagePreviews keeps every original in durable history for
// TUI viewing, while sending at most one generated preview per tool result.
// Unchanged messages share their parts; only changed request parts are copied.
func projectGeneratedImagePreviews(messages []message.Message) []message.Message {
	var projected []message.Message
	for i, msg := range messages {
		if msg.Role != message.RoleTool {
			continue
		}
		seen := false
		var parts []message.ContentPart
		for j, part := range msg.Parts {
			if part.Type == message.ContentPartImage && part.ArtifactID != "" {
				if seen {
					if parts == nil {
						parts = slices.Clone(msg.Parts[:j])
					}
					continue
				}
				seen = true
			}
			if parts != nil {
				parts = append(parts, part)
			}
		}
		if parts != nil {
			if projected == nil {
				projected = slices.Clone(messages)
			}
			projected[i].Parts = parts
		}
	}
	if projected == nil {
		return messages
	}
	return projected
}

func toolResultParts(text string, images []message.ContentPart) []message.ContentPart {
	if len(images) == 0 {
		return nil
	}
	parts := make([]message.ContentPart, 0, len(images)+1)
	parts = append(parts, message.ContentPart{Type: "text", Text: text})
	parts = append(parts, images...)
	return parts
}

func toolResultPartsForCapability(text string, images []message.ContentPart, capability inputCapability) ([]message.ContentPart, unsupportedPartCounts) {
	if len(images) == 0 || capability == nil {
		return toolResultParts(text, images), unsupportedPartCounts{}
	}
	filtered := make([]message.ContentPart, 0, len(images))
	var dropped unsupportedPartCounts
	for _, part := range images {
		switch part.Type {
		case "image":
			if part.ArtifactID == "" && !canReplayToolResultModality(capability, "image") {
				dropped.Images++
				continue
			}
		case "pdf":
			if !canReplayToolResultModality(capability, "pdf") {
				dropped.PDFs++
				continue
			}
		}
		filtered = append(filtered, part)
	}
	return toolResultParts(text, filtered), dropped
}

func canReplayToolResultModality(capability inputCapability, modality string) bool {
	if toolResultCap, ok := capability.(toolResultCapability); ok {
		return toolResultCap.SupportsToolResultModalities([]string{modality})
	}
	return capability.SupportsInput(modality)
}
