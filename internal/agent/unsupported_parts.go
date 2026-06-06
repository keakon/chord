package agent

import "github.com/keakon/chord/internal/message"

type inputCapability interface {
	SupportsInput(modality string) bool
}

type toolResultCapability interface {
	SupportsToolResultModalities(modalities []string) bool
}

type unsupportedPartCounts struct {
	Images int
	PDFs   int
}

func filterUnsupportedBinaryPartsForModel(messages []message.Message, capability inputCapability) ([]message.Message, unsupportedPartCounts) {
	if capability == nil {
		return messages, unsupportedPartCounts{}
	}
	supportsImage := capability.SupportsInput("image")
	supportsPDF := capability.SupportsInput("pdf")
	if supportsImage && supportsPDF {
		return messages, unsupportedPartCounts{}
	}

	out := make([]message.Message, 0, len(messages))
	var counts unsupportedPartCounts
	for _, msg := range messages {
		if len(msg.Parts) == 0 {
			out = append(out, msg)
			continue
		}
		filtered := make([]message.ContentPart, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			switch part.Type {
			case "image":
				if !supportsImage {
					counts.Images++
					continue
				}
			case "pdf":
				if !supportsPDF {
					counts.PDFs++
					continue
				}
			}
			filtered = append(filtered, part)
		}
		if len(filtered) == 0 && msg.Content == "" {
			continue
		}
		msg.Parts = filtered
		out = append(out, msg)
	}
	return out, counts
}

func (c unsupportedPartCounts) any() bool {
	return c.Images > 0 || c.PDFs > 0
}

func (c unsupportedPartCounts) summary() string {
	switch {
	case c.Images > 0 && c.PDFs > 0:
		return "image/PDF"
	case c.Images > 0:
		return "image"
	case c.PDFs > 0:
		return "PDF"
	default:
		return ""
	}
}
