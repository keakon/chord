package agent

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// maxCheckpointRetainedFailureBatches bounds how many failed tool batches a
// model-driven checkpoint re-attaches to the live transcript, counting back
// from the newest. It matches maxToolErrorEvidenceItems so the live records
// and the checkpoint's evidence pack agree on how many failures still count:
// older failures stay in the archive and, as labeled excerpts, in the pack.
const maxCheckpointRetainedFailureBatches = maxToolErrorEvidenceItems

// checkpointRetainedFailureRecords returns the tool-call batches of the
// current turn — everything after the last user-authored message — that ended
// with an unsuccessful result and are complete inside head.
//
// The archival profile archives the whole head, so the records of a failed
// tool call (for example the rejection of the compact_context request that
// triggered the checkpoint itself) would otherwise disappear from the live
// transcript, leaving only the card's excerpt: the user can no longer read the
// error as a card, and a fork no longer replays the failed call. Keeping the
// records makes the failure part of the session again while the checkpoint
// card and its evidence pack stay the summary of everything older.
//
// Only complete batches qualify: an assistant tool call whose result is
// missing or outside head would replay as an unanswered tool call, which
// providers reject. Unsuccessful results are the failure/cancellation
// vocabulary plus legacy results that read as errors (see
// retainedFailureResult). Records are returned in their original order.
func checkpointRetainedFailureRecords(head []message.Message) []message.Message {
	if len(head) == 0 {
		return nil
	}
	turnStart := 0
	for i := range slices.Backward(head) {
		if message.IsUserAuthored(head[i]) {
			turnStart = i + 1
			break
		}
	}
	var batches [][]message.Message
	for i := turnStart; i < len(head); {
		assistant := head[i]
		if assistant.Role != message.RoleAssistant || len(assistant.ToolCalls) == 0 {
			i++
			continue
		}
		end := i + 1
		for end < len(head) && head[end].Role == message.RoleTool {
			end++
		}
		results := head[i+1 : end]
		if !toolCallBatchComplete(assistant, results) || !batchHasRetainedFailure(results) {
			i = end
			continue
		}
		batches = append(batches, head[i:end])
		i = end
	}
	if len(batches) > maxCheckpointRetainedFailureBatches {
		batches = batches[len(batches)-maxCheckpointRetainedFailureBatches:]
	}
	var out []message.Message
	for _, batch := range batches {
		out = append(out, batch...)
	}
	return out
}

// batchHasRetainedFailure reports whether any result of the batch ended
// unsuccessfully.
func batchHasRetainedFailure(results []message.Message) bool {
	for _, result := range results {
		if retainedFailureResult(result) {
			return true
		}
	}
	return false
}

// retainedFailureResult reports whether a tool result records an outcome the
// user still needs to see after the checkpoint: an explicit failure or
// cancellation, or a legacy result without a terminal status whose content
// reads as an error. Successful output never qualifies, even when it contains
// error-looking text, because the terminal status takes precedence.
func retainedFailureResult(msg message.Message) bool {
	if status := strings.TrimSpace(msg.ToolStatus); status != "" {
		return isToolResultUnsuccessfulStatus(status)
	}
	return isToolResultErrorMessage(msg)
}

// toolCallBatchComplete reports whether every non-empty tool call of the
// assistant message has its result inside the batch. A batch truncated by the
// archive boundary is left to the archive instead of replayed as an assistant
// tool call without its response.
func toolCallBatchComplete(assistant message.Message, results []message.Message) bool {
	if len(assistant.ToolCalls) == 0 {
		return false
	}
	for _, call := range assistant.ToolCalls {
		if call.ID == "" {
			continue
		}
		matched := false
		for _, result := range results {
			if result.ToolCallID == call.ID {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
