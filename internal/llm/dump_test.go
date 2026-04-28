package llm

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeDumpNamePart(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: "unknown"},
		{in: "responses", want: "responses"},
		{in: "provider/model-1", want: "provider_model-1"},
		{in: "../odd\\model name", want: "odd_model_name"},
		{in: "@xhigh", want: "@xhigh"},
	}

	for _, tt := range tests {
		if got := sanitizeDumpNamePart(tt.in); got != tt.want {
			t.Fatalf("sanitizeDumpNamePart(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDumpWriterWriteSanitizesFilename(t *testing.T) {
	dir := t.TempDir()
	writer := NewDumpWriter(dir)

	err := writer.Write(&LLMDump{
		Provider: "openai",
		Model:    "provider/model-1",
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].IsDir() {
		t.Fatalf("dump entry %q should be a file, not directory", entries[0].Name())
	}
	if strings.Contains(entries[0].Name(), "/") || strings.Contains(entries[0].Name(), `\`) {
		t.Fatalf("dump filename should be flat, got %q", entries[0].Name())
	}
	if !strings.HasSuffix(entries[0].Name(), "_openai_provider_model-1.json") {
		t.Fatalf("dump filename = %q, want sanitized model suffix", entries[0].Name())
	}
}

func TestDumpWriterWritePersistsReadableJSONRequestBody(t *testing.T) {
	dir := t.TempDir()
	writer := NewDumpWriter(dir)
	requestBody := []byte(`{"model":"sample/test-model","input":[{"role":"user","content":"hello"}]}`)

	err := writer.Write(&LLMDump{
		Provider:    "responses",
		Model:       "sample/test-model",
		RequestBody: requestBody,
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}

	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var dump struct {
		RequestBody json.RawMessage `json:"request_body"`
	}
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !json.Valid(dump.RequestBody) {
		t.Fatalf("request_body is not valid json: %s", dump.RequestBody)
	}
	var got any
	if err := json.Unmarshal(dump.RequestBody, &got); err != nil {
		t.Fatalf("json.Unmarshal(request_body) error = %v", err)
	}
	var want any
	if err := json.Unmarshal(requestBody, &want); err != nil {
		t.Fatalf("json.Unmarshal(want request body) error = %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(got) error = %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(want) error = %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("request_body = %s, want %s", gotJSON, wantJSON)
	}
}

func TestDumpWriterWriteDecodesCompressedRequestBody(t *testing.T) {
	dir := t.TempDir()
	writer := NewDumpWriter(dir)
	original := []byte(`{"model":"sample/test-model","input":[{"role":"user","content":"hello world hello world hello world hello world hello world hello world hello world hello world hello world hello world"}]}`)
	compressed, err := gzipCompress(original)
	if err != nil {
		t.Fatalf("gzipCompress() error = %v", err)
	}

	err = writer.Write(&LLMDump{
		Provider:    "responses",
		Model:       "sample/test-model",
		RequestBody: compressed,
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}

	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var dump struct {
		RequestBody json.RawMessage `json:"request_body"`
	}
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	var got any
	if err := json.Unmarshal(dump.RequestBody, &got); err != nil {
		t.Fatalf("json.Unmarshal(request_body) error = %v", err)
	}
	var want any
	if err := json.Unmarshal(original, &want); err != nil {
		t.Fatalf("json.Unmarshal(original) error = %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(got) error = %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal(want) error = %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("request_body = %s, want %s", gotJSON, wantJSON)
	}
}
