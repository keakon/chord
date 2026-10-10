package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/keakon/chord/internal/message"
)

// projectNativeImageReplay adds original bytes only to the request surface.
// Neither transcript receipts nor the native request journal acquire base64.
// Complete image items replay without depending on server-side storage or TTL.
func projectNativeImageReplay(ctx context.Context, messages []message.Message) ([]message.Message, error) {
	var projected []message.Message
	policy, _ := ctx.Value(nativePolicyContextKey{}).(*NativeToolPolicy)
	for i, msg := range messages {
		if msg.NativeTools == nil {
			continue
		}
		var items []json.RawMessage
		images, err := msg.NativeTools.ImageReplayItems()
		if err != nil {
			return nil, err
		}
		for _, image := range images {
			if msg.NativeTools.OutcomeUnknown {
				return nil, fmt.Errorf("native image %q outcome is unknown", image.Call.ID)
			}
			var item map[string]json.RawMessage
			if err := json.Unmarshal(image.Raw, &item); err != nil {
				return nil, fmt.Errorf("decode native image metadata: %w", err)
			}
			if image.ConfirmedFailure() {
				item["result"] = json.RawMessage("null")
			} else {
				part, confirmed := image.OriginalPart()
				if !confirmed {
					return nil, fmt.Errorf("native image %q has no confirmed original", image.Call.ID)
				}
				if policy == nil || policy.ImageOriginal == nil {
					return nil, fmt.Errorf("native image original resolver is unavailable")
				}
				data, err := policy.ImageOriginal(ctx, part)
				if err != nil {
					return nil, fmt.Errorf("read native image %q original: %w", image.Call.ID, err)
				}
				if len(data) == 0 {
					return nil, fmt.Errorf("native image %q original is empty", image.Call.ID)
				}
				item["result"], _ = json.Marshal(base64.StdEncoding.EncodeToString(data))
			}
			encoded, err := json.Marshal(item)
			if err != nil {
				return nil, fmt.Errorf("encode native image replay: %w", err)
			}
			if items == nil {
				items = slices.Clone(msg.NativeTools.Items)
			}
			items[image.Index] = encoded
		}
		if items != nil {
			if projected == nil {
				projected = slices.Clone(messages)
			}
			native := *msg.NativeTools
			native.Items = items
			projected[i].NativeTools = &native
		}
	}
	if projected == nil {
		return messages, nil
	}
	return projected, nil
}
