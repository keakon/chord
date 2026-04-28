package tui

import "strings"

func revealSearchMatchedBlock(block *Block) bool {
	if block == nil {
		return false
	}
	changed := false
	switch block.Type {
	case BlockToolCall:
		switch block.ToolName {
		case "Write", "Edit", "Read", "Delete":
			if block.Collapsed {
				block.Collapsed = false
				changed = true
			}
			if block.ToolName == "Read" && !block.ReadContentExpanded {
				block.ReadContentExpanded = true
				changed = true
			}
		case "Delegate":
			if block.Collapsed {
				block.Collapsed = false
				changed = true
			}
		default:
			if toolUsesCompactDetailToggle(block.ToolName) {
				if !block.ToolCallDetailExpanded {
					block.ToolCallDetailExpanded = true
					changed = true
				}
				if block.Collapsed {
					block.Collapsed = false
					changed = true
				}
				if strings.TrimSpace(block.ResultContent) != "" && !block.ResultDone {
					block.ResultDone = true
					changed = true
				}
			} else if (block.ResultContent != "" || block.DoneSummary != "") && block.Collapsed {
				block.Collapsed = false
				changed = true
			}
		}
	case BlockToolResult:
		if block.Collapsed {
			block.Collapsed = false
			changed = true
		}
	case BlockCompactionSummary:
		if block.Collapsed {
			block.Collapsed = false
			changed = true
		}
	}
	if changed {
		block.InvalidateCache()
	}
	return changed
}
