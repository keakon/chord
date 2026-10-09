package llm

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestAnthropicNativeCacheProjectionPreservesProtocolFields(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"text","text":"Sample answer","citations":[{"url":"https://example.invalid/"}],"extra":{"value":9007199254740993}}`),
		json.RawMessage(`{"type":"server_tool_use","id":"call-1","name":"web_search","input":{"query":"sample"}}`),
		json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"call-1","content":[]}`),
	} {
		t.Run(string(raw), func(t *testing.T) {
			history := []message.Message{{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Items: []json.RawMessage{raw}}}}
			before := string(raw)
			wire, mapping := convertMessagesWithMap(history)
			entry := mapping[0]
			applyCacheBreakpoints(nil, wire, AnthropicCacheBoundary{MessageIndex: entry.MessageIndex, BlockIndex: entry.BlockIndex, Valid: true}, AnthropicCacheBoundary{}, "1h", 0)
			blocks := wire[0].Content.([]anthropicContent)
			encoded, err := json.Marshal(blocks[0])
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			var marker anthropicCacheCtrl
			if err := json.Unmarshal(got["cache_control"], &marker); err != nil {
				t.Fatal(err)
			}
			if marker.Type != "ephemeral" || marker.TTL != "1h" {
				t.Fatalf("marker=%+v", marker)
			}
			delete(got, "cache_control")
			if !reflect.DeepEqual(got, want) || string(history[0].NativeTools.Items[0]) != before {
				t.Fatalf("native protocol fields changed: %s", encoded)
			}
		})
	}
}

func TestAnthropicNativeCacheSkipsSignedAndEmptyBlocks(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"thinking","thinking":"Sample reasoning","signature":"sig-1"}`),
		json.RawMessage(`{"type":"redacted_thinking","data":"encrypted-data"}`),
		json.RawMessage(`{"type":"text","text":""}`),
	} {
		history := []message.Message{{Role: message.RoleAssistant, NativeTools: &message.NativeToolHistory{Items: []json.RawMessage{json.RawMessage(`{"type":"text","text":"Sample answer"}`), raw}}}}
		wire, _ := convertMessagesWithMap(history)
		applyCacheBreakpoints(nil, wire, AnthropicCacheBoundary{MessageIndex: 0, BlockIndex: 1, Valid: true}, AnthropicCacheBoundary{}, "", 0)
		blocks := wire[0].Content.([]anthropicContent)
		if blocks[1].CacheControl != nil || blocks[0].CacheControl == nil {
			t.Fatalf("cache did not fall back to the eligible tail: %+v", blocks)
		}
		encoded, err := json.Marshal(blocks[1])
		if err != nil || string(encoded) != string(raw) {
			t.Fatalf("uncacheable raw changed: %s, %v", encoded, err)
		}
	}
}
