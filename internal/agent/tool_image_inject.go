package agent

import "github.com/keakon/chord/internal/message"

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
			if !canReplayToolResultModality(capability, "image") {
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
