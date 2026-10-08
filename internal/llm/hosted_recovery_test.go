package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestHostedToolsExplicitEmptyOverride(t *testing.T) {
	p := NewProviderConfig("provider", config.ProviderConfig{Compat: &config.ProviderCompatConfig{HostedTools: new([]string{"sample_tool"})}, Models: map[string]config.ModelConfig{"disabled": {Compat: &config.ModelCompatConfig{HostedTools: new([]string{})}}, "inherited": {}}}, nil)
	if len(p.HostedToolsCompat("disabled")) != 0 {
		t.Fatal("explicit empty list must disable inherited tools")
	}
	list := p.HostedToolsCompat("inherited")
	if len(list) != 1 {
		t.Fatal("missing inherited list")
	}
	list[0] = "changed"
	if p.HostedToolsCompat("inherited")[0] != "sample_tool" {
		t.Fatal("snapshot aliases configuration")
	}
}

func TestResponsesHostedErrorsApprovalAndFiles(t *testing.T) {
	for _, status := range []string{"", ",\"status\":\"completed\""} {
		stream := buildSSEStream([]string{`{"type":"response.completed","response":{"status":"completed","output":[{"type":"mcp_call","id":"mcp-1","name":"remote_action","arguments":"{}","error":{"message":"failed"}` + status + `},{"type":"message","id":"msg-1","content":[{"type":"output_text","text":"File ready","annotations":[{"type":"container_file_citation","container_id":"container-1","file_id":"file-1","filename":"result.csv"}]}]},{"type":"mcp_approval_request","id":"approval-1"},{"type":"sample_native_output","id":"native-1","data":"retained"}]}}`})
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, true, false)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Hosted == nil || resp.Hosted.HasCompleteResult() || len(resp.Hosted.Calls) != 1 || resp.Hosted.Calls[0].Error == "" || !resp.Hosted.RequiresApproval || len(resp.Hosted.Items) != 4 {
			t.Fatalf("observation = %+v", resp.Hosted)
		}
		raw, _ := json.Marshal(resp.Hosted.Items)
		if !strings.Contains(string(raw), "result.csv") || !strings.Contains(string(raw), "retained") {
			t.Fatalf("items = %s", raw)
		}
	}
}

func TestAnthropicHostedNativeContinuationCapture(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		`event: message_start`, `data: {"type":"message_start","message":{"container":{"id":"container-1"}}}`, "",
		`event: content_block_start`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, "",
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Sample"}}`, "",
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.invalid/a","title":"A","cited_text":"Sample","encrypted_index":"opaque"}}}`, "",
		`event: content_block_stop`, `data: {"type":"content_block_stop","index":0}`, "",
		`event: content_block_start`, `data: {"type":"content_block_start","index":1,"content_block":{"type":"sample_native_block","opaque":"retained"}}`, "",
		`event: content_block_stop`, `data: {"type":"content_block_stop","index":1}`, "",
		`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"pause_turn"}}`, "",
		`event: message_stop`, `data: {"type":"message_stop"}`, "",
	}, "\n"))
	resp, err := parseSSEStream(stream, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Hosted == nil || resp.Hosted.Container != "container-1" || len(resp.Hosted.Items) != 2 || !strings.Contains(string(resp.Hosted.Items[0]), "opaque") || !strings.Contains(string(resp.Hosted.Items[1]), "retained") {
		t.Fatalf("observation = %+v", resp.Hosted)
	}
}

func TestResponsesHostedTerminalOrderAndAuthoritativeError(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg-1","content":[]}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"mcp_call","id":"call-1","status":"completed","error":null}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"mcp_call","id":"call-1","error":{"message":"failed"}},{"type":"message","id":"msg-1","content":[]}]}}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Hosted.HasCompleteResult() || resp.Hosted.Calls[0].Error == "" || len(resp.Hosted.Items) != 2 || !strings.Contains(string(resp.Hosted.Items[0]), "call-1") {
		t.Fatalf("observation = %+v", resp.Hosted)
	}
}

func TestHostedParsersRetainEvidenceOnStreamFailure(t *testing.T) {
	anthropic := strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":17}}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"failed\"}}\n\n")
	resp, err := parseSSEStream(anthropic, nil, nil, true)
	if err == nil || resp == nil || resp.Usage == nil || resp.Usage.InputTokens != 17 {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	stream := buildSSEStream([]string{`{"type":"response.output_item.added","output_index":0,"item":{"type":"mcp_approval_request","id":"approval-1"}}`, `{"type":"error","message":"connection interrupted"}`})
	resp, _, err = parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, true, false)
	if err == nil || resp == nil || resp.Hosted == nil || !resp.Hosted.RequiresApproval {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
}
