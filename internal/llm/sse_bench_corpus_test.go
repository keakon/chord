package llm

import (
	"bytes"
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

func TestReconstructSSEBenchmarkBodyPreservesEOF(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"anthropic", "openai", "responses", "responses_ws"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			chunks := []string{`{"type":"sample"}`, `{"type":"partial`}
			want := "data: " + chunks[0] + "\n\ndata: " + chunks[1]
			if provider == "anthropic" {
				chunks = []string{"sample: " + chunks[0], "partial: " + chunks[1]}
				want = "event: sample\ndata: " + `{"type":"sample"}` + "\n\nevent: partial\ndata: " + `{"type":"partial`
			}
			body, err := reconstructSSEBenchmarkBody(provider, chunks)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != want {
				t.Fatalf("body = %q, want %q", body, want)
			}
		})
	}
}

func TestSSEBenchReplayRecoversIncompleteFinalEvent(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_sample","call_id":"call_sample","name":"sample"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"value\":1}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_sample","call_id":"call_sample","name":"sample","status":"completed"}}`,
		`{"type":"response.completed","response":{"id":"response_sample","output":[`,
	}
	body, err := reconstructSSEBenchmarkBody("responses", chunks)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(chunks[len(chunks)-1])) {
		t.Fatal("incomplete final event was discarded")
	}
	resp, err := parseSSEBenchFixture(sseBenchFixture{Provider: "responses", BodyBytes: body})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.StopReason != "tool_calls" || len(resp.ToolCalls) != 1 {
		t.Fatalf("response = %+v, want the completed tool call", resp)
	}
	call := resp.ToolCalls[0]
	if call.ID != "call_sample" || call.Name != "sample" || string(call.Args) != `{"value":1}` {
		t.Fatalf("tool call changed: %+v", call)
	}
}

func TestSSEBenchReplayRejectsIncompleteEventWithoutResponse(t *testing.T) {
	t.Parallel()
	body, err := reconstructSSEBenchmarkBody("responses", []string{`{"type":"response.completed","response":`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseSSEBenchFixture(sseBenchFixture{Provider: "responses", BodyBytes: body}); err == nil {
		t.Fatal("incomplete event without a recoverable response was accepted")
	}
}
