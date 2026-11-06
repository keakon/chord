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

func TestHTTPCallToolRequiresCompleteSSEEvent(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, tail string
		ok                 bool
	}{
		{"complete", "data: ", "\n\n", true},
		{"multiline", "data: \ndata: ", "\n\n", true},
		{"bom_cr", "\xef\xbb\xbf: ping\r\rdata:", "\r\r", true},
		{"unfinished frame", "data: ", "\n", false},
		{"unfinished line", "data: ", "", false},
		{"second data object", "data: ", "\ndata: {}\n\n", false},
		{"invalid second data", "data: ", "\ndata: {incomplete\n\n", false},
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
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, `%s{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"created"}]}}%s`, tc.prefix, req.ID, tc.tail)
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

func TestSSERPCResponseFraming(t *testing.T) {
	for _, sep := range []string{"\n", "\r\n", "\r"} {
		stream := strings.Join([]string{
			": ping", "", `data: {"jsonrpc":"2.0","id":2,"result":{}}`, "",
			"data:{", `data: "jsonrpc":"2.0","id":1,"result":{"value":"hello"}}`, "", "",
		}, sep)
		resp, err := readJSONRPCResponse(strings.NewReader(stream), "text/event-stream", 1)
		if err != nil || resp.ID != 1 || string(resp.Result) != `{"value":"hello"}` {
			t.Fatalf("separator=%q, response=%+v, err=%v", sep, resp, err)
		}
	}
}

func TestSSERPCResponsePreservesReadFailure(t *testing.T) {
	body := io.MultiReader(
		strings.NewReader(`data: {"jsonrpc":"2.0","id":1,"result":{}}`+"\n"),
		failingResponseReader{},
	)
	_, err := readJSONRPCResponse(body, "text/event-stream", 1)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error=%v, want unexpected EOF", err)
	}
}
