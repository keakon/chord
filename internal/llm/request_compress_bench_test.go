package llm

import (
	"strconv"
	"strings"
	"testing"
)

// benchRequestBody builds a body shaped like a real chat request: repetitive
// JSON envelopes around varying prose, so the codec has both structure to model
// and entropy to spend time on.
func benchRequestBody(sizeBytes int) []byte {
	var b strings.Builder
	b.Grow(sizeBytes + 1024)
	b.WriteString(`{"model":"provider/model-1","messages":[`)
	for i := 0; b.Len() < sizeBytes; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"role":"assistant","content":"step `)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(` inspected internal/agent/context_reduction.go and internal/llm/provider.go for retention rules"}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func BenchmarkZstdCompress400K(b *testing.B) {
	body := benchRequestBody(400 * 1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if _, err := zstdCompress(body); err != nil {
			b.Fatalf("zstdCompress returned error: %v", err)
		}
	}
}

func BenchmarkGzipCompress400K(b *testing.B) {
	body := benchRequestBody(400 * 1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if _, err := gzipCompress(body); err != nil {
			b.Fatalf("gzipCompress returned error: %v", err)
		}
	}
}
