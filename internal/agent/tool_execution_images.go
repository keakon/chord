package agent

import (
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// Archive on the tool goroutine, before the event loop commits the result, so
// live history and JSONL share a recall address without blocking the event loop.
func (p toolExecutionPipeline) normalizeToolResultImages(parts []message.ContentPart) []message.ContentPart {
	if len(parts) == 0 || p.sessionDir == "" {
		return parts
	}
	normalized, err := recovery.NewRecoveryManager(p.sessionDir).NormalizeAttachmentParts(parts)
	if err != nil {
		log.Warnf("keeping inline tool attachment data agent=%v error=%v", p.agentID, err)
		return parts
	}
	return normalized
}
