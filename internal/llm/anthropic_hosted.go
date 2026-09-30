package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	sonicjson "github.com/bytedance/sonic"

	"github.com/keakon/chord/internal/message"
)

const (
	// anthropicServerToolUseBlock is the Anthropic block type of a hosted
	// (server-side) tool call.
	anthropicServerToolUseBlock = "server_tool_use"
	// anthropicHostedResultSuffix marks the block types that carry a hosted
	// tool's result (web_search_tool_result, web_fetch_tool_result, ...).
	anthropicHostedResultSuffix = "_tool_result"
)

// anthropicHostedRequest is the minimal sub-request shape used by the local
// hosted tools: one user message and one raw hosted tool declaration, with an
// optional raw tool_choice that forces the call. The hint-only shape omits
// tool_choice and lets the prompt ask for the tool instead.
type anthropicHostedRequest struct {
	Model      string             `json:"model"`
	MaxTokens  int                `json:"max_tokens"`
	System     []anthropicContent `json:"system,omitempty"`
	Messages   []json.RawMessage  `json:"messages"`
	Tools      []json.RawMessage  `json:"tools"`
	ToolChoice json.RawMessage    `json:"tool_choice,omitempty"`
	Stream     bool               `json:"stream"`
	Container  string             `json:"container,omitempty"`
}

// newAnthropicHostedRequest builds the wire request for a hosted sub-request.
// The declaration and tool_choice are placed verbatim: Chord does not
// interpret the declaration's version or options. Arguments travel only in
// the wire fields, never folded into the prompt text, so a fallback to
// another target cannot silently drop them.
func newAnthropicHostedRequest(model, systemPrompt, query string, ht *HostedToolRequest, maxTokens int) (anthropicHostedRequest, error) {
	if ht == nil || len(ht.Declaration) == 0 {
		return anthropicHostedRequest{}, fmt.Errorf("hosted tool %q has no Anthropic declaration", hostedRequestName(ht))
	}
	if query == "" {
		return anthropicHostedRequest{}, fmt.Errorf("hosted tool %q sub-request requires a user message", ht.Name)
	}
	req := anthropicHostedRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    buildSystemBlocks(systemPrompt),
		Messages:  append([]json.RawMessage(nil), ht.Messages...),
		Container: ht.Container,
		Tools:     []json.RawMessage{ht.Declaration},
		Stream:    true,
	}
	if len(req.Messages) == 0 {
		user, err := json.Marshal(map[string]string{"role": "user", "content": query})
		if err != nil {
			return anthropicHostedRequest{}, fmt.Errorf("marshal hosted prompt: %w", err)
		}
		req.Messages = []json.RawMessage{user}
	}
	if len(ht.Force) > 0 {
		req.ToolChoice = ht.Force
	}
	return req, nil
}

// appendAnthropicHostedCall records a hosted call and returns its index. The
// paired *_tool_result block completes it; unpaired calls show up in
// HostedObservation.PendingCalls. Anthropic reports no per-call status:
// completion is expressed solely by the paired result block.
func appendAnthropicHostedCall(resp *message.Response, id, name, kind string, input json.RawMessage) int {
	obs := ensureHostedObservation(resp)
	call := message.HostedCall{
		ID:   cloneLongLivedLLMString(id),
		Name: cloneLongLivedLLMString(name),
		Kind: cloneLongLivedLLMString(kind),
	}
	call.Input = anthropicHostedInputRaw(input)
	obs.Calls = append(obs.Calls, call)
	return len(obs.Calls) - 1
}

// applyAnthropicHostedInput stores the arguments accumulated from
// input_json_delta once the block closes. Invalid partial JSON is dropped
// rather than retained: the observation outlives the stream and its raw
// payloads may be embedded into a JSON result.
func applyAnthropicHostedInput(resp *message.Response, callIndex int, accumulated string) {
	if resp == nil || resp.Hosted == nil || callIndex < 0 || callIndex >= len(resp.Hosted.Calls) {
		return
	}
	raw := json.RawMessage(accumulated)
	if len(raw) == 0 || !json.Valid(raw) {
		return
	}
	resp.Hosted.Calls[callIndex].Input = cloneHostedRaw(raw)
}

// anthropicHostedInputRaw normalizes the input object carried by a
// content_block_start event. The empty object is the streaming placeholder,
// not the call's arguments.
func anthropicHostedInputRaw(input json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	return cloneHostedRaw(trimmed)
}

// recordAnthropicHostedResult stores a *_tool_result block on its call. The
// content is either the result array (possibly empty: the search ran and
// found nothing) or an error object; an unpaired result still produces a call
// so no payload is silently dropped.
func recordAnthropicHostedResult(resp *message.Response, byToolID map[string]int, blockType, toolUseID string, content json.RawMessage) {
	callIndex, paired := byToolID[toolUseID]
	if !paired {
		callIndex = appendAnthropicHostedCall(resp, toolUseID, "", blockType, nil)
		if toolUseID != "" && byToolID != nil {
			byToolID[toolUseID] = callIndex
		}
	}
	if resp == nil || resp.Hosted == nil || callIndex < 0 || callIndex >= len(resp.Hosted.Calls) {
		return
	}
	call := &resp.Hosted.Calls[callIndex]
	if len(call.Result) > 0 || call.Error != "" {
		return
	}
	if detail := anthropicHostedResultError(blockType, content); detail != "" {
		call.Error = detail
		return
	}
	call.Result = cloneHostedRaw(content)
}

// anthropicHostedResultError reports the error carried by a result block and
// returns "" for a successful result. Error objects are recognized by their
// error_code plus the *_error type suffix some families use.
func anthropicHostedResultError(blockType string, content json.RawMessage) string {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return blockType + ": missing result content"
	}
	if trimmed[0] != '{' {
		return ""
	}
	var errObj struct {
		Type      string `json:"type"`
		ErrorCode string `json:"error_code"`
	}
	if err := sonicjson.ConfigDefault.Unmarshal(trimmed, &errObj); err != nil {
		return blockType + ": unparsable result content"
	}
	if errObj.ErrorCode != "" {
		if errObj.Type != "" {
			return errObj.Type + ": " + errObj.ErrorCode
		}
		return errObj.ErrorCode
	}
	if strings.HasSuffix(errObj.Type, "_error") {
		return errObj.Type
	}
	return ""
}

// hostedRequestName keeps error messages readable when a declaration is
// missing entirely.
func hostedRequestName(ht *HostedToolRequest) string {
	if ht == nil {
		return ""
	}
	return ht.Name
}
