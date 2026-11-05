package agent

import "github.com/keakon/chord/internal/message"

// Normal completion and interrupted closeout must persist the same evidence,
// even though their continuation and delivery lifecycles differ.
func (s *SubAgent) buildToolResultMessage(result *toolResult, contextResult string, parts []message.ContentPart, isError bool, changedPaths []string, attributionIncomplete bool) message.Message {
	return message.Message{
		Role:                      message.RoleTool,
		DeliveredFiles:            successfulDeliveredFiles(result.DeliveredFiles, isError),
		ToolCallID:                result.CallID,
		Content:                   contextResult,
		ToolPayload:               result.Payload,
		ToolNotes:                 append([]string(nil), result.Notes...),
		Parts:                     parts,
		ToolDiff:                  result.Diff,
		ToolDiffAdded:             result.DiffAdded,
		ToolDiffRemoved:           result.DiffRemoved,
		ToolDurationMs:            result.Duration.Milliseconds(),
		ToolStatus:                string(toolResultStatus(isError)),
		FileState:                 result.FileState.Clone(),
		ToolChangedPaths:          append([]string(nil), changedPaths...),
		FileAttributionIncomplete: attributionIncomplete,
		LSPReviews:                append([]message.LSPReview(nil), result.LSPReviews...),
		Audit:                     result.Audit.Clone(),
		Provenance:                toolProvenanceFromContext(s.ctxMgr, result.CallID),
		ToolRecoveryState:         result.RecoveryState,
		SkillActivation:           skillActivationForResult(s.ctxMgr.Snapshot, s.skillActivationView, s.sessionEpoch, s.workDirState.load().Generation, result.Name, result.CallID, result.Error, result.SkillActivation),
	}
}
