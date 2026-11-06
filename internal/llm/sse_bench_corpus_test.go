package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadSSEBenchCorpora(t *testing.T) {
	writeDump := func(t *testing.T, dir string, chunks []string) {
		t.Helper()
		raw, err := json.Marshal(sseBenchDumpMeta{Provider: "openai", SSEChunks: chunks})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sample.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("explicit directory", func(t *testing.T) {
		dir := t.TempDir()
		writeDump(t, dir, []string{
			`{"choices":[{"index":0,"delta":{"content":"sample"},"finish_reason":null}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			"[DONE]",
		})
		t.Setenv("CHORD_SSE_BENCH_DUMPS", dir)
		corpora, err := buildSSEBenchCorpora()
		if err != nil {
			t.Fatal(err)
		}
		entries := corpora["openai"].Entries
		if len(entries) != 1 || entries[0].ChunkCount != 3 {
			t.Fatalf("unexpected corpus: %+v", entries)
		}
	})
	t.Run("invalid dump fails", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "sample.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSSEBenchCorpora(dir); err == nil {
			t.Fatal("invalid dump was silently ignored")
		}
	})
	t.Run("failed replay fails", func(t *testing.T) {
		dir := t.TempDir()
		writeDump(t, dir, []string{`{"choices":`})
		if _, err := readSSEBenchCorpora(dir); err == nil {
			t.Fatal("failed replay was silently ignored")
		}
	})
	t.Run("empty directory fails", func(t *testing.T) {
		if _, err := readSSEBenchCorpora(t.TempDir()); err == nil {
			t.Fatal("empty explicit corpus was accepted")
		}
	})
}
