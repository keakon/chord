package mcp

import (
	"encoding/json"
	"strings"
	"testing"
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

func BenchmarkJSONRPCMarshal(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(mcpJSONRPCBenchRequest); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCResponseDecode(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := decodeJSONRPCResponse(mcpJSONRPCBenchResponseBytes); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONRPCToolCallResultDecode(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{{"4KiB", 4 << 10}, {"1MiB", 1 << 20}} {
		raw := []byte(`{"jsonrpc":"2.0","id":42,"result":{"content":[{"type":"text","text":"` + strings.Repeat("x", size.bytes) + `"}],"isError":false}}`)
		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			for b.Loop() {
				resp, err := decodeJSONRPCResponse(raw)
				if err != nil {
					b.Fatal(err)
				}
				var result toolCallResult
				if err := json.Unmarshal(resp.Result, &result); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
