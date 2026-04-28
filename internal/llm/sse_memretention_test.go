package llm

import (
	"bytes"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

const (
	defaultSSEMemSettleIters     = 5
	defaultSSEMemSettleMaxGrowth = 32 << 20 // 32 MiB
)

type sseMemSettleConfig struct {
	Iters             int
	MaxGrowth         int64
	ForceOSMemoryTrim bool
}

func TestResponsesSSEMemorySettlesHermetic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SSE memory retention test in short mode")
	}
	fixture := hermeticResponsesLargeSSEFixture()
	runSSEMemorySettleTest(t, fixture, sseMemSettleConfig{
		Iters:             defaultSSEMemSettleIters,
		MaxGrowth:         defaultSSEMemSettleMaxGrowth,
		ForceOSMemoryTrim: true,
	})
}

func runSSEMemorySettleTest(t *testing.T, fixture sseBenchFixture, cfg sseMemSettleConfig) {
	t.Helper()
	if len(fixture.BodyBytes) == 0 {
		t.Fatal("fixture body is empty")
	}
	if cfg.Iters <= 0 {
		t.Fatal("iteration count must be positive")
	}

	if cfg.ForceOSMemoryTrim {
		debug.FreeOSMemory()
	} else {
		runtime.GC()
	}
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for i := 0; i < cfg.Iters; i++ {
		resp, err := parseSSEBenchFixture(fixture)
		if err != nil {
			t.Fatalf("iteration %d parse: %v", i, err)
		}
		if resp == nil {
			t.Fatalf("iteration %d got nil response", i)
		}
	}

	if cfg.ForceOSMemoryTrim {
		debug.FreeOSMemory()
	} else {
		runtime.GC()
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	heapGrowth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if heapGrowth > cfg.MaxGrowth {
		t.Fatalf("HeapAlloc grew too much after repeated parse: before=%d after=%d growth=%d iterations=%d fixture_bytes=%d", before.HeapAlloc, after.HeapAlloc, heapGrowth, cfg.Iters, len(fixture.BodyBytes))
	}
}

func hermeticResponsesLargeSSEFixture() sseBenchFixture {
	const repeats = 512
	dataLines := make([]string, 0, repeats*4+1)
	for i := 0; i < repeats; i++ {
		dataLines = append(dataLines,
			fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_%d","status":"in_progress"}}`, i),
			fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"delta":"chunk-%03d-%s"}`, i, strings.Repeat("x", 128)),
			fmt.Sprintf(`{"type":"response.output_text.delta","output_index":0,"delta":"tail-%03d-%s"}`, i, strings.Repeat("y", 128)),
			fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_%d","status":"completed"}}`, i),
		)
	}
	dataLines = append(dataLines, `{"type":"response.completed","response":{"id":"resp-hermetic","status":"completed","output":[],"usage":{"input_tokens":1024,"output_tokens":2048}}}`)
	body := buildResponsesSSEBody(dataLines)
	return sseBenchFixture{
		Name:       "hermetic_large",
		Provider:   "responses",
		Path:       "hermetic://responses_large",
		ChunkCount: len(dataLines),
		BodyBytes:  body,
	}
}

func buildResponsesSSEBody(dataLines []string) []byte {
	var b bytes.Buffer
	for _, d := range dataLines {
		b.WriteString("event: ")
		b.WriteString(responsesEventTypeForTest(d))
		b.WriteString("\ndata: ")
		b.WriteString(d)
		b.WriteString("\n\n")
	}
	return b.Bytes()
}

func responsesEventTypeForTest(data string) string {
	if idx := strings.Index(data, `"type":"`); idx >= 0 {
		start := idx + len(`"type":"`)
		if end := strings.Index(data[start:], `"`); end >= 0 {
			return data[start : start+end]
		}
	}
	return "unknown"
}
