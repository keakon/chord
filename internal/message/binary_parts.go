package message

// BinaryPartCounts records how many image/PDF content parts were dropped while
// filtering messages for a target that cannot accept them.
type BinaryPartCounts struct {
	Images int
	PDFs   int
}

// Any reports whether any binary part was dropped.
func (c BinaryPartCounts) Any() bool {
	return c.Images > 0 || c.PDFs > 0
}

// FilterUnsupportedBinaryParts removes image/PDF content parts that the target
// cannot accept, returning the filtered messages plus a count of what was
// dropped. A message left with no parts and no text content is dropped entirely
// so wire encoders never receive an empty message. When both modalities are
// supported, or nothing is dropped, the original slice is returned unchanged so
// the common no-op path stays allocation-free.
func FilterUnsupportedBinaryParts(messages []Message, supportsImage, supportsPDF bool) ([]Message, BinaryPartCounts) {
	if supportsImage && supportsPDF {
		return messages, BinaryPartCounts{}
	}

	out := make([]Message, 0, len(messages))
	var counts BinaryPartCounts
	for _, msg := range messages {
		if len(msg.Parts) == 0 {
			out = append(out, msg)
			continue
		}
		filtered := make([]ContentPart, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			switch part.Type {
			case ContentPartImage:
				if !supportsImage {
					counts.Images++
					continue
				}
			case ContentPartPDF:
				if !supportsPDF {
					counts.PDFs++
					continue
				}
			}
			filtered = append(filtered, part)
		}
		if len(filtered) == len(msg.Parts) {
			out = append(out, msg)
			continue
		}
		if len(filtered) == 0 && msg.Content == "" {
			continue
		}
		msg.Parts = filtered
		out = append(out, msg)
	}
	if !counts.Any() {
		return messages, counts
	}
	return out, counts
}
