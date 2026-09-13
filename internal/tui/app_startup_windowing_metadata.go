package tui

import (
	"maps"
	"strings"
)

func startupDeferredMetaSearchInnerOffset(meta startupDeferredBlockMeta, query string, width int) int {
	searchable := meta.searchableText()
	if query == "" || strings.TrimSpace(searchable) == "" {
		return 0
	}
	if width <= 0 {
		width = 80
	}
	lowerQuery := strings.ToLower(query)
	if offset, ok := wrappedSearchMatchLineOffset(searchable, lowerQuery, width); ok {
		return offset
	}
	return 0
}

// startupDeferredBlockMeta is the archive-side descriptor of one deferred
// block. Summary and searchable text stay on the block and resolve on demand
// (search is the only consumer and runs on explicit user action), so long
// sessions no longer keep a lowercase copy of every block resident outside the
// spill budget.
type startupDeferredBlockMeta struct {
	BlockID    int
	Type       BlockType
	block      *Block
	LineCounts map[int]int
}

// summary resolves the block's one-line description; an archive entry without
// a live block (should not happen) yields an empty summary.
func (meta startupDeferredBlockMeta) summary() string {
	if meta.block == nil {
		return ""
	}
	return meta.block.Summary()
}

// searchableText resolves the block's cached lowercase search text.
func (meta startupDeferredBlockMeta) searchableText() string {
	if meta.block == nil {
		return ""
	}
	return meta.block.searchableTextLower()
}

func cloneLineCounts(src map[int]int) map[int]int {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[int]int, len(src))
	maps.Copy(dst, src)
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
			BlockID:    block.ID,
			Type:       block.Type,
			block:      block,
			LineCounts: lineCounts,
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
	searchable := meta.searchableText()
	if strings.TrimSpace(searchable) == "" {
		return false
	}
	if searchDiagnosticArtifactExcluded(meta.Type, searchable) {
		return false
	}
	if meta.Type == BlockThinking {
		return strings.TrimSpace(preprocessThinkingMarkdown(searchable)) != ""
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
		candidate := strings.Contains(blockMeta.searchableText(), lowerQuery)
		if !candidate && blockMeta.Type == BlockAssistant {
			candidate = assistantMarkdownMayContainQuery(blockMeta.searchableText(), lowerQuery)
		}
		if candidate && startupDeferredMetaSearchVisible(blockMeta) {
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
