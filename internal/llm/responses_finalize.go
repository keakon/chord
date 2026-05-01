package llm

import (
	"encoding/json"
	"github.com/keakon/golog/log"
	"sort"

	"github.com/keakon/chord/internal/message"
)

func applyResponsesCompletionPayload(resp *message.Response, payload responsesCompletedPayload, truncated *bool) {
	resp.ProviderResponseID = payload.ID
	if payload.Usage != nil {
		u := payload.Usage
		resp.Usage = &message.TokenUsage{
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
		}
		if u.InputTokensDetails != nil {
			resp.Usage.CacheReadTokens = u.InputTokensDetails.CachedTokens
		}
		if u.OutputTokensDetails != nil {
			resp.Usage.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
		}
	}
	if payload.IncompleteDetails != nil && payload.IncompleteDetails.Reason != "" {
		resp.StopReason = "length"
		if truncated != nil {
			*truncated = true
		}
		return
	}
	for _, out := range payload.Output {
		if out.Type == "function_call" {
			resp.StopReason = "tool_calls"
			return
		}
	}
	resp.StopReason = "stop"
}

func recoverResponsesToolCallsFromOutput(resp *message.Response, output []responsesOutputEntry, cb StreamCallback) {
	for _, out := range output {
		if out.Type != "function_call" {
			continue
		}
		callID := out.CallID
		if callID == "" {
			callID = out.ID
		}
		args := json.RawMessage(out.Arguments)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		args = unwrapJSONString(args)
		log.Warnf("responses: recovering tool call from response.completed output (output_item.added was missed) tool=%v call_id=%v", out.Name, callID)
		resp.ToolCalls = append(resp.ToolCalls, message.ToolCall{
			ID:   callID,
			Name: out.Name,
			Args: args,
		})
		if cb != nil {
			cb(message.StreamDelta{
				Type: "tool_use_start",
				ToolCall: &message.ToolCallDelta{
					ID:   callID,
					Name: out.Name,
				},
			})
		}
	}
}

// finalizeOneResponsesToolCall finalizes a single tool call by output index and removes it from the map.
// Used from output_item.done to avoid double-append when the stream sends duplicate done events.
// When doneArguments is non-empty (e.g. from response.output_item.done item.arguments), it is
// used as the final args instead of accumulated deltas.
func finalizeOneResponsesToolCall(
	toolCalls map[int]*responsesToolAccumulator,
	idx int,
	resp *message.Response,
	cb StreamCallback,
	truncated bool,
	doneArguments json.RawMessage,
	finalizedCalls map[string]bool,
) {
	acc, ok := toolCalls[idx]
	if !ok {
		return
	}
	if truncated {
		log.Warnf("discarding truncated tool call in responses API tool=%v id=%v partial_args=%v", acc.name, acc.id, acc.args.String())
		delete(toolCalls, idx)
		return
	}
	args := json.RawMessage(acc.args.String())
	if len(doneArguments) > 0 {
		args = doneArguments
	} else if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	args = unwrapJSONString(args)
	if !json.Valid(args) {
		log.Warnf("tool call has invalid JSON args in responses API tool=%v id=%v raw_args=%v", acc.name, acc.id, string(args))
		args = json.RawMessage(`{"error":"malformed tool call arguments from model"}`)
	}
	log.Debugf("finalized tool call (responses API) tool=%v id=%v args=%v", acc.name, acc.id, string(args))
	resp.ToolCalls = append(resp.ToolCalls, message.ToolCall{
		ID:   acc.id,
		Name: acc.name,
		Args: args,
	})
	if cb != nil {
		cb(message.StreamDelta{
			Type: "tool_use_end",
			ToolCall: &message.ToolCallDelta{
				ID:   acc.id,
				Name: acc.name,
			},
		})
	}
	// Track finalized call_id to skip duplicate events from proxies.
	if finalizedCalls != nil {
		finalizedCalls[acc.id] = true
	}
	delete(toolCalls, idx)
}

// finalizeResponsesToolCalls converts all accumulated tool calls into the response.
func finalizeResponsesToolCalls(
	toolCalls map[int]*responsesToolAccumulator,
	resp *message.Response,
	cb StreamCallback,
	truncated bool,
) {
	if len(toolCalls) == 0 {
		return
	}

	if truncated {
		for idx, acc := range toolCalls {
			log.Warnf("discarding truncated tool call in responses API tool=%v id=%v partial_args=%v", acc.name, acc.id, acc.args.String())
			delete(toolCalls, idx)
		}
		return
	}

	// Process in index order.
	indices := make([]int, 0, len(toolCalls))
	for idx := range toolCalls {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	for _, idx := range indices {
		acc := toolCalls[idx]
		args := json.RawMessage(acc.args.String())
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		args = unwrapJSONString(args)
		// If stream ended without response.incomplete but args are invalid JSON (e.g. truncated
		// mid-tool-call), treat as truncation: do not append malformed, set StopReason so agent
		// does not count as malformed and can suggest new conversation / max_output_tokens.
		if !json.Valid(args) {
			log.Warnf("discarding incomplete tool call (invalid JSON, likely output truncation) tool=%v id=%v partial_args=%v", acc.name, acc.id, acc.args.String())
			resp.StopReason = "length"
			delete(toolCalls, idx)
			continue
		}
		log.Debugf("finalized tool call (responses API) tool=%v id=%v args=%v", acc.name, acc.id, string(args))
		resp.ToolCalls = append(resp.ToolCalls, message.ToolCall{
			ID:   acc.id,
			Name: acc.name,
			Args: args,
		})
		if cb != nil {
			cb(message.StreamDelta{
				Type: "tool_use_end",
				ToolCall: &message.ToolCallDelta{
					ID:   acc.id,
					Name: acc.name,
				},
			})
		}
		delete(toolCalls, idx)
	}
}

// unwrapJSONString decodes JSON string layers until the result is not a string (e.g. object/array).
// Some APIs send arguments as a string or double-encoded string; tools expect a JSON object.
func unwrapJSONString(raw json.RawMessage) json.RawMessage {
	for len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := responsesSSEUnmarshal(raw, &s); err != nil {
			break
		}
		raw = json.RawMessage(s)
	}
	return raw
}

// UnwrapToolArgs unwraps JSON string layers so tool handlers receive a JSON object, not a string.
// Call before passing Args to tools.Execute when the provider may send arguments as a string.
func UnwrapToolArgs(raw json.RawMessage) json.RawMessage {
	return unwrapJSONString(raw)
}
