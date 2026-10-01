package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// codeExecutionTool is a config-only hosted tool used by the end-to-end test:
// it has no built-in spec, so the whole path is configuration plus the
// generic catalog and backend.
const codeExecutionTool = "code_execution"

// codeExecutionSSE is a minimal Anthropic stream for a server-side code
// execution: the call's input arrives through input_json_delta, the result
// arrives whole in the *_tool_result block, and the model adds a summary text.
func codeExecutionSSE() string {
	return strings.Join([]string{
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"bash_code_execution","input":{}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"code\":\"print(1)"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"}"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"bash_code_execution_result","stdout":"1\n","stderr":"","return_code":0,"content":[]}}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Ran the code."}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":2}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
}

// TestHostedToolConfigOnlyEndToEnd proves the bridge's promise: a second
// hosted tool is wired entirely through configuration. The test declares an
// Anthropic code_execution entry (catalog plus compat list), points a real
// Anthropic provider at a mock SSE endpoint, and asserts the whole path —
// configuration, declaration placement, hosted capture, local tool result —
// without any tool-specific Go code.
func TestHostedToolConfigOnlyEndToEnd(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprintf("pause=%t", paused), func(t *testing.T) { testHostedToolConfigOnlyEndToEnd(t, paused) })
	}
}

func testHostedToolConfigOnlyEndToEnd(t *testing.T, paused bool) {
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("unmarshal request body: %v", err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		count := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		stream := codeExecutionSSE()
		if paused && count == 1 {
			stream = strings.Replace(stream, `"stop_reason":"end_turn"`, `"stop_reason":"pause_turn","container":{"id":"container-1"}`, 1)
		}
		_, _ = io.WriteString(w, stream)
	}))
	defer srv.Close()

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: srv.URL,
		Models: map[string]config.ModelConfig{
			"claude-sample": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
		Compat: &config.ProviderCompatConfig{HostedTools: new([]string{codeExecutionTool})},
	}, []string{"test-key"})
	impl, err := llm.NewAnthropicProvider(providerCfg, "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}

	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}}
	setHostedTestPool(a, llm.FallbackModel{
		ProviderConfig: providerCfg,
		ProviderImpl:   impl,
		ModelID:        "claude-sample",
		MaxTokens:      4096,
	})

	catalog := tools.ResolveHostedToolCatalog(map[string]config.HostedToolConfig{
		codeExecutionTool: {
			Description: "Run Python code in a sandbox and report its output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"code": map[string]any{"type": "string", "description": "Python source to run."},
				},
				"required": []string{"code"},
			},
			Prompt: "Run this code and report the result:\n{code}",
			Declarations: map[string]config.HostedToolDeclarationConfig{
				config.ProviderTypeMessages: {
					Tool: map[string]any{
						"type": "code_execution_20250825",
						"name": codeExecutionTool,
					},
					Force: map[string]any{"type": "tool", "name": codeExecutionTool},
				},
			},
		},
	})
	tool := tools.NewHostedTool(catalog[codeExecutionTool], newTestHostedBackend(t, a, catalog))
	if tool.Name() != codeExecutionTool || tool.Description() != "Run Python code in a sandbox and report its output." {
		t.Fatalf("tool surface = %q/%q, want the configured name and description", tool.Name(), tool.Description())
	}
	if !tool.IsAvailable() {
		t.Fatal("a config-only tool with an enabled target must be available")
	}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"code":"print(1)"}`))
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if !strings.Contains(out, "Ran the code.") {
		t.Fatalf("tool result = %q, want the sub-request summary", out)
	}
	if !strings.Contains(out, "print(1)") || !strings.Contains(out, "stdout") {
		t.Fatalf("tool result = %q, want the captured input and result payloads", out)
	}

	mu.Lock()
	captured := append([]map[string]any(nil), bodies...)
	mu.Unlock()
	want := 1
	if paused {
		want = 2
	}
	if len(captured) != want {
		t.Fatalf("sub-request bodies = %d, want %d", len(captured), want)
	}
	if paused {
		resumed := captured[1]
		messages, ok := resumed["messages"].([]any)
		if !ok || len(messages) != 2 || resumed["container"] != "container-1" {
			t.Fatalf("resumed request = %+v", resumed)
		}
		assistant := messages[1].(map[string]any)
		content := assistant["content"].([]any)
		if assistant["role"] != "assistant" || len(content) != 3 || content[0].(map[string]any)["type"] != "server_tool_use" || content[1].(map[string]any)["type"] != "bash_code_execution_tool_result" || content[2].(map[string]any)["text"] != "Ran the code." {
			t.Fatalf("resumed content = %+v", assistant)
		}
	}
	body := captured[0]
	toolsRaw, _ := body["tools"].([]any)
	if len(toolsRaw) != 1 {
		t.Fatalf("declared tools = %v, want the configured declaration only", body["tools"])
	}
	decl, _ := toolsRaw[0].(map[string]any)
	if decl["type"] != "code_execution_20250825" || decl["name"] != codeExecutionTool {
		t.Fatalf("declaration = %v, want the configured code execution type", decl)
	}
	choice, _ := body["tool_choice"].(map[string]any)
	if choice["type"] != "tool" || choice["name"] != codeExecutionTool {
		t.Fatalf("tool_choice = %v, want the configured forced declaration", body["tool_choice"])
	}
	messagesRaw, _ := body["messages"].([]any)
	if len(messagesRaw) != 1 {
		t.Fatalf("sub-request messages = %v, want a single prompt", body["messages"])
	}
	messageRaw, _ := messagesRaw[0].(map[string]any)
	if messageRaw["role"] != "user" || messageRaw["content"] != "Run this code and report the result:\nprint(1)" {
		t.Fatalf("sub-request message = %v, want the rendered prompt", messagesRaw[0])
	}
}
