package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
)

func outputSchemaTestClient(t *testing.T, schema string) (*Client, *fakeTransport) {
	t.Helper()
	tr := newFakeTransport()
	def := MCPToolDef{Name: "sample", OutputSchema: json.RawMessage(schema)}
	tr.onMethod("tools/list", toolsListResult{Tools: []MCPToolDef{def}})
	c := NewClientWithInfo("sample", tr, testClientInfo)
	if _, err := c.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c, tr
}

func TestClientOutputSchemaAdvisory(t *testing.T) {
	const schema = `{"type":"object","properties":{"count":{"type":"integer","enum":[3]}},"required":["count"],"additionalProperties":false}`
	for _, tc := range []struct {
		name, schema, result, want, diagnostic string
	}{
		{"valid", schema, `{"structuredContent":{"count":3}}`, `{"count":3}`, ""},
		{"type", schema, `{"structuredContent":{"count":"3"}}`, `{"count":"3"}`, "violates"},
		{"required", schema, `{"structuredContent":{}}`, `{}`, "required"},
		{"enum", schema, `{"structuredContent":{"count":4}}`, `{"count":4}`, "enum"},
		{"additional property", schema, `{"structuredContent":{"count":3,"extra":true}}`, `{"count":3,"extra":true}`, "additionalProperties"},
		{"no schema", "", `{"structuredContent":{"count":"3"}}`, `{"count":"3"}`, ""},
		{"missing structured", schema, `{"content":[{"type":"text","text":"Available result"}]}`, "Available result", "is missing"},
		{"null structured", schema, `{"structuredContent":null,"content":[{"type":"text","text":"Available result"}]}`, "null\nAvailable result", "type"},
		{"array structured", schema, `{"structuredContent":[],"content":[{"type":"text","text":"Available result"}]}`, "[]\nAvailable result", "type"},
		{"null schema", `null`, `{"structuredContent":{"count":3}}`, `{"count":3}`, "must be an object"},
		{"array schema", `[]`, `{"structuredContent":{}}`, `{}`, "must be an object"},
		{"boolean schema", `true`, `{"structuredContent":{}}`, `{}`, "must be an object"},
		{"invalid declaration", `{"type":"unknown"}`, `{"structuredContent":{}}`, `{}`, "invalid or unsupported"},
		{"external HTTP reference", `{"$ref":"https://example.invalid/schema.json"}`, `{"structuredContent":{}}`, `{}`, "external schema references are disabled"},
		{"external file reference", `{"$ref":"file:///nonexistent-output-schema.json"}`, `{"structuredContent":{}}`, `{}`, "external schema references are disabled"},
		{"exact integer", `{"properties":{"count":{"const":9007199254740993}}}`, `{"structuredContent":{"count":9007199254740993}}`, `{"count":9007199254740993}`, ""},
		{"adjacent integer", `{"properties":{"count":{"const":9007199254740993}}}`, `{"structuredContent":{"count":9007199254740992}}`, `{"count":9007199254740992}`, "const"},
		{"local reference", `{"$defs":{"count":{"type":"integer"}},"properties":{"count":{"$ref":"#/$defs/count"}}}`, `{"structuredContent":{"count":3}}`, `{"count":3}`, ""},
		{"local reference violation", `{"$defs":{"count":{"type":"integer"}},"properties":{"count":{"$ref":"#/$defs/count"}}}`, `{"structuredContent":{"count":"3"}}`, `{"count":"3"}`, "type"},
		{"embedded resource", `{"$defs":{"count":{"$id":"https://example.invalid/count.json","type":"integer"}},"properties":{"count":{"$ref":"https://example.invalid/count.json"}}}`, `{"structuredContent":{"count":3}}`, `{"count":3}`, ""},
		{"draft seven", `{"$schema":"http://json-schema.org/draft-07/schema#","properties":{"count":{"type":"integer"}}}`, `{"structuredContent":{"count":3}}`, `{"count":3}`, ""},
		{"combination", `{"oneOf":[{"required":["count"]},{"required":["label"]}]}`, `{"structuredContent":{"count":3,"label":"sample"}}`, `{"count":3,"label":"sample"}`, "oneOf"},
		{"default not inserted", `{"properties":{"count":{"default":3}},"required":["count"]}`, `{"structuredContent":{}}`, `{}`, "required"},
		{"format annotation", `{"properties":{"link":{"type":"string","format":"uri"}}}`, `{"structuredContent":{"link":"sample text"}}`, `{"link":"sample text"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, tr := outputSchemaTestClient(t, tc.schema)
			tr.onMethod("tools/call", json.RawMessage(tc.result))
			for range 2 {
				text, images, err := c.CallTool(t.Context(), "sample", json.RawMessage(`{}`))
				if err != nil || len(images) != 0 {
					t.Fatalf("images=%v err=%v", images, err)
				}
				if tc.diagnostic == "" {
					if text != tc.want {
						t.Fatalf("text=%q want=%q", text, tc.want)
					}
				} else if !strings.HasPrefix(text, outputSchemaWarning) || !strings.Contains(text, tc.diagnostic) || !strings.HasSuffix(text, "\n\n"+tc.want) || !strings.Contains(text, "do not retry unchanged") {
					t.Fatalf("result or diagnostic lost: %q", text)
				}
			}
			// One discovery and exactly two calls: validation must not retry or rediscover.
			if tr.requestCount() != 3 {
				t.Fatalf("requests=%d", tr.requestCount())
			}
		})
	}
}

func TestClientOutputSchemaPreservesErrors(t *testing.T) {
	for _, result := range []string{
		`{"isError":true,"content":[{"type":"text","text":"operation failed"}]}`,
		`{"content":[{}]}`,
		`not json`,
	} {
		t.Run(result, func(t *testing.T) {
			c, tr := outputSchemaTestClient(t, `{"required":["count"]}`)
			tr.responses["tools/call"] = json.RawMessage(result)
			text, images, err := c.CallTool(t.Context(), "sample", json.RawMessage(`{}`))
			if err == nil || text != "" || len(images) != 0 || strings.Contains(err.Error(), outputSchemaWarning) {
				t.Fatalf("text=%q images=%v err=%v", text, images, err)
			}
			if strings.Contains(result, "isError") && err.Error() != "mcp tool error: operation failed" {
				t.Fatalf("server error changed: %v", err)
			}
		})
	}
}

func TestClientOutputSchemaPreservesImagesAndResources(t *testing.T) {
	c, tr := outputSchemaTestClient(t, `{"type":"object","required":["count"]}`)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	tr.onMethod("tools/call", toolCallResult{StructuredContent: json.RawMessage(`[]`), Content: []toolCallContent{
		{Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(data.Bytes())},
		{Type: "resource", Resource: &toolCallResource{URI: "sample://result", Text: new("Available resource")}},
	}})
	text, images, err := c.CallTool(t.Context(), "sample", json.RawMessage(`{}`))
	if err != nil || len(images) != 1 || !bytes.Equal(images[0].Data, data.Bytes()) || !strings.HasPrefix(text, outputSchemaWarning) || !strings.HasSuffix(text, "Available resource") {
		t.Fatalf("text=%q images=%v err=%v", text, images, err)
	}
}

func TestClientOutputSchemaDirectoryRefresh(t *testing.T) {
	c, tr := outputSchemaTestClient(t, `{"required":["count"]}`)
	tr.onMethod("tools/call", toolCallResult{StructuredContent: json.RawMessage(`{}`)})
	call := func(wantWarning bool) {
		t.Helper()
		text, _, err := c.CallTool(t.Context(), "sample", json.RawMessage(`{}`))
		if err != nil || strings.HasPrefix(text, outputSchemaWarning) != wantWarning {
			t.Fatalf("text=%q err=%v wantWarning=%v", text, err, wantWarning)
		}
	}
	call(true)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ListTools(cancelled); err == nil {
		t.Fatal("cancelled discovery succeeded")
	}
	call(true)
	other := NewClientWithInfo("sample", tr, testClientInfo)
	text, _, err := other.CallTool(t.Context(), "sample", json.RawMessage(`{}`))
	if err != nil || text != `{}` {
		t.Fatalf("new client inherited validation: text=%q err=%v", text, err)
	}
	// The first page advertises a replacement; the next page fails.
	tr.onMethod("tools/list", json.RawMessage(`{"tools":[{"name":"sample","outputSchema":{}}],"nextCursor":"next"}`))
	if _, err := c.ListTools(t.Context()); err == nil {
		t.Fatal("expected failed discovery on repeated cursor")
	}
	call(true)
	for _, defs := range [][]MCPToolDef{
		{{Name: "sample", OutputSchema: json.RawMessage(`{}`)}},
		{{Name: "sample", OutputSchema: json.RawMessage(`{"required":["count"]}`)}},
		{{Name: "sample"}},
		{},
	} {
		tr.onMethod("tools/list", toolsListResult{Tools: defs})
		if _, err := c.ListTools(t.Context()); err != nil {
			t.Fatal(err)
		}
		call(len(defs) == 1 && bytes.Contains(defs[0].OutputSchema, []byte("required")))
	}
}

func TestClientOutputSchemaIndependentOfReturnedDefinitions(t *testing.T) {
	c, tr := outputSchemaTestClient(t, `{"required":["count"]}`)
	defs, err := c.ListTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Callers may retain or modify the returned declaration; compiled validation
	// must own its document rather than depend on those bytes.
	for i := range defs[0].OutputSchema {
		defs[0].OutputSchema[i] = ' '
	}
	tr.onMethod("tools/call", toolCallResult{StructuredContent: json.RawMessage(`{}`)})
	text, _, err := c.CallTool(t.Context(), "sample", json.RawMessage(`{}`))
	if err != nil || !strings.HasPrefix(text, outputSchemaWarning) || !strings.Contains(text, "required") {
		t.Fatalf("caller changed compiled validation: text=%q err=%v", text, err)
	}
}

func TestClientOutputSchemaInFlightDirectory(t *testing.T) {
	tr := &scriptedMCPTransport{fakeTransport: newFakeTransport()}
	started, release := make(chan struct{}), make(chan struct{})
	schema := `{"required":["count"]}`
	tr.send = func(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
		if req.Method == "tools/list" {
			return JSONRPCResponse{Result: json.RawMessage(`{"tools":[{"name":"sample","outputSchema":` + schema + `}]}`)}, nil
		}
		close(started)
		select {
		case <-release:
			return JSONRPCResponse{Result: json.RawMessage(`{"structuredContent":{}}`)}, nil
		case <-ctx.Done():
			return JSONRPCResponse{}, ctx.Err()
		}
	}
	c := NewClientWithInfo("sample", tr, testClientInfo)
	if _, err := c.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer close(release)
	result := make(chan string, 1)
	go func() {
		text, _, err := c.CallTool(t.Context(), "sample", json.RawMessage(`{}`))
		if err != nil {
			text = err.Error()
		}
		result <- text
	}()
	<-started
	schema = `{}`
	if _, err := c.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	if text := <-result; !strings.HasPrefix(text, outputSchemaWarning) {
		t.Fatalf("in-flight call used refreshed schema: %q", text)
	}
}

func TestClientOutputSchemaBoundedDiagnostic(t *testing.T) {
	key := strings.Repeat("sample/field~", 1000)
	keyJSON, _ := json.Marshal(key)
	c, tr := outputSchemaTestClient(t, fmt.Sprintf(`{"properties":{%s:{"type":"integer"}}}`, keyJSON))
	payload := fmt.Sprintf(`{%s:"unchanged"}`, keyJSON)
	tr.onMethod("tools/call", toolCallResult{StructuredContent: json.RawMessage(payload)})
	text, _, err := c.CallTool(t.Context(), "sample", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	warning, original, ok := strings.Cut(text, "\n\n")
	if !ok || !strings.HasPrefix(warning, outputSchemaWarning) || len(warning) > 600 || original != payload || !strings.Contains(warning, "~1") || !strings.Contains(warning, "~0") {
		t.Fatalf("warning=%q original preserved=%v", warning, original == payload)
	}
}
