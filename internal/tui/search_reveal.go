package tui

import (
	"strings"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func revealSearchMatchedBlock(block *Block) bool {
	if block == nil {
		return false
	}
	changed := false
	switch block.Type {
	case BlockToolCall:
		switch block.ToolName {
		case "Write", "ApplyPatch", "Read", "Delete":
			if block.Collapsed {
				block.Collapsed = false
				changed = true
			}
			if block.ToolName == tools.NameRead && !block.ReadContentExpanded {
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
					// Freeze elapsed-time rendering (e.g. ⏱ footer) so late search-driven
					// stabilization of partially-recorded tool cards does not cause their
					// line count to grow over time and drift the viewport.
					if block.SettledAt.IsZero() {
						block.SettledAt = time.Now()
					}
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
