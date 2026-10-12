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

// unsupportedToolResultParts reports the binary parts of a tool result the
// capability cannot replay to the model. The counts drive the user-facing
// toast only — the parts themselves are never dropped here: they stay in the
// durable transcript and the TUI event, and the request wire strips modalities
// the resolved target cannot accept (filterUnsupportedBinaryPartsForTarget).
// Dropping them at commit time would lose them for good, including after a
// later switch to a model that can see them.
func unsupportedToolResultParts(images []message.ContentPart, capability inputCapability) unsupportedPartCounts {
	if len(images) == 0 || capability == nil {
		return unsupportedPartCounts{}
	}
	var dropped unsupportedPartCounts
	for _, part := range images {
		switch part.Type {
		case "image":
			// Artifact-backed parts are re-resolved per request from the
			// stored original, so they never depend on the current model.
			if part.ArtifactID == "" && !canReplayToolResultModality(capability, "image") {
				dropped.Images++
			}
		case "pdf":
			if !canReplayToolResultModality(capability, "pdf") {
				dropped.PDFs++
			}
		}
	}
	return dropped
}

func canReplayToolResultModality(capability inputCapability, modality string) bool {
	if toolResultCap, ok := capability.(toolResultCapability); ok {
		return toolResultCap.SupportsToolResultModalities([]string{modality})
	}
	return capability.SupportsInput(modality)
}
