package tui

import "strings"

func startupDeferredMetaSearchInnerOffset(meta startupDeferredBlockMeta, query string, width int) int {
	if query == "" || strings.TrimSpace(meta.SearchableText) == "" {
		return 0
	}
	if width <= 0 {
		width = 80
	}
	lowerQuery := strings.ToLower(query)
	for i, line := range wrapText(meta.SearchableText, width) {
		if strings.Contains(line, lowerQuery) {
			return i
		}
	}
	return 0
}

type startupDeferredBlockMeta struct {
	BlockID        int
	Type           BlockType
	Summary        string
	SearchableText string
	LineCounts     map[int]int
}

func cloneLineCounts(src map[int]int) map[int]int {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[int]int, len(src))
	for width, count := range src {
		dst[width] = count
	}
	return dst
}

func buildStartupDeferredBlockMeta(blocks []*Block, width int) []startupDeferredBlockMeta {
	if len(blocks) == 0 {
		return nil
	}
	if width <= 0 {
		width = 80
	}
	meta := make([]startupDeferredBlockMeta, 0, len(blocks))
	for _, block := range blocks {
		if block == nil {
			continue
		}
		lineCounts := cloneLineCounts(block.spillLineCounts)
		if block.lineCountCache > 0 && block.lineCacheWidth > 0 {
			if lineCounts == nil {
				lineCounts = make(map[int]int, 1)
			}
			if _, ok := lineCounts[block.lineCacheWidth]; !ok {
				lineCounts[block.lineCacheWidth] = block.lineCountCache
			}
		}
		meta = append(meta, startupDeferredBlockMeta{
			BlockID:        block.ID,
			Type:           block.Type,
			Summary:        block.Summary(),
			SearchableText: block.searchableTextLower(),
			LineCounts:     lineCounts,
		})
	}
	return meta
}

func startupDeferredBlockLineCount(meta startupDeferredBlockMeta, width int) int {
	if width <= 0 {
		width = 80
	}
	if meta.LineCounts != nil {
		if count, ok := meta.LineCounts[width]; ok && count > 0 {
			return count
		}
		if count, ok := meta.LineCounts[80]; ok && count > 0 {
			return count
		}
		for _, count := range meta.LineCounts {
			if count > 0 {
				return count
			}
		}
	}
	return 1
}

func startupDeferredMetaSearchVisible(meta startupDeferredBlockMeta) bool {
	if strings.TrimSpace(meta.SearchableText) == "" {
		return false
	}
	if searchDiagnosticArtifactExcluded(meta.Type, meta.SearchableText) {
		return false
	}
	if meta.Type == BlockThinking {
		return strings.TrimSpace(preprocessThinkingMarkdown(meta.SearchableText)) != ""
	}
	return true
}

func findMatchesInStartupDeferredBlockMeta(meta []startupDeferredBlockMeta, query string, width int) []MatchPosition {
	if query == "" || len(meta) == 0 {
		return nil
	}
	if width <= 0 {
		width = 80
	}
	lowerQuery := strings.ToLower(query)
	matches := make([]MatchPosition, 0)
	lineOffset := 0
	for i, blockMeta := range meta {
		if strings.Contains(blockMeta.SearchableText, lowerQuery) && startupDeferredMetaSearchVisible(blockMeta) {
			matches = append(matches, MatchPosition{
				BlockIndex:  i,
				BlockID:     blockMeta.BlockID,
				LineOffset:  lineOffset,
				InnerOffset: startupDeferredMetaSearchInnerOffset(blockMeta, query, width),
				Query:       query,
			})
		}
		lineOffset += startupDeferredBlockLineCount(blockMeta, width)
	}
	return matches
}
