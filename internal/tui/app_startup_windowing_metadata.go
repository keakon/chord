package tui

import (
	"maps"
	"strings"
)

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

// searchableInspection resolves the block's lowercase search text together
// with the block that should be used for visibility validation. A spilled
// archive block is loaded into a temporary inspection copy; the caller must
// invalidate it when temporary is true.
func (meta startupDeferredBlockMeta) searchableInspection() (string, *Block, bool) {
	if meta.block == nil {
		return "", nil, false
	}
	if !meta.block.spillCold {
		return meta.block.searchableTextLower(), meta.block, false
	}
	inspect, temporary := meta.block.inspectionBlock()
	if inspect == nil {
		return "", nil, false
	}
	return inspect.searchableTextLower(), inspect, temporary
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

func startupDeferredMetaSearchVisible(meta startupDeferredBlockMeta, searchable string) bool {
	if strings.TrimSpace(searchable) == "" {
		return false
	}
	if searchDiagnosticArtifactExcluded(meta.Type, searchable) {
		return false
	}
	if meta.Type == BlockThinking {
		if thinkingContentIsPlaceholder(searchable) {
			return false
		}
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
		searchable, inspect, temporary := blockMeta.searchableInspection()
		if inspect == nil {
			lineOffset += startupDeferredBlockLineCount(blockMeta, width)
			continue
		}
		candidate := strings.Contains(searchable, lowerQuery)
		if !candidate && blockMeta.Type == BlockAssistant {
			candidate = assistantMarkdownMayContainQuery(searchable, lowerQuery)
		}
		if candidate && startupDeferredMetaSearchVisible(blockMeta, searchable) {
			if innerOffset, ok := visibleSearchMatchInnerOffset(inspect, query, width); ok {
				matches = append(matches, MatchPosition{
					BlockIndex:  i,
					BlockID:     blockMeta.BlockID,
					LineOffset:  lineOffset,
					InnerOffset: innerOffset,
					Query:       query,
				})
			}
		}
		if temporary {
			inspect.InvalidateCache()
		}
		lineOffset += startupDeferredBlockLineCount(blockMeta, width)
	}
	return matches
}
