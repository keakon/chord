package mcp

import (
	"bytes"
	"context"
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
		{name: "bare control character", body: "{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"a\tb\"}]}}", want: callOutcomeUnknown},
		{name: "duplicate text rejected", body: `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"first","text":"last"}]}}`, want: callOutcomeUnknown},
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
					if strings.Contains(tc.name, "duplicate") {
						if errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("call settled by context timeout instead of parse error: %v", err)
						}
						// The http transport summarizes protocol failures; the
						// decode failure stays reachable through the cause chain.
						if !strings.Contains(flattenErr(err), "duplicate object member name") {
							t.Fatalf("error is not the parse error: %v", err)
						}
					}
				}
			})
		}
	}
}

// flattenErr renders the full error chain, including multi-unwrap nodes.
func flattenErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var multi interface{ Unwrap() []error }
	if errors.As(err, &multi) {
		for _, sub := range multi.Unwrap() {
			msg += " | " + flattenErr(sub)
		}
		return msg
	}
	if inner := errors.Unwrap(err); inner != nil {
		msg += " | " + flattenErr(inner)
	}
	return msg
}

// TestDecodeJSONRPCResponsePreservesID pins the ID-correlation contract: the
// lenient first pass extracts the ID even when the strict pass rejects the
// record, so transports can settle the matching caller with a parse error.
func TestDecodeJSONRPCResponsePreservesID(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantID  int
		wantErr string
	}{
		{name: "duplicate in result, id first", raw: `{"jsonrpc":"2.0","id":7,"result":{"a":1,"a":2}}`, wantID: 7, wantErr: "duplicate object member name"},
		{name: "duplicate in result, id last", raw: `{"result":{"a":1,"a":2},"id":7,"jsonrpc":"2.0"}`, wantID: 7, wantErr: "duplicate object member name"},
		{name: "duplicate in error body", raw: `{"jsonrpc":"2.0","id":3,"error":{"code":-1,"message":"m","data":{"d":1,"d":2}}}`, wantID: 3, wantErr: "duplicate object member name"},
		{name: "duplicate error code", raw: `{"jsonrpc":"2.0","id":3,"error":{"code":-1,"code":-2,"message":"m"}}`, wantID: 3, wantErr: "duplicate object member name"},
		{name: "duplicate id resolves last-wins", raw: `{"id":3,"id":4,"jsonrpc":"2.0","result":{}}`, wantID: 4, wantErr: "duplicate object member name"},
		{name: "invalid utf8 in result", raw: "{\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{\"a\":\"\xff\"}}", wantID: 9, wantErr: "invalid UTF-8"},
		{name: "invalid utf8 after id in garbage envelope", raw: "{\"jsonrpc\":\"2.0\",\"id\":9,", wantID: 9, wantErr: ""},
		{name: "unparseable envelope has no id", raw: `not json`, wantID: 0, wantErr: "decode JSON-RPC response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := decodeJSONRPCResponse([]byte(tc.raw))
			if resp.ID != tc.wantID {
				t.Fatalf("ID = %d, want %d", resp.ID, tc.wantID)
			}
			if err == nil {
				t.Fatal("expected strict decode failure")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
	resp, err := decodeJSONRPCResponse([]byte(`{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	if err != nil || resp.ID != 7 || resp.Error != nil || len(resp.Result) == 0 {
		t.Fatalf("valid response changed: resp=%+v err=%v", resp, err)
	}
}

// TestStdioSettlesParseErrorWithoutTimeout replays the reviewed regression: a
// response whose result carries duplicate members must settle the pending call
// with the parse error, never by waiting out the context.
func TestStdioSettlesParseErrorWithoutTimeout(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"first","text":"last"}]}}`)
	reader, writer := io.Pipe()
	input := &scriptedWriteCloser{onWrite: func([]byte) {
		if _, err := writer.Write(append(bytes.Clone(raw), '\n')); err != nil {
			t.Errorf("write response: %v", err)
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
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	client := NewClientWithInfo("sample", tr, testClientInfo)
	_, _, err := client.CallTool(ctx, "sample_tool", json.RawMessage(`{}`))
	requireCallOutcome(t, err, callOutcomeUnknown)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("invalid receipt was discarded; only context timeout settled the call: %v", err)
	}
	if !strings.Contains(err.Error(), "duplicate object member name") {
		t.Fatalf("call did not settle with the parse error: %v", err)
	}
}
