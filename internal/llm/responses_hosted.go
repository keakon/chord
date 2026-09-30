package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	sonicjson "github.com/bytedance/sonic"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// responsesHostedRequest is the minimal sub-request shape used by the local
// hosted tools: one user message and one raw hosted tool declaration, with an
// optional raw tool_choice that forces the call. The hint-only shape omits
// tool_choice and lets the prompt ask for the tool instead.
type responsesHostedRequest struct {
	Model           string               `json:"model"`
	Instructions    *string              `json:"instructions,omitempty"`
	Input           []responsesInputItem `json:"input"`
	Tools           []json.RawMessage    `json:"tools"`
	ToolChoice      json.RawMessage      `json:"tool_choice,omitempty"`
	MaxOutputTokens int                  `json:"max_output_tokens,omitempty"`
	Store           *bool                `json:"store,omitempty"`
	Stream          bool                 `json:"stream"`
	Include         []string             `json:"include,omitempty"`
	ServiceTier     string               `json:"service_tier,omitempty"`
}

// newResponsesHostedRequest builds the wire request for a hosted sub-request.
// The declaration and tool_choice are placed verbatim: Chord does not
// interpret the declaration's version or options. Arguments travel only in
// the wire fields, never folded into the prompt text, so a fallback to
// another target cannot silently drop them. rc mirrors the target's Responses
// compat so a gateway that rejects an optional field does not reject the
// sub-request either.
func newResponsesHostedRequest(model, systemPrompt, query string, ht *HostedToolRequest, maxTokens int, store bool, rc *config.ResponsesCompatConfig) (responsesHostedRequest, error) {
	if ht == nil || len(ht.Declaration) == 0 {
		return responsesHostedRequest{}, fmt.Errorf("hosted tool %q has no Responses declaration", hostedRequestName(ht))
	}
	if query == "" {
		return responsesHostedRequest{}, fmt.Errorf("hosted tool %q sub-request requires a user message", ht.Name)
	}
	var sendStore, sendInclude, sendMaxOutputTokens *bool
	if rc != nil {
		sendStore = rc.SendStore
		sendInclude = rc.SendReasoningInclude
		sendMaxOutputTokens = rc.SendMaxOutputTokens
	}
	req := responsesHostedRequest{
		Model: model,
		Input: []responsesInputItem{{
			Type:    "message",
			Role:    "user",
			Content: []responsesContentBlock{{Type: "input_text", Text: query}},
		}},
		Tools:  []json.RawMessage{ht.Declaration},
		Stream: true,
	}
	if len(ht.Force) > 0 {
		req.ToolChoice = ht.Force
	}
	if systemPrompt != "" {
		req.Instructions = new(systemPrompt)
	}
	if maxTokens > 0 && compatBool(sendMaxOutputTokens, true) {
		req.MaxOutputTokens = maxTokens
	}
	if compatBool(sendStore, true) {
		req.Store = new(store)
	}
	if compatBool(sendInclude, true) && len(ht.Include) > 0 {
		req.Include = ht.Include
	}
	return req, nil
}

// responsesHostedItem is the minimal shape of a hosted output item: enough to
// classify and complete the call. The full item is kept as the raw payload.
type responsesHostedItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	Action    json.RawMessage `json:"action"`
	Error     json.RawMessage `json:"error"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// responsesHostedCallState deduplicates a hosted item across the events that
// may carry it: output_item.added, output_item.done, and the terminal
// payload's output array.
type responsesHostedCallState struct {
	index    int
	used     bool
	terminal bool
}

// isResponsesHostedCallType reports whether an output item type is a
// provider-side call. Client-executed calls (function_call, custom_tool_call,
// local_shell_call, computer_call) must never be captured as hosted calls.
func isResponsesHostedCallType(itemType string) bool {
	if !strings.HasSuffix(itemType, "_call") {
		return false
	}
	switch itemType {
	case "function_call", "custom_tool_call", "local_shell_call", "computer_call":
		return false
	}
	return true
}

// responsesHostedToolName derives the declaration name from an item type
// (web_search_call → web_search).
func responsesHostedToolName(itemType string) string {
	return strings.TrimSuffix(itemType, "_call")
}

// recordResponsesHostedItem records one raw hosted output item. added
// registers a pending call; done and the terminal payload complete it. An
// item that never reaches a terminal status stays pending, which callers
// treat as an incomplete call.
func recordResponsesHostedItem(resp *message.Response, calls map[string]*responsesHostedCallState, raw json.RawMessage, terminalEvent bool) {
	if calls == nil || len(raw) == 0 {
		return
	}
	var item responsesHostedItem
	if err := sonicjson.ConfigDefault.Unmarshal(raw, &item); err != nil {
		return
	}
	if terminalEvent {
		retainHostedNativeItem(resp, item.ID, raw)
	}
	if item.Type == "mcp_approval_request" {
		ensureHostedObservation(resp).RequiresApproval = true
	}
	if !isResponsesHostedCallType(item.Type) {
		return
	}
	// An empty id cannot be deduplicated; keying it as one call at least
	// avoids double-counting the added/done pair of the same anonymous item.
	state := calls[item.ID]
	if state == nil {
		state = &responsesHostedCallState{}
		calls[item.ID] = state
	}
	obs := ensureHostedObservation(resp)
	if !state.used {
		state.used = true
		state.index = len(obs.Calls)
		obs.Calls = append(obs.Calls, message.HostedCall{
			ID:   cloneLongLivedLLMString(item.ID),
			Name: cloneLongLivedLLMString(responsesHostedToolName(item.Type)),
			Kind: cloneLongLivedLLMString(item.Type),
		})
	}
	if state.index < 0 || state.index >= len(obs.Calls) {
		return
	}
	call := &obs.Calls[state.index]
	if item.Name != "" {
		call.Name = cloneLongLivedLLMString(item.Name)
	}
	if len(item.Arguments) > 0 {
		call.Input = cloneHostedRaw(item.Arguments)
	}
	if len(item.Action) > 0 && len(item.Arguments) == 0 {
		call.Input = cloneHostedRaw(item.Action)
	}
	if state.terminal && !terminalEvent {
		return
	}
	status := strings.TrimSpace(item.Status)
	if status == "" {
		if terminalEvent {
			status = "completed"
		} else {
			status = "in_progress"
		}
	}
	call.Status = cloneLongLivedLLMString(status)
	if !terminalEvent {
		return
	}
	if rawError := bytes.TrimSpace(item.Error); len(rawError) > 0 && string(rawError) != "null" && string(rawError) != "\"\"" {
		state.terminal = true
		call.Status = "failed"
		call.Result = nil
		call.Error = item.Type + ": " + string(item.Error)
		return
	}
	switch status {
	case "completed":
		state.terminal = true
		call.Result = cloneHostedRaw(raw)
		call.Error = ""
	case "failed", "cancelled", "incomplete":
		state.terminal = true
		call.Result = nil
		call.Error = item.Type + ": " + status
	}
}

// responsesHostedRawItem extracts the raw item payload from an
// output_item.added/done event. The lightweight union used by the main parser
// drops fields a hosted item may carry, so the capture path re-reads the raw
// item.
func responsesHostedRawItem(eventData []byte) (json.RawMessage, bool) {
	var payload struct {
		Item json.RawMessage `json:"item"`
	}
	if err := responsesSSEUnmarshal(eventData, &payload); err != nil || len(payload.Item) == 0 {
		return nil, false
	}
	return payload.Item, true
}

// recordResponsesHostedOutputItems records every hosted call in a terminal
// payload's raw output array (response.completed / response.incomplete).
func recordResponsesHostedOutputItems(resp *message.Response, calls map[string]*responsesHostedCallState, eventData []byte) {
	var payload struct {
		Response struct {
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if err := responsesSSEUnmarshal(eventData, &payload); err != nil {
		return
	}
	for _, raw := range payload.Response.Output {
		recordResponsesHostedItem(resp, calls, raw, true)
	}
	// The terminal output array gives canonical provider order; item completion
	// events may arrive in a different order when hosted calls overlap.
	if len(payload.Response.Output) > 0 && calls != nil {
		obs := ensureHostedObservation(resp)
		obs.Items = make([]json.RawMessage, len(payload.Response.Output))
		for i, raw := range payload.Response.Output {
			obs.Items[i] = cloneHostedRaw(raw)
		}
	}
}

// retainHostedNativeItem keeps message annotations and unknown native items;
// repeated done/terminal payloads replace the same id in its original slot.
func retainHostedNativeItem(resp *message.Response, id string, raw json.RawMessage) {
	obs := ensureHostedObservation(resp)
	if id != "" {
		for i, old := range obs.Items {
			var existing struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(old, &existing) == nil && existing.ID == id {
				obs.Items[i] = cloneHostedRaw(raw)
				return
			}
		}
	}
	obs.Items = append(obs.Items, cloneHostedRaw(raw))
}
