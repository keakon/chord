package agent

import (
	"fmt"
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

// maxCheckpointRetainedFailureBytes bounds what the retained records add to
// the projected post-reset surface. Retention must not argue against the
// checkpoint that triggered it: the low-gain preflight weighs the projected
// surface against modelDrivenLowGainMinTokens, and a batch of large successful
// results — kept only because it shares the turn with a failure — would
// otherwise eat that margin and turn a worthwhile checkpoint into a skip.
// Successful bodies are elided below; this cap is the backstop for what
// elision cannot shrink (tool-call arguments, non-text parts). Batches yield
// oldest-first like the count cap, and the newest batch always stays so the
// failure behind this checkpoint remains readable.
const maxCheckpointRetainedFailureBytes = 8 << 10

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
	sizes := make([]int, len(batches))
	total := 0
	for i, batch := range batches {
		sizes[i] = retainedBatchBytes(batch)
		total += sizes[i]
	}
	for len(batches) > 1 && total > maxCheckpointRetainedFailureBytes {
		total -= sizes[0]
		batches, sizes = batches[1:], sizes[1:]
	}
	var out []message.Message
	for _, batch := range batches {
		for _, msg := range batch {
			out = append(out, elideRetainedResult(msg))
		}
	}
	return out
}

// elideRetainedResult replaces a successful result's bodies with a size marker.
// The record has to stay so the batch replays as a complete call/response pair,
// but its output is in the archive, and a full copy of it can weigh more than
// the checkpoint saves. Failures keep their text: they are the reason the
// record stayed live.
func elideRetainedResult(msg message.Message) message.Message {
	if msg.Role != message.RoleTool || retainedFailureResult(msg) {
		return msg
	}
	msg.Content = elidedToolResultContent(msg)
	msg.ToolPayload = ""
	msg.ToolDiff = ""
	return msg
}

func elidedToolResultContent(msg message.Message) string {
	size := len(msg.Content) + len(msg.ToolPayload) + len(msg.ToolDiff)
	if size == 0 {
		return ""
	}
	return fmt.Sprintf("[result elided by checkpoint: %d bytes]", size)
}

// retainedBatchBytes is what a batch costs once elision applies.
func retainedBatchBytes(batch []message.Message) int {
	total := 0
	for _, msg := range batch {
		total += len(elideRetainedResult(msg).Content)
	}
	return total
}

// retainedFailureCallIDs returns the call IDs of the failures the checkpoint
// keeps in the live transcript, so the evidence pack can leave those out: each
// one is already readable in full as a retained record.
func retainedFailureCallIDs(records []message.Message) map[string]struct{} {
	var ids map[string]struct{}
	for _, msg := range records {
		if msg.Role != message.RoleTool || !retainedFailureResult(msg) {
			continue
		}
		id := strings.TrimSpace(msg.ToolCallID)
		if id == "" {
			continue
		}
		if ids == nil {
			ids = make(map[string]struct{})
		}
		ids[id] = struct{}{}
	}
	return ids
}

// excludeRetainedFailureEvidence drops the tool-error evidence whose live
// record the checkpoint re-attaches. The live record shows the failure
// verbatim, so the pack's budget stays with the failures that survive only as
// excerpts.
func excludeRetainedFailureEvidence(items []evidenceItem, retained map[string]struct{}) []evidenceItem {
	if len(retained) == 0 {
		return items
	}
	kept := make([]evidenceItem, 0, len(items))
	for _, item := range items {
		if _, ok := retained[strings.TrimSpace(item.SourceID)]; ok {
			continue
		}
		kept = append(kept, item)
	}
	return kept
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
