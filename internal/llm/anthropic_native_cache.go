package llm

import (
	"encoding/json"
	"fmt"
)

// Read only the cache metadata. Protocol fields and signed payloads remain in
// canonical Raw; a cache marker belongs solely to the outgoing projection.
func anthropicNativeContent(raw json.RawMessage) anthropicContent {
	var metadata struct {
		Type         string              `json:"type"`
		Text         string              `json:"text"`
		CacheControl *anthropicCacheCtrl `json:"cache_control"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		// MarshalJSON still validates the raw payload when building the request.
		return anthropicContent{Raw: raw}
	}
	return anthropicContent{Raw: raw, Type: metadata.Type, Text: metadata.Text, CacheControl: metadata.CacheControl}
}

func marshalAnthropicNativeContent(item anthropicContent) ([]byte, error) {
	if item.CacheControl == nil {
		return item.Raw, nil
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(item.Raw, &block); err != nil {
		return nil, fmt.Errorf("decode native cache projection: %w", err)
	}
	if block == nil {
		return nil, fmt.Errorf("native cache projection must be a content block")
	}
	marker, err := json.Marshal(item.CacheControl)
	if err != nil {
		return nil, fmt.Errorf("encode native cache marker: %w", err)
	}
	block["cache_control"] = marker
	return json.Marshal(block)
}
