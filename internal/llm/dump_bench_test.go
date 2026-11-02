package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A regression back to encoding the whole dump at once shows up as B/op and
// allocs/op growing with the ~1 MiB request body.
func BenchmarkWriteDumpJSON(b *testing.B) {
	inputs := make([]string, 0, 20000)
	for range 20000 {
		inputs = append(inputs, `{"role":"user","content":"hello benchmark"}`)
	}
	body := json.RawMessage(`{"model":"sample/test-model","input":[` + strings.Join(inputs, ",") + `]}`)
	chunks := make([]string, 0, 2000)
	for range 2000 {
		chunks = append(chunks, `data: {"type":"response.output_text.delta","delta":"chunk"}`)
	}
	dump := &LLMDump{
		Timestamp:      "2026-11-02T12:34:56Z",
		Provider:       "responses",
		Model:          "sample/test-model",
		RequestBody:    body,
		RequestHeaders: http.Header{headerContentType: {"application/json"}},
		HTTPStatus:     http.StatusOK,
		SSEChunks:      chunks,
		Response:       &DumpResponse{Content: strings.Repeat("done ", 2000), StopReason: "stop"},
		DurationMS:     100,
	}
	headers := dumpRequestHeaders(dump.RequestHeaders)

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if _, err := writeDumpJSON(io.Discard, dump, body, headers); err != nil {
			b.Fatal(err)
		}
	}
}
