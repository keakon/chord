package sessionimport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sonicjson "github.com/bytedance/sonic"
)

var codexJSONLBenchLines = buildCodexJSONLBenchLines(256)

func BenchmarkCodexJSONLLineDecodeStdlib(b *testing.B) {
	for i := 0; i < b.N; i++ {
		line := codexJSONLBenchLines[i%len(codexJSONLBenchLines)]
		var lineObj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &lineObj); err != nil {
			b.Fatal(err)
		}
		var typ string
		if err := json.Unmarshal(lineObj["type"], &typ); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCodexJSONLLineDecodeSonic(b *testing.B) {
	for i := 0; i < b.N; i++ {
		line := codexJSONLBenchLines[i%len(codexJSONLBenchLines)]
		var lineObj map[string]json.RawMessage
		if err := importJSONUnmarshalString(line, &lineObj); err != nil {
			b.Fatal(err)
		}
		var typ string
		if err := importJSONUnmarshal(lineObj["type"], &typ); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCodexJSONLLineDecodeSonicStd(b *testing.B) {
	for i := 0; i < b.N; i++ {
		line := codexJSONLBenchLines[i%len(codexJSONLBenchLines)]
		var lineObj map[string]json.RawMessage
		if err := sonicjson.ConfigStd.UnmarshalFromString(line, &lineObj); err != nil {
			b.Fatal(err)
		}
		var typ string
		if err := sonicjson.ConfigStd.Unmarshal(lineObj["type"], &typ); err != nil {
			b.Fatal(err)
		}
	}
}

func buildCodexJSONLBenchLines(n int) []string {
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		payload := strings.Repeat(fmt.Sprintf(`{"type":"output_text_delta","delta":"chunk-%d"}`, i), 8)
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-06-09T00:00:%02dZ","type":"response_item","payload":{"id":"item-%d","type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}}`, i%60, i, payload))
	}
	return lines
}
