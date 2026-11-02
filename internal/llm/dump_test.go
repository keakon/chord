package llm

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	if !strings.HasPrefix(entries[0].Name(), "0001_") {
		t.Fatalf("first dump filename = %q, want 0001 prefix", entries[0].Name())
	}
}

func TestDumpWriterSetDirResetsSequenceForNewSessionDir(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := filepath.Join(t.TempDir(), "dumps", "llm")
	writer := NewDumpWriter(firstDir)

	if err := writer.Write(&LLMDump{Provider: "openai", Model: "gpt-5.5"}); err != nil {
		t.Fatalf("Write(firstDir #1) error = %v", err)
	}
	if err := writer.Write(&LLMDump{Provider: "openai", Model: "gpt-5.5"}); err != nil {
		t.Fatalf("Write(firstDir #2) error = %v", err)
	}

	writer.SetDir(secondDir)
	if err := writer.Write(&LLMDump{Provider: "openai", Model: "gpt-5.5"}); err != nil {
		t.Fatalf("Write(secondDir #1) error = %v", err)
	}

	entries := waitForDumpEntries(t, secondDir, 1)
	if got := entries[0].Name(); !strings.HasPrefix(got, "0001_") {
		t.Fatalf("first dump in new dir = %q, want 0001 prefix", got)
	}
}

func TestDumpWriterSetDirContinuesExistingSessionSequence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0042_20260708_230000_responses_gpt-5.5.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed existing dump: %v", err)
	}
	writer := NewDumpWriter(dir)

	if err := writer.Write(&LLMDump{Provider: "responses", Model: "gpt-5.5"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	entries := waitForDumpEntries(t, dir, 2)
	var found bool
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "0043_") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected resumed session dump with 0043 prefix, got %#v", dumpEntryNames(entries))
	}
}

func TestDumpWriterSetDirKeepsReservedSequence(t *testing.T) {
	dir := t.TempDir()
	otherDir := t.TempDir()
	writer := NewDumpWriter(dir)
	if err := writer.Write(&LLMDump{Provider: "responses", Model: "gpt-5.5"}); err != nil {
		t.Fatalf("Write(seed) error = %v", err)
	}

	pendingDir, pending, err := writer.reserveSeq()
	if err != nil {
		t.Fatalf("reserveSeq(pending) error = %v", err)
	}
	defer writer.releaseSeq(pendingDir)

	writer.SetDir(otherDir)
	writer.SetDir(dir)

	nextDir, next, err := writer.reserveSeq()
	if err != nil {
		t.Fatalf("reserveSeq(next) error = %v", err)
	}
	defer writer.releaseSeq(nextDir)
	if next <= pending {
		t.Fatalf("reserved sequence after directory round trip = %d, want > in-flight %d", next, pending)
	}
}

func TestDumpWriterRetiresInactiveDirState(t *testing.T) {
	t.Run("release after switch", func(t *testing.T) {
		dir := t.TempDir()
		writer := NewDumpWriter(dir)
		reservedDir, _, err := writer.reserveSeq()
		if err != nil {
			t.Fatalf("reserveSeq() error = %v", err)
		}
		writer.SetDir(t.TempDir())
		writer.releaseSeq(reservedDir)
		if _, ok := writer.dirs[dir]; ok {
			t.Fatal("inactive directory state was not retired after the write completed")
		}
	})

	t.Run("switch after release", func(t *testing.T) {
		dir := t.TempDir()
		writer := NewDumpWriter(dir)
		reservedDir, _, err := writer.reserveSeq()
		if err != nil {
			t.Fatalf("reserveSeq() error = %v", err)
		}
		writer.releaseSeq(reservedDir)
		writer.SetDir(t.TempDir())
		if _, ok := writer.dirs[dir]; ok {
			t.Fatal("idle directory state was not retired on switch")
		}
	})
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

func TestDumpWriterWritePersistsRecoveryMetadata(t *testing.T) {
	dir := t.TempDir()
	writer := NewDumpWriter(dir)

	err := writer.Write(&LLMDump{
		Provider:    "responses",
		Model:       "gpt-5.5",
		RequestBody: json.RawMessage(`{"input":"hello"}`),
		Recovery: &DumpRecovery{
			Reason: "context_length_exceeded",
			Stage:  "sse_parse",
			Action: "return_error",
			Code:   "context_length_exceeded",
		},
		Error: "context too long",
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
		Recovery *DumpRecovery `json:"recovery"`
	}
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if dump.Recovery == nil {
		t.Fatal("recovery = nil, want populated recovery metadata")
	}
	if dump.Recovery.Reason != "context_length_exceeded" || dump.Recovery.Stage != "sse_parse" || dump.Recovery.Action != "return_error" || dump.Recovery.Code != "context_length_exceeded" {
		t.Fatalf("unexpected recovery metadata: %+v", dump.Recovery)
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
func TestNormalizeDumpRequestBodyReusesValidJSON(t *testing.T) {
	body := json.RawMessage(`{"input":"hello"}`)
	got, err := normalizeDumpRequestBody(body)
	if err != nil {
		t.Fatalf("normalizeDumpRequestBody() error = %v", err)
	}
	if len(got) != len(body) || &got[0] != &body[0] {
		t.Fatal("valid JSON should be returned without copying")
	}
}

func TestDumpWriterWritePreservesEmptyFieldShape(t *testing.T) {
	dir := t.TempDir()
	if err := NewDumpWriter(dir).Write(&LLMDump{Provider: "sample", Model: "test-model"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	entries := waitForDumpEntries(t, dir, 1)
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := string(fields["request_body"]); got != "null" {
		t.Fatalf("request_body = %s, want null", got)
	}
	if got := string(fields["sse_chunks"]); got != "null" {
		t.Fatalf("sse_chunks = %s, want null", got)
	}
	if got := string(fields["duration_ms"]); got != "0" {
		t.Fatalf("duration_ms = %s, want 0", got)
	}
	for _, name := range []string{"request_headers", "http_status", "http_headers", "http_body", "response", "recovery", "error"} {
		if _, ok := fields[name]; ok {
			t.Fatalf("empty field %q should be omitted", name)
		}
	}
}

func TestDumpWriterWritePersistsHTTPErrorMetadata(t *testing.T) {
	dir := t.TempDir()
	writer := NewDumpWriter(dir)

	err := writer.Write(&LLMDump{
		Provider:    "responses",
		Model:       "gpt-5.5",
		RequestBody: json.RawMessage(`{"input":"hello"}`),
		HTTPStatus:  http.StatusTooManyRequests,
		HTTPHeaders: http.Header{"Retry-After": []string{"120"}, "X-Test-Header": []string{"abc"}},
		HTTPBody:    `{"error":{"message":"rate limited"}}`,
		Error:       "API error 429: rate limited",
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	entries := waitForDumpEntries(t, dir, 1)
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var dump struct {
		HTTPStatus  int         `json:"http_status"`
		HTTPHeaders http.Header `json:"http_headers"`
		HTTPBody    string      `json:"http_body"`
		Error       string      `json:"error"`
	}
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if dump.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("http_status = %d, want %d", dump.HTTPStatus, http.StatusTooManyRequests)
	}
	if dump.HTTPBody != `{"error":{"message":"rate limited"}}` {
		t.Fatalf("http_body = %q, want raw body", dump.HTTPBody)
	}
	if got := dump.HTTPHeaders.Get("Retry-After"); got != "120" {
		t.Fatalf("retry-after = %q, want 120", got)
	}
	if got := dump.HTTPHeaders.Get("X-Test-Header"); got != "abc" {
		t.Fatalf("x-test-header = %q, want abc", got)
	}
	if dump.Error != "API error 429: rate limited" {
		t.Fatalf("error = %q, want API error 429: rate limited", dump.Error)
	}
}

func TestDumpWriterWriteStreamsCompletePayload(t *testing.T) {
	dir := t.TempDir()
	writer := NewDumpWriter(dir)
	want := &LLMDump{
		Timestamp:      "2026-11-02T12:34:56Z",
		Provider:       "responses",
		Model:          "sample/test-model",
		RequestBody:    json.RawMessage(`{"input":[{"role":"user","content":"hello"}]}`),
		RequestHeaders: http.Header{headerContentType: {"application/json"}},
		HTTPStatus:     http.StatusOK,
		HTTPHeaders:    http.Header{"X-Test-Header": {"value"}},
		HTTPBody:       `{"raw":true}`,
		SSEChunks: []string{
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			"quoted \"value\" and\nnewline",
			`unicode: 中文`,
		},
		Response: &DumpResponse{
			Content:    "done",
			ToolCalls:  []DumpToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)}},
			StopReason: "stop",
			Usage:      DumpTokenUsage{InputTokens: 10, OutputTokens: 2},
		},
		Recovery:   &DumpRecovery{Reason: "context_length_exceeded", Stage: "sse_parse", Action: "return_error", Code: "context_length_exceeded"},
		Error:      "sample error",
		DurationMS: 123,
	}

	if err := writer.Write(want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	entries := waitForDumpEntries(t, dir, 1)
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var got LLMDump
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(&got, want) {
		t.Fatalf("dump mismatch:\n got: %#v\nwant: %#v", &got, want)
	}
}

func TestDumpWriterWriteRemovesTempFileOnEncodingError(t *testing.T) {
	dir := t.TempDir()
	writer := NewDumpWriter(dir)

	err := writer.Write(&LLMDump{
		Provider: "responses",
		Model:    "sample/test-model",
		Response: &DumpResponse{
			ToolCalls: []DumpToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`not-json`)}},
		},
	})
	if err == nil {
		t.Fatal("Write() error = nil, want an encoding error")
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir() error = %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("dump dir entries = %v, want none after a failed write", dumpEntryNames(entries))
	}
}

func waitForDumpEntries(t *testing.T, dir string, want int) []os.DirEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err == nil {
			entries = visibleDumpEntries(entries)
			if len(entries) >= want {
				return entries
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	entries = visibleDumpEntries(entries)
	t.Fatalf("len(entries) = %d, want at least %d", len(entries), want)
	return entries
}

func visibleDumpEntries(entries []os.DirEntry) []os.DirEntry {
	visible := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		visible = append(visible, entry)
	}
	return visible
}

func dumpEntryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
