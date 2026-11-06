package sessionimport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var codexJSONLBenchLines = buildCodexJSONLBenchLines(256)

func BenchmarkCodexJSONLLineDecode(b *testing.B) {
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
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

func buildCodexJSONLBenchLines(n int) []string {
	lines := make([]string, 0, n)
	for i := range n {
		payload := strings.Repeat(fmt.Sprintf(`{"type":"output_text_delta","delta":"chunk-%d"}`, i), 8)
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-06-09T00:00:%02dZ","type":"response_item","payload":{"id":"item-%d","type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}}`, i%60, i, payload))
	}
	return lines
}
