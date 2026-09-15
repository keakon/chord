package agent

import (
	"slices"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func modelDrivenHasProgressSinceCheckpoint(snapshot []message.Message) bool {
	boundary := -1
	for index := range slices.Backward(snapshot) {
		if snapshot[index].IsCompactionSummary {
			if snapshot[index].CompactionSummaryMode != compactionSummaryModeModelDriven {
				return true
			}
			boundary = index
			break
		}
	}
	if boundary < 0 {
		return true
	}
	ignoredCalls := make(map[string]struct{})
	applyBatch := snapshot[boundary].RequestBatch
	for _, entry := range snapshot[boundary+1:] {
		switch entry.Kind {
		case message.KindContextNotice, message.KindTurnOverlay, message.KindLoopNotice, message.KindStreamContinue, message.KindReplayContinuation:
			continue
		}
		switch entry.Role {
		case message.RoleAssistant:
			if applyBatch > 0 && entry.RequestBatch > 0 && entry.RequestBatch <= applyBatch {
				for _, call := range entry.ToolCalls {
					ignoredCalls[call.ID] = struct{}{}
				}
				continue
			}
			if len(entry.ToolCalls) == 0 {
				if entry.Content != "" || len(entry.Parts) > 0 {
					return true
				}
				continue
			}
			for _, call := range entry.ToolCalls {
				if tools.NormalizeName(call.Name) != tools.NameCompactContext {
					return true
				}
				ignoredCalls[call.ID] = struct{}{}
			}
		case message.RoleTool:
			if _, ignored := ignoredCalls[entry.ToolCallID]; !ignored {
				return true
			}
		default:
			return true
		}
	}
	return false
}
