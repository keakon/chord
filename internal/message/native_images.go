package message

import (
	"encoding/json"
	"fmt"
	"slices"
)

const (
	HostedCallStatusCompleted = "completed"
	HostedCallStatusFailed    = "failed"
)

// NativeImageReplayItem pairs one wire item with its durable execution receipt.
// Only items on the request surface participate; Calls may also hold receipts
// from earlier continuations that this message does not replay.
type NativeImageReplayItem struct {
	Index  int
	Raw    json.RawMessage
	Status string
	Call   *HostedCall
}

func (h *NativeToolHistory) ImageReplayItems() ([]NativeImageReplayItem, error) {
	var images []NativeImageReplayItem
	for index, raw := range h.Items {
		var item struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, fmt.Errorf("decode native replay item: %w", err)
		}
		if item.Type != HostedCallKindImageGeneration {
			continue
		}
		callIndex := slices.IndexFunc(h.Calls, func(call HostedCall) bool {
			return call.ID == item.ID && call.Kind == HostedCallKindImageGeneration
		})
		if item.ID == "" || callIndex < 0 {
			return nil, fmt.Errorf("native image %q has no saved receipt", item.ID)
		}
		images = append(images, NativeImageReplayItem{Index: index, Raw: raw, Status: item.Status, Call: &h.Calls[callIndex]})
	}
	return images, nil
}

func (i NativeImageReplayItem) OriginalPart() (ContentPart, bool) {
	call := i.Call
	if i.Status != HostedCallStatusCompleted || call.Status != i.Status || call.Error != "" ||
		len(call.Result) == 0 || len(call.Parts) != 1 || call.Parts[0].Type != ContentPartImage {
		return ContentPart{}, false
	}
	return call.Parts[0], true
}

// NativeImageFailureStatus reports a confirmed failure in the Responses image
// contract. Other hosted tools have different terminal status sets.
func NativeImageFailureStatus(status string) bool {
	return status == HostedCallStatusFailed
}

// ConfirmedFailure requires the wire item and durable receipt to agree without
// retaining an original or successful result.
func (i NativeImageReplayItem) ConfirmedFailure() bool {
	return i.Call != nil && NativeImageFailureStatus(i.Status) && i.Call.Status == i.Status &&
		i.Call.Error != "" && len(i.Call.Parts) == 0 && len(i.Call.Result) == 0
}
