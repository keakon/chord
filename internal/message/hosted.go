package message

import "encoding/json"

// HostedObservation captures provider-side (hosted) tool activity observed on
// a hosted sub-request and its continuations. The main conversation receives
// an ordinary tool result; native items stay within the hosted request and
// can also be preserved in a session artifact for inspection.
type HostedObservation struct {
	// Summary is the model text returned alongside the hosted blocks. It is
	// not a tool result by itself; callers must not flatten it when no hosted
	// call was observed.
	Summary string
	// Calls are the hosted tool calls the provider emitted, in provider order.
	Calls []HostedCall
	// Usage is the sub-request token usage normalized by the transport layer.
	Usage *TokenUsage
	// Items retains complete native blocks/items in provider order, including
	// annotations and unknown types. It belongs only to this hosted sub-request.
	Items []json.RawMessage
	// Container identifies the Anthropic sandbox for a continuation.
	Container string
	// RequiresApproval identifies a provider-side approval boundary.
	RequiresApproval bool
}

const HostedCallKindImageGeneration = "image_generation_call"

// HostedCall is one provider-side tool call. Input and Result carry the raw
// wire payloads so family- and tool-specific formatting stays in the tool
// layer: the transport only pairs and classifies them. Result is filled only
// on a terminal success; a failed or interrupted call carries Error instead.
type HostedCall struct {
	Parts []ContentPart `json:"parts,omitempty"`
	// ID is the provider-side call id (Anthropic tool_use id, Responses item id).
	ID string
	// Name is the tool name when the provider reports one; empty when only a
	// result block was observed.
	Name string
	// Kind is the native block/item type (for example server_tool_use or
	// web_search_call), kept for diagnostics and formatting.
	Kind string
	// Status is the provider-reported status when the family reports one.
	Status string
	// Input is the call's arguments as raw JSON (Anthropic input object,
	// Responses action object); empty when the provider sent none.
	Input json.RawMessage
	// Result is the raw result payload of a completed call.
	Result json.RawMessage
	// Error describes a terminal failure (an error object or a failed item
	// status).
	Error string
}

// PendingCalls returns the number of hosted calls that reached neither a
// result nor an error. A non-zero value means the provider did not complete
// every call (for example a truncated stream), which callers treat as a
// validation failure.
func (o *HostedObservation) PendingCalls() int {
	if o == nil {
		return 0
	}
	pending := 0
	for i := range o.Calls {
		if len(o.Calls[i].Result) == 0 && o.Calls[i].Error == "" {
			pending++
		}
	}
	return pending
}

// HasCompleteResult reports whether at least one hosted call produced a
// result payload. An empty result list still counts: the call ran and found
// nothing.
func (o *HostedObservation) HasCompleteResult() bool {
	if o == nil {
		return false
	}
	for i := range o.Calls {
		if len(o.Calls[i].Result) > 0 {
			return true
		}
	}
	return false
}

// CallErrors returns the terminal error messages of all calls, in call order.
func (o *HostedObservation) CallErrors() []string {
	if o == nil {
		return nil
	}
	var errs []string
	for i := range o.Calls {
		if o.Calls[i].Error != "" {
			errs = append(errs, o.Calls[i].Error)
		}
	}
	return errs
}
