package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// maxCheckpointRetainedFailureBatches bounds how many failed tool batches a
// model-driven checkpoint re-attaches to the live transcript, counting back
// from the newest. It matches maxToolErrorEvidenceItems so the live records
// and the checkpoint's evidence pack agree on how many failures still count:
// older failures stay in the archive and, as labeled excerpts, in the pack.
const maxCheckpointRetainedFailureBatches = maxToolErrorEvidenceItems

// maxCheckpointRetainedFailureBytes bounds what the retained records add to
// the projected post-reset surface, measured the same way the preflight
// measures it: the projected surface is metered in messageContextBytes, which
// counts payload bytes (Content, or the Parts text and binary payloads when a
// message carries Parts) plus the assistant call's ToolCalls[].Args, so the
// cap has to count both as well — otherwise a batch with a huge call argument
// would slip past a byte budget that claims to cover it.
// Retention must not argue against the checkpoint that triggered it: the
// low-gain preflight weighs the projected surface against
// modelDrivenLowGainMinTokens, and a batch of large successful results — kept
// only because it shares the turn with a failure — would otherwise eat that
// margin and turn a worthwhile checkpoint into a skip.
// Successful bodies are elided below; this cap is the backstop for what
// elision does not touch (tool-call arguments and the failure bodies that are
// the whole reason the batch stays). Batches yield oldest-first like the count
// cap, and the newest batch always stays so the failure behind this checkpoint
// remains readable.
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
//
// A rejected compact_context request that a later successful call of the same
// tool already replaced is not retained: the retry, not the rejection, is what
// the head settled on, and the retained records are re-attached directly under
// the new checkpoint — a superseded rejection there reads as a checkpoint that
// ran and failed right after one applied.
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
	var superseded map[string]struct{}
	supersededLoaded := false
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
		if !supersededLoaded {
			superseded = toolFailureSupersededByLaterSuccess(head)
			supersededLoaded = true
		}
		if supersededCompactContextBatch(assistant, superseded) {
			i = end
			continue
		}
		batches = append(batches, elideRetainedBatch(head[i:end]))
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
		out = append(out, batch...)
	}
	return out
}

// supersededCompactContextBatch reports whether every tool call of a retained
// batch is a compact_context request the runtime rejected and a later call of
// the same tool already replaced (toolFailureSupersededByLaterSuccess). A
// rejected request never settled — the barrier refused it before it could arm —
// so once a later call was accepted, the rejection is no longer part of the
// head's outcome: keeping it would replay a failure that the checkpoint being
// written just superseded, directly under that checkpoint. Requiring every
// call of the batch to be in the set leaves a batch that also carries another
// call alone, because that failure is not the checkpoint's own; such a call
// can only have entered the set through the path rule, which never covers a
// compact_context call (it has no target).
func supersededCompactContextBatch(assistant message.Message, superseded map[string]struct{}) bool {
	if len(superseded) == 0 || len(assistant.ToolCalls) == 0 {
		return false
	}
	for _, call := range assistant.ToolCalls {
		if _, ok := superseded[strings.TrimSpace(call.ID)]; !ok {
			return false
		}
	}
	return true
}

// elideRetainedBatch applies elision once, at the point a batch becomes a
// retention candidate, so the byte cap measures exactly the records that are
// returned instead of re-deriving them per batch.
func elideRetainedBatch(batch []message.Message) []message.Message {
	out := make([]message.Message, 0, len(batch))
	for _, msg := range batch {
		out = append(out, elideRetainedResult(msg))
	}
	return out
}

// elideRetainedResult replaces a successful result's bodies with a size marker.
// The record has to stay so the batch replays as a complete call/response pair,
// but its output is in the archive, and a full copy of it can weigh more than
// the checkpoint saves. Failures keep their text: they are the reason the
// record stayed live.
//
// What is elided mirrors the preflight surface (messageContextBytes): Content
// or, when the result carries Parts, the Parts text and binary payloads, plus
// the ToolPayload and ToolDiff copies behind them. message.Message documents
// that Parts supersedes Content, so clearing Content alone would leave the
// whole body live for an image-bearing result, and ToolPayload has to go with
// it because it is the raw form of the same output (which is also why the
// marker must not count it twice — see elidedToolResultContent).
//
// Parts are dropped outright rather than reduced to an attachment
// reference: a binary part keeps reaching the provider through
// binaryPartPayload, which re-reads ImagePath from disk at request time, so a
// reference-only copy would still send every byte the marker claims to have
// removed. The marker records what the size was; the archive holds the blob,
// and the checkpoint's key-file recall is how a still-relevant attachment
// comes back. ToolNotes are kept — the estimator does not count them, and they
// are the runtime's own diagnosis (retry hints, polling guidance), not a copy
// of the output. FileState is kept for the same reason: hashes are bytes-cheap
// and restore-time sentinels read them, while clearing ToolDiff but keeping
// ToolDiffAdded/Removed is safe because the shape hash only needs the counts
// to detect a rewrite.
func elideRetainedResult(msg message.Message) message.Message {
	if msg.Role != message.RoleTool || retainedFailureResult(msg) {
		return msg
	}
	msg.Content = elidedToolResultContent(msg)
	msg.Parts = nil
	msg.ToolPayload = ""
	msg.ToolDiff = ""
	return msg
}

// elidedToolResultContent renders the marker that replaces an elided body,
// reporting the payload bytes the elision actually removed from the request
// surface. ToolPayload is deliberately not added: message.Message defines
// Content as the model-visible combination of payload and notes, so the
// payload is already inside the bytes MessagePayloadBytes counted (and inside
// the Parts text when Parts supersede Content). ToolDiff is a separate field
// and is counted on its own.
func elidedToolResultContent(msg message.Message) string {
	size := len(msg.ToolDiff) + ctxmgr.MessagePayloadBytes([]message.Message{msg})
	if size == 0 {
		return ""
	}
	return fmt.Sprintf("[result elided by checkpoint: %d bytes]", size)
}

// retainedBatchBytes is what an already-elided batch costs, in the same units
// the cap polices: the payload bytes messageContextBytes attributes to each
// record (Content, or the Parts text and binary payloads while any remain)
// plus the assistant call's ToolCalls[].Args. Assistant text is normally empty
// — the cost of a call is its arguments — and the non-payload fields
// messageContextBytes also counts (thinking blocks, responses output,
// reasoning content, call IDs) stay out here exactly like they stay out of the
// retained records themselves: they are never part of what this cap decides to
// keep or drop.
func retainedBatchBytes(batch []message.Message) int {
	total := ctxmgr.MessagePayloadBytes(batch)
	for _, msg := range batch {
		for _, call := range msg.ToolCalls {
			total += len(call.Args)
		}
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
	return slices.ContainsFunc(results, retainedFailureResult)
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
