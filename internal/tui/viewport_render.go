package tui

import (
	"strings"
)

func (v *Viewport) Render(spinnerFrame string, sel *SelectionRange, searchBlockIndex int, searchInnerOffset int, searchQuery string) string {
	if v.width <= 0 || v.height <= 0 {
		return ""
	}
	if v.lastBlockDirty {
		v.UpdateLastBlock()
	}

	lineBg := ViewportLineStyle.Width(v.width)
	emptyLine := lineBg.Render(padLineToDisplayWidth("", v.width))

	blocks := v.visibleBlocks()
	if len(blocks) == 0 {
		lines := make([]string, v.height)
		for i := range lines {
			lines[i] = emptyLine
		}
		return strings.Join(lines, "\n")
	}

	visible := make([]string, 0, v.height)
	windowStart := v.offset
	windowEnd := v.offset + v.height
	currentLine := 0

	for blockIndex, block := range blocks {
		if len(visible) >= v.height {
			break
		}
		leadingSpacing := v.blockLeadingSpacing(blocks, blockIndex)
		if leadingSpacing > 0 {
			spacingStart := currentLine
			spacingEnd := currentLine + leadingSpacing
			if spacingEnd > windowStart && spacingStart < windowEnd {
				lo := 0
				if windowStart > spacingStart {
					lo = windowStart - spacingStart
				}
				hi := leadingSpacing
				if windowEnd < spacingEnd {
					hi = windowEnd - spacingStart
				}
				for i := lo; i < hi && len(visible) < v.height; i++ {
					visible = append(visible, emptyLine)
				}
			}
			currentLine = spacingEnd
			if len(visible) >= v.height || currentLine >= windowEnd {
				break
			}
		}
		if block.spillCold {
			blockCount := 0
			if v.blockSpansCache != nil && blockIndex < len(v.blockSpansCache) {
				blockCount = v.blockSpansCache[blockIndex] - leadingSpacing
			}
			if blockCount <= 0 {
				blockCount = v.lineCount(block, v.width)
			}
			if blockCount < 0 {
				blockCount = 0
			}
			blockStart := currentLine
			blockEnd := currentLine + blockCount
			if blockEnd <= windowStart {
				currentLine = blockEnd
				continue
			}
			if blockStart >= windowEnd {
				break
			}
		}
		block = v.materialize(block)
		sFrame := ""
		if block.Type == BlockToolCall && block.toolExecutionIsRunning() {
			sFrame = spinnerFrame
		}
		if block.Type == BlockUser && block.UserLocalShellCmd != "" && block.UserLocalShellPending {
			sFrame = spinnerFrame
		}

		fullCached := block.GetViewportCache(v.width, sFrame)
		blockCount := len(fullCached)
		if blockCount == 0 {
			blockCount = v.lineCount(block, v.width)
		}
		blockStart := currentLine
		blockEnd := currentLine + blockCount

		if blockEnd <= windowStart {
			currentLine = blockEnd
			continue
		}
		if blockStart >= windowEnd {
			break
		}

		lo := 0
		if windowStart > blockStart {
			lo = windowStart - blockStart
		}
		hi := blockCount
		if windowEnd < blockEnd {
			hi = windowEnd - blockStart
		}

		var finalLines []string
		if fullCached != nil {
			finalLines = fullCached[lo:hi]
		} else {
			blockLines := block.RenderRange(v.width, sFrame, lo, hi)
			finalLines = make([]string, len(blockLines))
			for i, l := range blockLines {
				line := expandTabsForDisplayANSI(l, preformattedTabWidth)
				line = truncateLineToDisplayWidth(line, v.width)
				line = padLineToDisplayWidth(line, v.width)
				finalLines[i] = lineBg.Render(line)
			}
			if lo == 0 && hi == blockCount {
				block.SetViewportCache(v.width, finalLines)
			}
		}

		searchHighlighted := false
		for i := 0; i < len(finalLines) && len(visible) < v.height; i++ {
			line := finalLines[i]
			lineIndex := lo + i
			if searchBlockIndex == blockIndex && !searchHighlighted && sel == nil && lineIndex >= searchInnerOffset {
				if colStart, colEnd, ok := searchMatchColumnRangeInLine(line, searchQuery); ok {
					line = applySearchMatchToLine(line, colStart, colEnd)
					searchHighlighted = true
				}
			}
			if sel != nil && sel.StartBlockID >= 0 && sel.EndBlockID >= 0 {
				if colStart, colEnd, ok := selectionColRange(block.ID, lineIndex, sel); ok && colStart < colEnd {
					lineWidth := selectionStyledTextWidth(line)
					if colStart > lineWidth {
						colStart = lineWidth
					}
					if colEnd > lineWidth {
						colEnd = lineWidth
					}
					if colStart < colEnd {
						line = applyHighlightToLine(line, colStart, colEnd)
					}
				}
			}
			visible = append(visible, line)
		}
		currentLine = blockEnd
	}

	if v.hotBudgetDirty {
		v.enforceHotBudget()
	}

	for len(visible) < v.height {
		visible = append(visible, emptyLine)
	}
	return strings.Join(visible, "\n")
}
