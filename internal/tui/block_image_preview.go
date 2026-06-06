package tui

func (b *Block) appendImagePreviewLines(finalLines *[]string, contentWidth int, cardBg string, paddingTop int, needsLeadingBlank bool) bool {
	if b == nil || len(b.ImageParts) == 0 || finalLines == nil {
		return false
	}
	if needsLeadingBlank {
		*finalLines = append(*finalLines, "")
	}
	imagesRendered := false
	for i := range b.ImageParts {
		startLine := len(*finalLines)
		imageLines, renderCols, renderRows, err := renderImageBlock(b.ImageParts[i], contentWidth, cardBg, currentImageCapabilities())
		if err != nil {
			b.ImageParts[i].RenderStartLine = -1
			b.ImageParts[i].RenderEndLine = -1
			b.ImageParts[i].RenderCols = 0
			b.ImageParts[i].RenderRows = 0
			continue
		}
		*finalLines = append(*finalLines, imageLines...)
		b.ImageParts[i].RenderStartLine = startLine + paddingTop
		b.ImageParts[i].RenderEndLine = len(*finalLines) - 1 + paddingTop
		b.ImageParts[i].RenderCols = renderCols
		b.ImageParts[i].RenderRows = renderRows
		imagesRendered = true
		if i < len(b.ImageParts)-1 {
			*finalLines = append(*finalLines, "")
		}
	}
	return imagesRendered
}

func blockSupportsImagePreview(block *Block) bool {
	return block != nil && (block.Type == BlockUser || block.Type == BlockToolResult) && len(block.ImageParts) > 0
}
