package llm

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

// toolArgumentsFallback ensures a replayed or recovered tool call always
// carries the required "arguments" field. A spelled-out `arguments: ""` would
// be dropped during serialization by omitempty, and the Responses API rejects
// a function_call item missing the field with a 400 "input[N].arguments"
// error. apply_patch keeps its canonical {patch} object shape; every other
// tool gets an empty object.
func toolArgumentsFallback(toolName string, raw json.RawMessage) json.RawMessage {
	if len(raw) > 0 {
		return raw
	}
	if toolName == toolname.ApplyPatch {
		return json.RawMessage(`{"patch":""}`)
	}
	return json.RawMessage(`{}`)
}

// canonicalApplyPatchArgs converts freeform custom_tool_call input into the
// canonical {"patch": "..."} arguments object every downstream consumer sees
// (finalize, replay, audit, execution). A payload that already carries a patch
// field passes through unchanged (gateway-lowered objects); everything else —
// bare patch text or a JSON string wrapping it — is treated as patch text.
func canonicalApplyPatchArgs(raw json.RawMessage) json.RawMessage {
	text := unwrapJSONString(raw)
	if json.Valid(text) {
		var obj struct {
			Patch *string `json:"patch"`
		}
		if err := json.Unmarshal(text, &obj); err == nil && obj.Patch != nil {
			return text
		}
	}
	out, err := json.Marshal(map[string]string{"patch": string(text)})
	if err != nil {
		return json.RawMessage(`{"patch":""}`)
	}
	return out
}

// normalizeResponsesOutputEntry maps a raw wire output entry to its canonical
// function_call representation. custom_tool_call entries (freeform text in
// input) become function_call entries with canonical {"patch": "..."}
// arguments, so every consumer — collectResponsesOutput, StopReason detection,
// recovery, and ResponsesOutput replay — reuses the existing function_call
// logic and never sees a raw custom item.
func normalizeResponsesOutputEntry(out responsesOutputEntry) responsesOutputEntry {
	if out.Type != "custom_tool_call" {
		return out
	}
	out.Type = "function_call"
	out.Arguments = string(canonicalApplyPatchArgs(json.RawMessage(out.Input)))
	return out
}

func applyResponsesCompletionPayload(resp *message.Response, payload responsesCompletedPayload, truncated *bool) {
	resp.ProviderResponseID = payload.ID
	if payload.Usage != nil {
		u := payload.Usage
		cacheWriteTokens := 0
		if u.InputTokensDetails != nil {
			cacheWriteTokens = u.InputTokensDetails.CacheWriteTokens
		}
		cacheReadTokens := 0
		if u.InputTokensDetails != nil {
			cacheReadTokens = u.InputTokensDetails.CachedTokens
		}
		resp.Usage = &message.TokenUsage{
			InputTokens:             u.InputTokens,
			OutputTokens:            u.OutputTokens,
			CacheWriteTokens:        cacheWriteTokens,
			CacheReadTokens:         cacheReadTokens,
			InputSemanticsKnown:     true,
			InputIncludesCacheRead:  true,
			InputIncludesCacheWrite: true,
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
	collectResponsesOutput(resp, payload.Output)
	if strings.TrimSpace(resp.Content) == "" {
		resp.Content = responsesOutputRefusalText(payload.Output)
	}
	for _, out := range payload.Output {
		if out.Type == "function_call" || out.Type == "custom_tool_call" {
			resp.StopReason = "tool_calls"
			return
		}
	}
	resp.StopReason = "stop"
}

func responsesOutputRefusalText(output []responsesOutputEntry) string {
	var refusals []string
	for _, out := range output {
		if out.Type != "message" {
			continue
		}
		for _, content := range out.Content {
			if content.Type == "refusal" && strings.TrimSpace(content.Refusal) != "" {
				refusals = append(refusals, content.Refusal)
			}
		}
	}
	return strings.Join(refusals, "\n")
}

// collectResponsesOutput preserves recognized Responses output items in their
// provider order so stateless replay does not reorder reasoning, messages, or
// sequential function calls. Entries go through the same mapping as the
// incremental baseline (responsesOutputEntryToMessageItem) so replayed
// function_call items keep the call_id→id fallback the streaming tool-call
// accumulator applies, and their outputs never end up orphaned.
//
// Streaming streams may produce a response.completed payload whose custom
// tool-call items carry no input (the full text only flows through the
// incremental accumulator, which lands in resp.ToolCalls). To avoid replayed
// function_call items missing the required arguments field, the canonical
// apply_patch args are back-filled from the accumulated tool call.
func collectResponsesOutput(resp *message.Response, output []responsesOutputEntry) {
	resp.ResponsesOutput = nil
	for _, out := range output {
		var item message.ResponsesOutputItem
		if out.Type == "custom_tool_call" {
			// normalizeResponsesOutputEntry already maps the entry to
			// function_call with canonical arguments, but a completed payload
			// may carry no input at all (the full text only reached the
			// accumulator), which would emit `arguments:""` and get dropped by
			// serialization. Prefer the accumulated tool call's exact arguments
			// so replay (and tool execution from ResponsesOutput) sees the full
			// patch rather than an empty object.
			item = responsesOutputEntryToMessageItem(out)
			item.Arguments = string(canonicalApplyPatchArgsFromToolCalls(resp.ToolCalls, out))
		} else {
			item = responsesOutputEntryToMessageItem(out)
		}
		switch item.Type {
		case "reasoning":
		case "message":
			if len(item.Content) == 0 {
				continue
			}
		case "function_call":
			item.Arguments = string(toolArgumentsFallback(item.Name, json.RawMessage(item.Arguments)))
			if strings.TrimSpace(item.CallID) == "" || strings.TrimSpace(item.Name) == "" {
				continue
			}
		default:
			continue
		}
		resp.ResponsesOutput = append(resp.ResponsesOutput, item)
	}
}

// canonicalApplyPatchArgsFromToolCalls returns the full accumulated apply_patch
// args for a custom_tool_call output entry. Streaming responses stop
// accumulating the freeform text in the wire input only when the accumulator is
// finalized into resp.ToolCalls, so this is the source for the canonical object.
// It falls back to the done-payload input when available, else the {patch:""}
// placeholder so the arguments field is never missing on replay.
func canonicalApplyPatchArgsFromToolCalls(calls []message.ToolCall, out responsesOutputEntry) json.RawMessage {
	id := out.CallID
	if id == "" {
		id = out.ID
	}
	for _, call := range slices.Backward(calls) {
		if call.ID == id {
			return canonicalApplyPatchArgs(call.Args)
		}
	}
	return canonicalApplyPatchArgs(json.RawMessage(out.Input))
}

func recoverResponsesToolCallsFromOutput(resp *message.Response, output []responsesOutputEntry, cb StreamCallback) {
	for _, out := range output {
		out = normalizeResponsesOutputEntry(out)
		if out.Type != "function_call" {
			continue
		}
		callID := out.CallID
		if callID == "" {
			callID = out.ID
		}
		if callID == "" || out.Name == "" {
			log.Warnf("responses: skip malformed recovered tool call tool=%v call_id=%v id=%v", out.Name, callID, out.ID)
			continue
		}
		args := json.RawMessage(out.Arguments)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		args = unwrapJSONString(args)
		log.Warnf("responses: recovering tool call from response.completed output (output_item.added was missed) tool=%v call_id=%v", out.Name, callID)
		resp.ToolCalls = append(resp.ToolCalls, message.ToolCall{
			ID:   cloneLongLivedLLMString(callID),
			Name: cloneLongLivedLLMString(out.Name),
			Args: args,
		})
		if cb != nil {
			cb(message.StreamDelta{
				Type: message.StreamDeltaToolUseStart,
				ToolCall: &message.ToolCallDelta{
					ID:   callID,
					Name: out.Name,
				},
			})
			cb(message.StreamDelta{
				Type: message.StreamDeltaToolUseEnd,
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
	if acc.id == "" || acc.name == "" {
		log.Warnf("discarding malformed tool call in responses API tool=%v id=%v item_id=%v args=%v", acc.name, acc.id, acc.itemID, acc.args.String())
		delete(toolCalls, idx)
		return
	}
	args := json.RawMessage(acc.args.String())
	if len(doneArguments) > 0 {
		args = doneArguments
	} else if len(args) == 0 {
		// An accumulator that saw no input must not inherit the generic {}
		// placeholder: a freeform custom call would replay the literal text
		// "{}" as the patch. The canonical empty object parses downstream as
		// the regular empty-patch error instead.
		if acc.custom {
			args = json.RawMessage(`{"patch":""}`)
		} else {
			args = json.RawMessage("{}")
		}
	}
	args = unwrapJSONString(args)
	if acc.custom {
		args = canonicalApplyPatchArgs(args)
	}
	if !json.Valid(args) {
		log.Warnf("tool call has invalid JSON args in responses API tool=%v id=%v raw_args=%v", acc.name, acc.id, string(args))
		args = json.RawMessage(MalformedArgsSentinel)
	}
	log.Debugf("finalized tool call (responses API) tool=%v id=%v args=%v", acc.name, acc.id, string(args))
	resp.ToolCalls = append(resp.ToolCalls, message.ToolCall{
		ID:   cloneLongLivedLLMString(acc.id),
		Name: cloneLongLivedLLMString(acc.name),
		Args: args,
	})
	if cb != nil && acc.streamStartEmitted && !acc.endEmitted {
		cb(message.StreamDelta{
			Type: message.StreamDeltaToolUseEnd,
			ToolCall: &message.ToolCallDelta{
				ID:   responsesToolStreamID(acc),
				Name: acc.name,
			},
		})
	}
	// Track finalized identifiers to skip duplicate events from proxies.
	markResponsesToolCallFinalized(finalizedCalls, acc)
	delete(toolCalls, idx)
}

// finalizeResponsesToolCalls converts all accumulated tool calls into the response.
func finalizeResponsesToolCalls(
	toolCalls map[int]*responsesToolAccumulator,
	resp *message.Response,
	cb StreamCallback,
	truncated bool,
	finalizedCalls map[string]bool,
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
		if acc.id == "" || acc.name == "" {
			log.Warnf("discarding malformed tool call in responses API tool=%v id=%v item_id=%v args=%v", acc.name, acc.id, acc.itemID, acc.args.String())
			delete(toolCalls, idx)
			continue
		}
		args := json.RawMessage(acc.args.String())
		if len(args) == 0 {
			// See finalizeOneResponsesToolCall: a custom accumulator with no
			// input must not become the literal patch "{}".
			if acc.custom {
				args = json.RawMessage(`{"patch":""}`)
			} else {
				args = json.RawMessage("{}")
			}
		}
		args = unwrapJSONString(args)
		if acc.custom {
			args = canonicalApplyPatchArgs(args)
		}
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
			ID:   cloneLongLivedLLMString(acc.id),
			Name: cloneLongLivedLLMString(acc.name),
			Args: args,
		})
		markResponsesToolCallFinalized(finalizedCalls, acc)
		if cb != nil && acc.streamStartEmitted && !acc.endEmitted {
			cb(message.StreamDelta{
				Type: message.StreamDeltaToolUseEnd,
				ToolCall: &message.ToolCallDelta{
					ID:   responsesToolStreamID(acc),
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
// tools.unwrapToolArgs is the stdlib-decoder twin of this helper; keep the two
// behaviourally identical when either changes.
func UnwrapToolArgs(raw json.RawMessage) json.RawMessage {
	return unwrapJSONString(raw)
}
