package agent

import (
	"slices"

	"github.com/keakon/chord/internal/message"
)

func latestTaskEvidenceIndex(taskID string, messages []message.Message) int {
	for i := range slices.Backward(messages) {
		if carry := messages[i].CompactionTaskEvidence; messages[i].IsCompactionSummary && carry != nil && carry.TaskID == taskID {
			return i
		}
	}
	return -1
}

func collectTaskFileEvidence(taskID, root string, messages []message.Message) *message.TaskEvidence {
	evidence := &message.TaskEvidence{TaskID: taskID}
	start := latestTaskEvidenceIndex(taskID, messages)
	if start >= 0 {
		evidence = messages[start].CompactionTaskEvidence.Clone()
	}
	for _, msg := range messages[max(0, start):] {
		if msg.Role != message.RoleTool {
			continue
		}
		if len(msg.ToolChangedPaths) > 0 {
			evidence.ChangedPaths = append(evidence.ChangedPaths, msg.ToolChangedPaths...)
		} else if msg.FileState != nil {
			for _, state := range msg.FileState.Writes {
				evidence.ChangedPaths = append(evidence.ChangedPaths, displayPathFromWorkDir(root, state.Path))
			}
			for _, state := range msg.FileState.Deletes {
				evidence.ChangedPaths = append(evidence.ChangedPaths, displayPathFromWorkDir(root, state.Path))
			}
		}
		evidence.FileAttributionIncomplete = evidence.FileAttributionIncomplete || msg.FileAttributionIncomplete
	}
	evidence.ChangedPaths = normalizeStringList(evidence.ChangedPaths)
	if len(evidence.ChangedPaths) == 0 && !evidence.FileAttributionIncomplete {
		return nil
	}
	return evidence
}

func buildTaskEvidenceCarry(taskID, root string, messages []message.Message) *message.TaskEvidence {
	evidence := collectTaskFileEvidence(taskID, root, messages)
	if evidence != nil && len(evidence.ChangedPaths) > message.MaxTaskEvidencePaths {
		evidence.ChangedPaths = evidence.ChangedPaths[:message.MaxTaskEvidencePaths]
		evidence.FileAttributionIncomplete = true
	}
	return evidence
}
