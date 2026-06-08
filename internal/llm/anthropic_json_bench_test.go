package llm

import (
	"encoding/json"
	"strings"
	"testing"

	sonicjson "github.com/bytedance/sonic"
)

var anthropicSSEBenchPayloads = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12000,"cache_creation_input_tokens":512,"cache_read_input_tokens":8192,"output_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":256,"ephemeral_1h_input_tokens":256}}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"write","input":{}}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"` + strings.Repeat(`\\\"chunk\\\":\\\"value\\\",`, 32) + `"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":128}}`,
}

func BenchmarkAnthropicSSEEventDecodeStdlib(b *testing.B) {
	for i := 0; i < b.N; i++ {
		decodeAnthropicSSEBenchPayloadStdlib(anthropicSSEBenchPayloads[i%len(anthropicSSEBenchPayloads)])
	}
}

func BenchmarkAnthropicSSEEventDecodeSonic(b *testing.B) {
	for i := 0; i < b.N; i++ {
		decodeAnthropicSSEBenchPayloadSonicDefault(anthropicSSEBenchPayloads[i%len(anthropicSSEBenchPayloads)])
	}
}

func BenchmarkAnthropicSSEEventDecodeSonicStd(b *testing.B) {
	for i := 0; i < b.N; i++ {
		decodeAnthropicSSEBenchPayloadSonicStd(anthropicSSEBenchPayloads[i%len(anthropicSSEBenchPayloads)])
	}
}

func decodeAnthropicSSEBenchPayloadStdlib(data string) {
	switch {
	case strings.Contains(data, `"message_start"`):
		var ev sseMessageStart
		_ = json.Unmarshal([]byte(data), &ev)
	case strings.Contains(data, `"content_block_start"`):
		var ev sseContentBlockStart
		_ = json.Unmarshal([]byte(data), &ev)
	case strings.Contains(data, `"content_block_delta"`):
		var ev sseContentBlockDelta
		_ = json.Unmarshal([]byte(data), &ev)
	case strings.Contains(data, `"content_block_stop"`):
		var ev sseContentBlockStop
		_ = json.Unmarshal([]byte(data), &ev)
	default:
		var ev sseMessageDelta
		_ = json.Unmarshal([]byte(data), &ev)
	}
}

func decodeAnthropicSSEBenchPayloadSonicDefault(data string) {
	switch {
	case strings.Contains(data, `"message_start"`):
		var ev sseMessageStart
		_ = sonicjson.ConfigDefault.UnmarshalFromString(data, &ev)
	case strings.Contains(data, `"content_block_start"`):
		var ev sseContentBlockStart
		_ = sonicjson.ConfigDefault.UnmarshalFromString(data, &ev)
	case strings.Contains(data, `"content_block_delta"`):
		var ev sseContentBlockDelta
		_ = sonicjson.ConfigDefault.UnmarshalFromString(data, &ev)
	case strings.Contains(data, `"content_block_stop"`):
		var ev sseContentBlockStop
		_ = sonicjson.ConfigDefault.UnmarshalFromString(data, &ev)
	default:
		var ev sseMessageDelta
		_ = sonicjson.ConfigDefault.UnmarshalFromString(data, &ev)
	}
}

func decodeAnthropicSSEBenchPayloadSonicStd(data string) {
	switch {
	case strings.Contains(data, `"message_start"`):
		var ev sseMessageStart
		_ = sonicjson.ConfigStd.UnmarshalFromString(data, &ev)
	case strings.Contains(data, `"content_block_start"`):
		var ev sseContentBlockStart
		_ = sonicjson.ConfigStd.UnmarshalFromString(data, &ev)
	case strings.Contains(data, `"content_block_delta"`):
		var ev sseContentBlockDelta
		_ = sonicjson.ConfigStd.UnmarshalFromString(data, &ev)
	case strings.Contains(data, `"content_block_stop"`):
		var ev sseContentBlockStop
		_ = sonicjson.ConfigStd.UnmarshalFromString(data, &ev)
	default:
		var ev sseMessageDelta
		_ = sonicjson.ConfigStd.UnmarshalFromString(data, &ev)
	}
}
