package llm

import (
	"cmp"
	"encoding/json"
	"slices"

	"github.com/keakon/chord/internal/message"
)

// The completion event supplies evidence when status is omitted. An explicit
// non-completed status overrides that evidence for the individual tool item.
func responsesToolItemCompleted(status string) bool {
	return status == "" || status == "completed"
}

// An explicitly unfinished item cannot become executable or enter replay just
// because the terminal snapshot omits its status.
func omitIncompleteResponsesToolOutput(output []responsesOutputEntry, incomplete map[string]bool) []responsesOutputEntry {
	if len(incomplete) == 0 {
		return output
	}
	return slices.DeleteFunc(output, func(out responsesOutputEntry) bool {
		return (out.Type == "function_call" || out.Type == "custom_tool_call") &&
			responsesToolCallMarked(incomplete, responsesStreamItem{ID: out.ID, CallID: out.CallID})
	})
}

// completeResponsesToolCallsFromOutput settles pending calls using the terminal
// output's per-item completion evidence. Matching by identity instead of slice
// position preserves calls when intervening output_item events were missed.
func completeResponsesToolCallsFromOutput(state responsesEventState, output []responsesOutputEntry) error {
	var missing []responsesOutputEntry
	for _, out := range output {
		if out.Type != "function_call" && out.Type != "custom_tool_call" {
			continue
		}
		if !responsesToolItemCompleted(out.Status) {
			state.resp.StopReason = "length"
			continue
		}
		item := responsesStreamItem{ID: out.ID, CallID: out.CallID, Name: out.Name}
		if responsesToolCallMarked(state.incompleteCalls, item) {
			continue
		}
		if responsesToolCallAlreadyFinalized(state.finalizedCalls, item) {
			continue
		}
		callID := responsesToolCallID(item)
		matched := false
		customIdx, customKnown := state.customItemToIndex[out.ID]
		for idx, acc := range state.toolCalls {
			if (out.ID == "" || out.ID != acc.itemID) && (callID == "" || callID != acc.id) &&
				!(out.Type == "custom_tool_call" && customKnown && idx == customIdx) {
				continue
			}
			if err := acc.mergeMetadata(item); err != nil {
				return err
			}
			args := json.RawMessage(out.Arguments)
			if out.Type == "custom_tool_call" {
				acc.custom = true
				args = json.RawMessage(out.Input)
			}
			maybeEmitResponsesToolStart(acc, state.cb)
			finalizeOneResponsesToolCall(state.toolCalls, idx, state.resp, state.cb, false, args, state.finalizedCalls)
			matched = true
			break
		}
		if !matched {
			missing = append(missing, out)
		}
	}
	recoverResponsesToolCallsFromOutput(state.resp, missing, state.cb)
	// Calls may finish in a different order or be recovered only at the end.
	// Execution follows the terminal output order, not event arrival order.
	if len(state.resp.ToolCalls) > 1 {
		positions := make(map[string]int, len(output))
		for i, out := range output {
			if out.Type != "function_call" && out.Type != "custom_tool_call" {
				continue
			}
			id := responsesToolCallID(responsesStreamItem{ID: out.ID, CallID: out.CallID})
			if _, exists := positions[id]; !exists {
				positions[id] = i
			}
		}
		position := func(id string) int {
			if i, exists := positions[id]; exists {
				return i
			}
			return len(output)
		}
		slices.SortStableFunc(state.resp.ToolCalls, func(a, b message.ToolCall) int {
			return cmp.Compare(position(a.ID), position(b.ID))
		})
	}
	return nil
}
