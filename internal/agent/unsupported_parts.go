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
	filtered, counts := message.FilterUnsupportedBinaryParts(messages, capability.SupportsInput("image"), capability.SupportsInput("pdf"))
	return filtered, unsupportedPartCounts{Images: counts.Images, PDFs: counts.PDFs}
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
