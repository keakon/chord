package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestResponsesCacheBreakpointsPreserveDurableWirePrefix(t *testing.T) {
	for _, freeform := range []bool{false, true} {
		msgs := []message.Message{
			{Role: message.RoleUser, Content: "request"},
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-a", Name: "apply_patch", Args: json.RawMessage(`{"patch":"sample"}`)}}},
			{Role: message.RoleTool, ToolCallID: "call-a", Content: "result"},
		}
		first := convertMessagesToResponsesWithItemIDs("", append(append([]message.Message(nil), msgs...), message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "hint"}), false, freeform, true)
		secondMsgs := append(append([]message.Message(nil), msgs...), message.Message{Role: message.RoleUser, Content: "follow-up"}, message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "another hint"})
		second := convertMessagesToResponsesWithItemIDs("", secondMsgs, false, freeform, true)
		if !reflect.DeepEqual(first[:3], second[:3]) {
			t.Fatal("durable wire prefix changed when history grew")
		}
		for _, idx := range []int{0, 2} {
			content := first[idx].Content
			if idx == 2 {
				content = first[idx].Output
			}
			blocks, ok := content.([]responsesContentBlock)
			if !ok || blocks[len(blocks)-1].PromptCacheBreakpoint == nil {
				t.Fatalf("missing durable breakpoint: %#v", first[idx])
			}
		}
		if blocks := first[3].Content.([]responsesContentBlock); blocks[0].PromptCacheBreakpoint != nil {
			t.Fatal("transient overlay received a breakpoint")
		}
		oldWire, _ := json.Marshal(convertMessagesToResponsesWithItemIDs("", msgs, false, freeform, false))
		if strings.Contains(string(oldWire), "prompt_cache_breakpoint") {
			t.Fatal("unsupported target received cache markers")
		}
	}
}

func TestMarkResponsesCacheTextPreservesOpaqueBlocksAndOwnership(t *testing.T) {
	source := []responsesContentBlock{{Type: "input_text", Text: "reference"}, {Type: "input_image", ImageURL: "https://example.invalid/image.png"}}
	marked := markResponsesCacheText(source).([]responsesContentBlock)
	if marked[0].PromptCacheBreakpoint == nil || marked[1].PromptCacheBreakpoint != nil || source[0].PromptCacheBreakpoint != nil {
		t.Fatal("marker placement or ownership violated")
	}
	output := []responsesContentBlock{{Type: "output_text", Text: "assistant response"}}
	if got := markResponsesCacheText(output).([]responsesContentBlock); got[0].PromptCacheBreakpoint != nil {
		t.Fatal("output_text was marked")
	}
}

func TestResponsesProviderCacheCapabilityOnHTTPWire(t *testing.T) {
	var request responsesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	cfg := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: server.URL + "/responses"}, []string{"test-key"})
	provider := &ResponsesProvider{provider: cfg, client: server.Client()}
	for _, model := range []string{"gpt-6.1-sol", "gpt-5.5", "sample/test-model"} {
		request = responsesRequest{}
		_, err := provider.CompleteStream(context.Background(), "test-key", model, "instructions", []message.Message{{Role: message.RoleUser, Content: "request"}, {Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "hint"}}, nil, 0, RequestTuning{}, func(message.StreamDelta) {})
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(request)
		supported := model == "gpt-6.1-sol"
		if strings.Contains(string(wire), "prompt_cache_breakpoint") != supported || (request.PromptCacheOptions != nil) != supported {
			t.Fatalf("wrong capability gating for %s: %s", model, wire)
		}
	}
}

func TestResponsesCacheBreakpointsSkipPrefixAndTailOverlays(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "file snapshot"},
		{Role: message.RoleUser, Content: "request"},
		{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "hint"},
	}
	items := convertMessagesToResponsesWithItemIDs("", msgs, false, false, true)
	for i, item := range items {
		marked := item.Content.([]responsesContentBlock)[0].PromptCacheBreakpoint != nil
		if marked != (i == 1) {
			t.Fatalf("wrong boundary at source message %d", i)
		}
	}
}

func TestResponsesCacheModeAvoidsTransientWrite(t *testing.T) {
	durable := []message.Message{{Role: message.RoleUser, Content: "request"}}
	tail := append(append([]message.Message(nil), durable...), message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: "hint"})
	for _, tc := range []struct {
		msgs    []message.Message
		enabled bool
		want    string
	}{{durable, true, "implicit"}, {tail, true, "explicit"}, {tail, false, "implicit"}} {
		input := convertMessagesToResponsesWithItemIDs("", tc.msgs, false, false, tc.enabled)
		if got := responsesPromptCacheOptions(tc.msgs, input).Mode; got != tc.want {
			t.Fatalf("cache mode=%s want=%s", got, tc.want)
		}
	}
}

func TestResponsesProviderMetadataIdentityOnWire(t *testing.T) {
	var seen []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		seen = append(seen, request.ClientMetadata)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	cfg := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeResponses, APIURL: server.URL + "/responses"}, []string{"test-key"})
	provider := &ResponsesProvider{provider: cfg, client: server.Client()}
	first := NewResponsesTurnState()
	for _, state := range []*ResponsesTurnState{first, first, NewResponsesTurnState()} {
		_, err := provider.CompleteStream(WithResponsesTurnState(context.Background(), state), "test-key", "gpt-6.1-sol", "", []message.Message{{Role: message.RoleUser, Content: "request"}}, nil, 0, RequestTuning{SessionKey: "session-a"}, func(message.StreamDelta) {})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 3 || !reflect.DeepEqual(seen[0], seen[1]) {
		t.Fatalf("turn metadata not reused on wire: %v", seen)
	}
	if seen[0][responsesClientMetadataTurnID] == seen[2][responsesClientMetadataTurnID] {
		t.Fatal("wire metadata reused across turns")
	}
}
