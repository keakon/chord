package llm

import (
	"testing"
)

func BenchmarkResponsesLargeSSEMemory(b *testing.B) {
	corpora, err := loadSSEBenchCorpora()
	if err != nil {
		b.Fatalf("load corpora: %v", err)
	}
	corpus, ok := corpora["responses"]
	if !ok || len(corpus.Entries) == 0 {
		b.Skip("no responses corpus")
	}
	fixture := corpus.Entries[0]
	for _, entry := range corpus.Entries {
		if entry.ChunkCount > fixture.ChunkCount {
			fixture = entry
		}
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(fixture.BodyBytes)))
	for i := 0; i < b.N; i++ {
		resp, err := parseSSEBenchFixture(fixture)
		if err != nil {
			b.Fatalf("parse fixture %s: %v", fixture.Path, err)
		}
		if resp == nil {
			b.Fatalf("parse fixture %s returned nil response", fixture.Path)
		}
	}
}
