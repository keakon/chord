package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	sonicjson "github.com/bytedance/sonic"
)

var mcpJSONRPCBenchRequest = JSONRPCRequest{
	JSONRPC: "2.0",
	ID:      42,
	Method:  "tools/call",
	Params: map[string]any{
		"name": "large",
		"arguments": map[string]any{
			"path":    "/tmp/example.txt",
			"content": strings.Repeat("x", 4096),
			"limit":   200,
		},
	},
}

var mcpJSONRPCBenchResponseBytes = []byte(`{"jsonrpc":"2.0","id":42,"result":{"content":[{"type":"text","text":"` + strings.Repeat("x", 4096) + `"}],"isError":false}}`)

func BenchmarkJSONRPCMarshalStdlib(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(mcpJSONRPCBenchRequest); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCMarshalSonic(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := sonicjson.ConfigDefault.Marshal(mcpJSONRPCBenchRequest); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCMarshalSonicStd(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := sonicjson.ConfigStd.Marshal(mcpJSONRPCBenchRequest); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCResponseDecodeStdlib(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var resp JSONRPCResponse
		if err := json.Unmarshal(mcpJSONRPCBenchResponseBytes, &resp); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCResponseDecodeSonic(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var resp JSONRPCResponse
		if err := sonicjson.ConfigDefault.Unmarshal(mcpJSONRPCBenchResponseBytes, &resp); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCResponseDecodeSonicStream(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var resp JSONRPCResponse
		if err := mcpWireJSON.NewDecoder(bytes.NewReader(mcpJSONRPCBenchResponseBytes)).Decode(&resp); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCResponseDecodeSonicStd(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var resp JSONRPCResponse
		if err := sonicjson.ConfigStd.Unmarshal(mcpJSONRPCBenchResponseBytes, &resp); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCToolCallResultDecodeSonic(b *testing.B) {
	var resp JSONRPCResponse
	if err := sonicjson.ConfigDefault.Unmarshal(mcpJSONRPCBenchResponseBytes, &resp); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var result toolCallResult
		if err := sonicjson.ConfigDefault.Unmarshal(resp.Result, &result); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCToolCallResultDecodeSonicCopyString(b *testing.B) {
	var resp JSONRPCResponse
	if err := sonicjson.ConfigDefault.Unmarshal(mcpJSONRPCBenchResponseBytes, &resp); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var result toolCallResult
		if err := mcpLongLivedJSON.Unmarshal(resp.Result, &result); err != nil {
			b.Fatal(err)
		}
	}
}
