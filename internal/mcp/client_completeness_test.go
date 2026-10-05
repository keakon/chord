package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// Keep the production JSON-RPC entry point while scripting each page locally.
type scriptedMCPTransport struct {
	*fakeTransport
	send func(context.Context, JSONRPCRequest) (JSONRPCResponse, error)
}

func (s *scriptedMCPTransport) Send(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
	return s.send(ctx, req)
}

func TestClientListToolsPagination(t *testing.T) {
	for _, end := range []string{``, `,"nextCursor":null`} {
		t.Run(end, func(t *testing.T) {
			calls := 0
			transport := &scriptedMCPTransport{fakeTransport: newFakeTransport()}
			transport.send = func(_ context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
				calls++
				params := req.Params.(map[string]any)
				if req.Method != "tools/list" || req.ID != calls {
					t.Fatalf("unexpected request: %+v", req)
				}
				var result string
				switch calls {
				case 1:
					if _, ok := params["cursor"]; ok {
						t.Fatal("first page has cursor")
					}
					result = `{"tools":[{"name":"first"}],"nextCursor":" opaque cursor "}`
				case 2:
					if params["cursor"] != " opaque cursor " {
						t.Fatalf("cursor = %v", params)
					}
					result = `{"tools":[],"nextCursor":""}`
				case 3:
					if cursor, exists := params["cursor"]; !exists || cursor != "" {
						t.Fatalf("cursor = %v", params)
					}
					result = `{"tools":[{"name":"second"}]` + end + `}`
				default:
					t.Fatal("unexpected extra page")
				}
				return JSONRPCResponse{Result: json.RawMessage(result)}, nil
			}
			defs, err := NewClientWithInfo("sample", transport, testClientInfo).ListTools(t.Context())
			if err != nil || len(defs) != 2 || calls != 3 || defs[1].Name != "second" {
				t.Fatalf("defs=%v calls=%d err=%v", defs, calls, err)
			}
		})
	}
}

func TestClientListToolsRejectsIncompleteDirectory(t *testing.T) {
	for _, tc := range []struct{ name, first, second, want string }{
		{"cycle", `{"tools":[],"nextCursor":"a"}`, `{"tools":[],"nextCursor":"a"}`, "repeated cursor"},
		{"empty cursor cycle", `{"tools":[],"nextCursor":""}`, `{"tools":[],"nextCursor":""}`, "repeated cursor"},
		{"duplicate", `{"tools":[{"name":"same"}],"nextCursor":"a"}`, `{"tools":[{"name":"same","description":"different"}]}`, "duplicate tool name"},
		{"bad cursor", `{"tools":[],"nextCursor":3}`, `{}`, "decode"},
		{"missing tools", `{}`, `{}`, "tools array"},
		{"null tools", `{"tools":null}`, `{}`, "tools array"},
		{"bad page", `{"tools":[{"name":"first"}],"nextCursor":"a"}`, `not json`, "decode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			tr := &scriptedMCPTransport{fakeTransport: newFakeTransport()}
			tr.send = func(context.Context, JSONRPCRequest) (JSONRPCResponse, error) {
				calls++
				result := tc.first
				if calls > 1 {
					result = tc.second
				}
				if calls > 2 {
					t.Fatal("did not stop on invalid page")
				}
				return JSONRPCResponse{Result: json.RawMessage(result)}, nil
			}
			defs, err := NewClientWithInfo("sample", tr, testClientInfo).ListTools(t.Context())
			if defs != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("defs=%v err=%v, want %s", defs, err, tc.want)
			}
		})
	}
}

func TestDiscoveryPaginationCacheAndAllowlist(t *testing.T) {
	tr := &scriptedMCPTransport{fakeTransport: newFakeTransport()}
	phase, calls := 0, 0
	failure := errors.New("page unavailable")
	tr.send = func(context.Context, JSONRPCRequest) (JSONRPCResponse, error) {
		calls++
		if phase == 1 && calls == 2 {
			return JSONRPCResponse{}, failure
		}
		result := `{"tools":[{"name":"ignored"}],"nextCursor":"a"}`
		if phase == 1 {
			result = `{"tools":[{"name":"replacement"}],"nextCursor":"a"}`
		}
		if calls == 2 {
			result = `{"tools":[{"name":"kept"}]}`
		}
		return JSONRPCResponse{Result: json.RawMessage(result)}, nil
	}
	mgr := NewPendingManagerWithClientInfo([]ServerConfig{{Name: "sample", AllowedTools: []string{"kept", "replacement"}}}, testClientInfo)
	mgr.clients["sample"] = NewClientWithInfo("sample", tr, testClientInfo)
	for phase = range 2 {
		calls = 0
		discovered, err := DiscoverAllTools(t.Context(), mgr)
		cache := mgr.CachedToolDefs("sample")
		if err != nil || len(discovered) != 1 || discovered[0].Name() != RegisteredMCPToolName("sample", "kept") || len(cache) != 1 || cache[0].Name != "kept" {
			t.Fatalf("phase=%d discovered=%v cache=%v err=%v", phase, discovered, cache, err)
		}
	}
}

func TestClientCallToolContentNormalization(t *testing.T) {
	for _, tc := range []struct{ name, result, want, wantErr string }{
		{"structured", `{"content":[],"structuredContent":{"count":9007199254740993}}`, `{"count":9007199254740993}`, ""},
		{"no content", `{"structuredContent":{"count":3}}`, `{"count":3}`, ""},
		{"structured and summary", `{"content":[{"type":"text","text":"summary"}],"structuredContent":{"count":3}}`, "{\"count\":3}\nsummary", ""},
		{"structured and empty text", `{"content":[{"type":"text","text":""}],"structuredContent":{"count":3}}`, `{"count":3}`, ""},
		{"no duplication", `{"content":[{"type":"text","text":"{ \"count\": 3 }"}],"structuredContent":{"count":3}}`, `{ "count": 3 }`, ""},
		{"different JSON text", `{"content":[{"type":"text","text":"{\"count\":2}"}],"structuredContent":{"count":3}}`, "{\"count\":3}\n{\"count\":2}", ""},
		{"resource text", `{"content":[{"type":"resource","resource":{"uri":"sample://item","text":"value"}}]}`, "value", ""},
		{"resource empty text", `{"content":[{"type":"resource","resource":{"uri":"sample://item","text":""}}]}`, "", ""},
		{"link", `{"content":[{"type":"resource_link","name":"item","uri":"sample://item"}]}`, "item: sample://item", ""},
		{"audio", `{"content":[{"type":"audio","mimeType":"audio/wav","data":"AAAA"}]}`, "[audio audio/wav omitted]", ""},
		{"blob", `{"content":[{"type":"resource","resource":{"uri":"sample://item","mimeType":"application/pdf","blob":"AAAA"}}]}`, "[binary resource sample://item (application/pdf) omitted]", ""},
		{"unknown", `{"content":[{"type":"sample_future"}]}`, "[unsupported MCP content sample_future]", ""},
		{"bad image", `{"content":[{"type":"text","text":"ok"},{"type":"image","data":"!!"}]}`, "ok\n1 image attachment(s) in this result could not be read", ""},
		{"empty image", `{"content":[{"type":"image"}]}`, "1 image attachment(s) in this result could not be read", ""},
		{"error structured", `{"content":[],"structuredContent":{"reason":"failed"},"isError":true}`, "", `mcp tool error: {"reason":"failed"}`},
		{"error resource", `{"content":[{"type":"resource","resource":{"text":"failed","uri":"sample://item"}}],"isError":true}`, "", "mcp tool error: failed"},
		{"empty error", `{"content":[],"isError":true}`, "", "mcp tool error: server returned isError without details"},
		{"array structured", `{"content":[],"structuredContent":[1]}`, "[1]", ""},
		{"null structured", `{"content":[],"structuredContent":null}`, "null", ""},
		{"missing resource", `{"content":[{"type":"resource"}]}`, "", "missing embedded resource"},
		{"missing resource uri", `{"content":[{"type":"resource","resource":{"text":"value"}}]}`, "", "requires uri"},
		{"missing link uri", `{"content":[{"type":"resource_link","name":"item"}]}`, "", "requires name and uri"},
		{"missing link name", `{"content":[{"type":"resource_link","uri":"sample://item"}]}`, "", "requires name and uri"},
		{"unknown blob mime", `{"content":[{"type":"resource","resource":{"uri":"sample://item","blob":"AAAA"}}]}`, "[binary resource sample://item (unknown type) omitted]", ""},
		{"malformed resource", `{"content":[{"type":"resource","resource":{"uri":"sample://item","text":1}}]}`, "", "decode"},
		{"error image", `{"content":[{"type":"image","data":"!!"}],"isError":true}`, "", "mcp tool error: [image omitted from failed tool result]"},
		{"missing resource body", `{"content":[{"type":"resource","resource":{"uri":"sample://item"}}]}`, "", "text or blob"},
		{"missing type", `{"content":[{}]}`, "", "missing content type"},
		{"null result", `null`, "", "result must be an object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFakeTransport()
			ft.responses["tools/call"] = json.RawMessage(tc.result)
			client := NewClientWithInfo("sample", ft, testClientInfo)
			text, images, err := client.CallTool(t.Context(), "sample_tool", json.RawMessage(`{}`))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || text != "" || len(images) != 0 {
					t.Fatalf("text=%q images=%v err=%v", text, images, err)
				}
				return
			}
			if err != nil || !strings.HasPrefix(text, tc.want) || len(images) != 0 {
				t.Fatalf("text=%q images=%v err=%v", text, images, err)
			}
			if tc.name != "bad image" && tc.name != "empty image" && text != tc.want {
				t.Fatalf("text=%q want=%q", text, tc.want)
			}
		})
	}
}

func TestMCPToolEmbeddedImageAndStructuredArtifact(t *testing.T) {
	ft := newFakeTransport()
	client := NewClientWithInfo("sample", ft, testClientInfo)
	wrapped := wrapToolDefs("sample", []MCPToolDef{{Name: "sample_tool"}}, func(_, remote string) *ExecutionHandle {
		return &ExecutionHandle{client: client, remoteName: remote}
	})[0]
	image := base64.StdEncoding.EncodeToString(encodeToolPNG(t))
	ft.responses["tools/call"] = json.RawMessage(fmt.Sprintf(`{"content":[{"type":"resource","resource":{"uri":"sample://image","mimeType":"image/png","blob":%q}},{"type":"text","text":"caption"}],"structuredContent":{"count":3}}`, image))
	collector := &tools.ImageCollector{}
	text, err := wrapped.Execute(tools.WithImageSink(t.Context(), collector), json.RawMessage(`{}`))
	parts := collector.Drain()
	if err != nil || text != "{\"count\":3}\ncaption" || len(parts) != 1 || parts[0].MimeType != "image/png" {
		t.Fatalf("text=%q images=%v err=%v", text, parts, err)
	}
	ft.responses["tools/call"] = json.RawMessage(fmt.Sprintf(`{"content":[{"type":"image","data":%q,"mimeType":"image/png"}],"isError":true}`, image))
	text, err = wrapped.Execute(tools.WithImageSink(t.Context(), collector), json.RawMessage(`{}`))
	if err == nil || text != "" || len(collector.Drain()) != 0 {
		t.Fatalf("failed call injected an image: text=%q err=%v", text, err)
	}
	ft.responses["tools/call"] = json.RawMessage(fmt.Sprintf(`{"content":[],"structuredContent":{"value":%q}}`, strings.Repeat("a", tools.MaxOutputBytes+1)))
	text, err = wrapped.Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	truncated := tools.TruncateOutputWithOptions(text, t.TempDir(), tools.TruncateOptions{})
	if !truncated.Truncated || truncated.SavedPath == "" {
		t.Fatalf("missing artifact: %+v", truncated)
	}
	saved, err := os.ReadFile(truncated.SavedPath)
	if err != nil || string(saved) != text {
		t.Fatalf("artifact did not preserve full result: %v", err)
	}
}
