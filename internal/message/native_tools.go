package message

import (
	"bytes"
	"encoding/json"
	"slices"
)

type NativeRequestOutcome string

const (
	NativeRequestCompleted NativeRequestOutcome = "completed"
	NativeRequestNotSent   NativeRequestOutcome = "not_sent"
	NativeRequestRejected  NativeRequestOutcome = "rejected"
	NativeRequestUnknown   NativeRequestOutcome = "outcome_unknown"
)

func (o NativeRequestOutcome) Unexecuted() bool {
	return o == NativeRequestNotSent || o == NativeRequestRejected
}

// NativeToolAuthorization identifies the tool contract and its immutable,
// adapter-validated constraints. Endpoint identity is carried by the request.
type NativeToolAuthorization struct {
	Tool        string          `json:"tool"`
	Contract    string          `json:"contract"`
	Constraints json.RawMessage `json:"constraints"`
}

func (a NativeToolAuthorization) Equal(other NativeToolAuthorization) bool {
	return a.Tool == other.Tool && a.Contract == other.Contract && bytes.Equal(a.Constraints, other.Constraints)
}

// NativeToolHistory is an immutable receipt for server-side execution in a
// main request. It must never be interpreted as client-dispatched ToolCalls.
type NativeToolHistory struct {
	OutcomeUnknown bool                    `json:"outcome_unknown,omitempty"`
	Authorization  NativeToolAuthorization `json:"authorization"`
	RequestIDs     []string                `json:"request_ids"`
	Target         string                  `json:"target"`
	Protocol       string                  `json:"protocol"`
	APIURL         string                  `json:"api_url"`
	Container      string                  `json:"container,omitempty"`
	Items          []json.RawMessage       `json:"items"`
	Calls          []HostedCall            `json:"calls,omitempty"`
}

// Clone isolates the receipt's raw provider data from export and request owners.
func (h *NativeToolHistory) Clone() *NativeToolHistory {
	if h == nil {
		return nil
	}
	out := *h
	out.Authorization.Constraints = slices.Clone(h.Authorization.Constraints)
	out.RequestIDs = slices.Clone(h.RequestIDs)
	out.Items = make([]json.RawMessage, len(h.Items))
	for i, item := range h.Items {
		out.Items[i] = slices.Clone(item)
	}
	out.Calls = slices.Clone(h.Calls)
	for i := range out.Calls {
		out.Calls[i].Input = slices.Clone(h.Calls[i].Input)
		out.Calls[i].Result = slices.Clone(h.Calls[i].Result)
	}
	return &out
}
