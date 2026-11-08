package mcp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestClientCallToolLenientArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args json.RawMessage
		want string
	}{
		{"duplicate members", json.RawMessage(`{"note":"first","note":"last"}`), "last"},
		{"invalid UTF-8", json.RawMessage("{\"note\":\"x\xffy\"}"), "x\ufffdy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFakeTransport()
			ft.onMethod("tools/call", toolCallResult{Content: []toolCallContent{}})
			client := NewClientWithInfo("sample", ft, testClientInfo)
			if _, _, err := client.CallTool(t.Context(), "sample", tc.args); err != nil {
				t.Fatal(err)
			}
			if ft.requestCount() != 1 {
				t.Fatalf("requests = %d, want 1", ft.requestCount())
			}
			args := ft.requests[0].Params.(map[string]any)["arguments"].(map[string]any)
			if args["note"] != tc.want {
				t.Fatalf("arguments = %+v, want note=%q", args, tc.want)
			}
		})
	}
}

func TestClientCallToolAcceptsSchemaValidatedArgs(t *testing.T) {
	tool := &MCPTool{
		registeredName: RegisteredMCPToolName("sample", "sample"),
		remoteName:     "sample",
		schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"note": map[string]any{"type": "string"}},
			"required":   []string{"note"},
		},
	}
	args := json.RawMessage("{\"note\":\"x\xffy\"}")
	sanitized, ignored, invalid, err := tools.SanitizeUnknownArgsWithDiagnostics(tool, args)
	if err != nil || len(ignored) != 0 || len(invalid) != 0 {
		t.Fatalf("schema validation: err=%v, ignored=%v, invalid=%v", err, ignored, invalid)
	}
	if !bytes.Equal(sanitized, args) {
		t.Fatalf("schema validation unexpectedly re-encoded the arguments: %q", sanitized)
	}
	ft := newFakeTransport()
	ft.onMethod("tools/call", toolCallResult{Content: []toolCallContent{}})
	client := NewClientWithInfo("sample", ft, testClientInfo)
	if _, _, err := client.CallTool(t.Context(), "sample", sanitized); err != nil {
		t.Fatalf("schema-validated arguments were rejected: %v", err)
	}
	if ft.requestCount() != 1 {
		t.Fatalf("requests = %d, want 1", ft.requestCount())
	}
	effective := ft.requests[0].Params.(map[string]any)["arguments"].(map[string]any)
	if effective["note"] != "x\ufffdy" {
		t.Fatalf("effective arguments = %+v", effective)
	}
}

func TestClientCallToolRejectsMalformedArgs(t *testing.T) {
	for _, args := range []json.RawMessage{json.RawMessage(`{`), json.RawMessage(`{} {}`), json.RawMessage(`[]`), json.RawMessage(`"sample"`)} {
		t.Run(string(args), func(t *testing.T) {
			ft := newFakeTransport()
			client := NewClientWithInfo("sample", ft, testClientInfo)
			_, _, err := client.CallTool(t.Context(), "sample", args)
			requireCallOutcome(t, err, callNotSent)
			if ft.requestCount() != 0 {
				t.Fatalf("malformed arguments reached the transport: %d requests", ft.requestCount())
			}
		})
	}
}
