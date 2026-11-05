package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallToolReceiptValidationAcrossTransports(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    callOutcome
		success bool
	}{
		{name: "missing content", body: `{"jsonrpc":"2.0","id":%d,"result":{}}`, want: callOutcomeUnknown},
		{name: "null content", body: `{"jsonrpc":"2.0","id":%d,"result":{"content":null}}`, want: callOutcomeUnknown},
		{name: "null error", body: `{"jsonrpc":"2.0","id":%d,"error":null}`, want: callOutcomeUnknown},
		{name: "empty error", body: `{"jsonrpc":"2.0","id":%d,"error":{}}`, want: callOutcomeUnknown},
		{name: "missing error code", body: `{"jsonrpc":"2.0","id":%d,"error":{"message":"failed"}}`, want: callOutcomeUnknown},
		{name: "missing error message", body: `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000}}`, want: callOutcomeUnknown},
		{name: "both result and error", body: `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"failed"},"result":{"content":[]}}`, want: callOutcomeUnknown},
		{name: "null error with result", body: `{"jsonrpc":"2.0","id":%d,"error":null,"result":{"content":[]}}`, want: callOutcomeUnknown},
		{name: "neither result nor error", body: `{"jsonrpc":"2.0","id":%d}`, want: callOutcomeUnknown},
		{name: "missing protocol version", body: `{"id":%d,"result":{"content":[]}}`, want: callOutcomeUnknown},
		{name: "invalid content in reported error", body: `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text"}],"isError":true}}`, want: callOutcomeUnknown},
		{name: "empty content", body: `{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, success: true},
		{name: "structured content only", body: `{"jsonrpc":"2.0","id":%d,"result":{"structuredContent":{"created":true}}}`, success: true},
		{name: "null structured without content", body: `{"jsonrpc":"2.0","id":%d,"result":{"structuredContent":null}}`, want: callOutcomeUnknown},
		{name: "bad image with valid text", body: `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"created"},{"type":"image","data":"?","mimeType":"image/png"}]}}`, success: true},
		{name: "reported tool error", body: `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"rejected"}],"isError":true}}`, want: callReportedError},
		{name: "reported protocol error", body: `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"failed","data":{"reason":"sample"}}}`, want: callReportedError},
	}
	for _, protocol := range []string{"http", "sse", "stdio"} {
		for _, tc := range cases {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				var transport Transport
				reply := func(raw []byte) string {
					var req JSONRPCRequest
					if err := json.Unmarshal(raw, &req); err != nil {
						t.Errorf("decode request: %v", err)
					}
					requests.Add(1)
					return fmt.Sprintf(tc.body, req.ID)
				}
				if protocol == "stdio" {
					reader, writer := io.Pipe()
					input := &scriptedWriteCloser{onWrite: func(raw []byte) {
						if _, err := io.WriteString(writer, reply(raw)+"\n"); err != nil {
							t.Errorf("write reply: %v", err)
						}
					}}
					tr := newScriptedTransport(input)
					tr.stdout = reader
					go tr.readLoop()
					t.Cleanup(func() {
						writer.Close()
						reader.Close()
						select {
						case <-tr.done:
						case <-time.After(5 * time.Second):
							t.Error("stdio reader did not stop")
						}
					})
					transport = tr
				} else {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						raw, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						body := reply(raw)
						if protocol == "sse" {
							w.Header().Set("Content-Type", "text/event-stream")
							body = "data: " + body + "\n\n"
						} else {
							w.Header().Set("Content-Type", "application/json")
						}
						io.WriteString(w, body)
					}))
					t.Cleanup(server.Close)
					transport = NewHTTPTransport(HTTPTransportOptions{Endpoint: server.URL})
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				client := NewClientWithInfo("sample", transport, testClientInfo)
				text, _, err := client.CallTool(ctx, "create_record", json.RawMessage(`{}`))
				if requests.Load() != 1 {
					t.Fatalf("requests=%d, want 1", requests.Load())
				}
				if tc.success {
					if err != nil {
						t.Fatalf("valid receipt failed: %v", err)
					}
					if tc.name == "bad image with valid text" && !strings.Contains(text, "created") {
						t.Fatalf("valid text lost: %q", text)
					}
				} else {
					requireCallOutcome(t, err, tc.want)
				}
			})
		}
	}
}
