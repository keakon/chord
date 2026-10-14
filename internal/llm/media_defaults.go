package llm

// defaultPDFMediaType returns the PDF media type, falling back to the canonical
// "application/pdf" when a content part carries no explicit MIME type.
func defaultPDFMediaType(mimeType string) string {
	if mimeType == "" {
		return "application/pdf"
	}
	return mimeType
}

// defaultPDFFilename returns a usable filename for providers (OpenAI Chat/
// Responses) that require one on file blocks, defaulting when none is set.
func defaultPDFFilename(name string) string {
	if name == "" {
		return "document.pdf"
	}
	return name
}
