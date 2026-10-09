package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestDumpRequestHeadersFiltersAndCopies(t *testing.T) {
	headers := http.Header{
		"content-type": {"application/json"}, "Session-Id": {"session-a"},
		"x-codex-installation-id": {"installation-a"}, "Authorization": {"Bearer secret-a"},
		"api-key": {"secret-b"}, "Cookie": {"secret-c"}, "X-Custom-Credential": {"secret-d"},
	}
	dir := t.TempDir()
	if err := NewDumpWriter(dir).Write(&LLMDump{Provider: "sample", Model: "test-model", RequestHeaders: headers}); err != nil {
		t.Fatal(err)
	}
	entries := waitForDumpEntries(t, dir, 1)
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var dump LLMDump
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatal(err)
	}
	if dump.RequestHeaders.Get(headerContentType) != "application/json" || dump.RequestHeaders.Get("Session-Id") != "session-a" {
		t.Fatalf("headers=%v", dump.RequestHeaders)
	}
	for _, name := range []string{"Authorization", "Api-Key", "Cookie"} {
		if dump.RequestHeaders.Get(name) != "[redacted]" {
			t.Fatalf("header %s was not redacted", name)
		}
	}
	if _, ok := dump.RequestHeaders["X-Custom-Credential"]; ok {
		t.Fatal("unknown header was persisted")
	}
	for _, secret := range []string{"secret-a", "secret-b", "secret-c", "secret-d"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("dump contains %s", secret)
		}
	}
	if headers.Get("Authorization") != "Bearer secret-a" {
		t.Fatal("source headers were mutated")
	}
	snapshot := dumpRequestHeaders(headers)
	headers["Session-Id"][0] = "session-b"
	if snapshot.Get("Session-Id") != "session-a" {
		t.Fatal("snapshot aliases source values")
	}
	if dumpRequestHeaders(nil) != nil {
		t.Fatal("empty headers should remain absent")
	}
}

func TestResponsesDumpCapturesFinalRequestHeaders(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var wireHeaders http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wireHeaders = r.Header.Clone()
				if status != http.StatusOK {
					w.WriteHeader(status)
					io.WriteString(w, `{"error":{"code":"invalid_responses_request","message":"request rejected"}}`)
					return
				}
				w.Header().Set(headerContentType, "text/event-stream")
				io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-a\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			cfg := NewProviderConfig("sample", config.ProviderConfig{
				Type: config.ProviderTypeResponses, APIURL: server.URL + "/v1/responses", Compress: config.RequestCompressionGzip,
				Compat: &config.ProviderCompatConfig{RequestOverrides: &config.RequestOverridesConfig{Headers: map[string]*string{"session-id": new("overridden-session"), "X-Custom-Credential": new("private-value")}}},
			}, []string{"test-key"})
			dir := t.TempDir()
			p := &ResponsesProvider{provider: cfg, client: server.Client()}
			p.SetDumpWriter(NewDumpWriter(dir))
			_, callErr := p.CompleteStream(context.Background(), "test-key", "test-model", "", []message.Message{{Role: message.RoleUser, Content: "hello"}}, nil, 128, RequestTuning{SessionKey: "session-a"}, func(message.StreamDelta) {})
			if (callErr != nil) != (status != http.StatusOK) {
				t.Fatalf("error=%v status=%d", callErr, status)
			}
			entries := waitForDumpEntries(t, dir, 1)
			// The async file writer can create the entry before completing its write.
			dump := waitForReadableHeaderDump(t, filepath.Join(dir, entries[0].Name()))
			for _, name := range []string{"Session-Id", headerContentType, headerContentEncoding, responsesClientMetadataInstallationID, responsesClientMetadataWindowID, responsesClientMetadataTurnMetadata} {
				if got, want := dump.RequestHeaders.Get(name), wireHeaders.Get(name); got != want || want == "" {
					t.Fatalf("header %s=%q, want %q", name, got, want)
				}
			}
			if dump.RequestHeaders.Get("Authorization") != "[redacted]" || dump.RequestHeaders.Get("X-Custom-Credential") != "" {
				t.Fatalf("unsafe dump headers=%v", dump.RequestHeaders)
			}
		})
	}
}

func waitForReadableHeaderDump(t *testing.T, path string) LLMDump {
	t.Helper()
	var dump LLMDump
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &dump) == nil {
			return dump
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("dump file did not become readable")
	return dump
}
