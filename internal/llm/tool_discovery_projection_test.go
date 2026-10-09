package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

type discoveryRequestProvider struct {
	scriptedProvider
	requests [][]message.Message
}

func (p *discoveryRequestProvider) CompleteStream(ctx context.Context, key, model, system string, messages []message.Message, defs []message.ToolDefinition, maxTokens int, tuning RequestTuning, cb StreamCallback) (*message.Response, error) {
	p.requests = append(p.requests, messages)
	return p.scriptedProvider.CompleteStream(ctx, key, model, system, messages, defs, maxTokens, tuning, cb)
}

func TestToolDiscoveryProjectionRunsAfterFallbackBoundary(t *testing.T) {
	history := func(id string) []message.Message {
		result, err := json.Marshal(message.ToolDiscoveryResult{Tools: []message.ToolDiscoveryEntry{{Name: "sample_lookup", Status: message.ToolDiscoveryLoaded, Definition: &message.ToolDefinition{Name: "sample_lookup", InputSchema: map[string]any{"type": "object"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		return []message.Message{
			{Role: message.RoleUser, Content: "Find sample records."},
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: id, Name: toolname.ToolSearch, Args: json.RawMessage(`{"query":"sample"}`)}}},
			{Role: message.RoleTool, ToolCallID: id, Content: string(result), ToolStatus: message.ToolStatusSuccess},
		}
	}
	first := &discoveryRequestProvider{calls: []scriptedCall{{err: &APIError{StatusCode: 503}}}}
	second := &discoveryRequestProvider{calls: []scriptedCall{{resp: &message.Response{Content: "Complete.", StopReason: "stop"}}}}
	provider := func(name string) *ProviderConfig {
		return NewProviderConfig(name, config.ProviderConfig{Type: config.ProviderTypeResponses, Models: map[string]config.ModelConfig{"test-model": {}}}, []string{"key"})
	}
	client := NewClient(provider("first"), first, "test-model", 1024, "")
	defer client.Close()
	client.SetFallbackModels([]FallbackModel{{ProviderConfig: provider("second"), ProviderImpl: second, ModelID: "test-model", MaxTokens: 1024}})
	client.SetStreamRetryRounds(1)
	original, replacement := history("call-1"), history("call-2")
	_, err := client.CompleteStreamWithOptions(t.Context(), original, nil, nil, CompleteStreamOptions{
		BeforeFallback: func(_ context.Context, messages []message.Message, _ FallbackModel) ([]message.Message, error) {
			if !strings.Contains(messages[2].Content, `"definition"`) {
				t.Error("fallback received an already projected history")
			}
			return replacement, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, impl := range []*discoveryRequestProvider{first, second} {
		if len(impl.requests) != 1 {
			t.Fatalf("requests=%d", len(impl.requests))
		}
		content := impl.requests[0][2].Content
		if strings.Contains(content, `"definition"`) || !strings.Contains(content, "sample_lookup") {
			t.Fatalf("unexpected projected result: %s", content)
		}
	}
	for _, messages := range [][]message.Message{original, replacement} {
		if !strings.Contains(messages[2].Content, `"definition"`) {
			t.Fatal("canonical discovery schema was changed")
		}
	}
}
