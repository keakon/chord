package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPCallToolRequiresCompleteJSONDocument(t *testing.T) {
	for _, tc := range []struct {
		name string
		tail string
		ok   bool
	}{
		{"end of body", "", true},
		{"trailing whitespace", " \n\t\r", true},
		{"trailing garbage", "garbage", false},
		{"second object", `{"unexpected":true}`, false},
		{"second null", "null", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req JSONRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"created"}]}}%s`, req.ID, tc.tail)
			}))
			defer server.Close()
			client := NewClientWithInfo("sample", NewHTTPTransport(HTTPTransportOptions{
				Endpoint: server.URL, Client: &http.Client{Timeout: 5 * time.Second},
			}), testClientInfo)
			text, _, err := client.CallTool(t.Context(), "create_record", json.RawMessage(`{}`))
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1", requests.Load())
			}
			if tc.ok {
				if err != nil || text != "created" {
					t.Fatalf("result = %q, error = %v", text, err)
				}
			} else {
				requireCallOutcome(t, err, callOutcomeUnknown)
			}
		})
	}
}

func TestHTTPJSONResponsePreservesTrailingReadError(t *testing.T) {
	body := io.MultiReader(
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`),
		failingResponseReader{},
	)
	_, err := readJSONRPCResponse(body, "application/json", 1)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("response error = %v, want unexpected EOF", err)
	}
}

type failingResponseReader struct{}

func (failingResponseReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
