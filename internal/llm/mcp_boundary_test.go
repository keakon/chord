package llm

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

type boundaryToolsProvider struct {
	defs []message.ToolDefinition
}

func (p *boundaryToolsProvider) CompleteStream(_ context.Context, _, _, _ string, _ []message.Message, defs []message.ToolDefinition, _ int, _ RequestTuning, _ StreamCallback) (*message.Response, error) {
	p.defs = slices.Clone(defs)
	return &message.Response{Content: "ok"}, nil
}

func TestMCPBoundaryRefreshesEffectiveDefinitions(t *testing.T) {
	for _, retryRound := range []bool{false, true} {
		name := "fallback"
		if retryRound {
			name = "retry round"
		}
		t.Run(name, func(t *testing.T) {
			primary := mcpDynamicProviderConfig(config.ProviderTypeChatCompletions, "model-1", true, false)
			fallback := mcpDynamicProviderConfig(config.ProviderTypeChatCompletions, "model-2", false, false)
			primaryImpl := &constantErrProvider{err: &APIError{StatusCode: 400, Message: "invalid_request_error: model unavailable"}}
			fallbackImpl := &boundaryToolsProvider{}
			client := NewClient(primary, primaryImpl, "model-1", 512, "")
			client.SetModelPool([]FallbackModel{
				{ProviderConfig: primary, ProviderImpl: primaryImpl, ModelID: "model-1", MaxTokens: 512, ContextLimit: 128000},
				{ProviderConfig: fallback, ProviderImpl: fallbackImpl, ModelID: "model-2", MaxTokens: 512, ContextLimit: 128000},
			}, 0)
			oldDef := message.ToolDefinition{Name: "mcp_sample_old", InputSchema: map[string]any{"type": "object"}}
			newDef := message.ToolDefinition{Name: "mcp_sample_lookup", InputSchema: map[string]any{"type": "object"}}
			options := CompleteStreamOptions{MCPDeclarations: []message.ToolDefinition{oldDef}}
			boundaryRan := false
			update := func(messages []message.Message) *RequestSnapshot {
				boundaryRan = true
				return &RequestSnapshot{
					Messages:        append(messages, message.NewSystemToolsMessage([]message.ToolDefinition{newDef})),
					MCPDeclarations: []message.ToolDefinition{newDef},
				}
			}
			if retryRound {
				client = NewClient(fallback, fallbackImpl, "model-2", 512, "")
				fallback.MarkCooldown("test-key", 20*time.Millisecond)
				options.BeforeRetryRound = func(_ context.Context, messages []message.Message) (*RequestSnapshot, error) {
					return update(messages), nil
				}
			} else {
				options.BeforeFallback = func(_ context.Context, messages []message.Message, _ FallbackModel) (*RequestSnapshot, error) {
					return update(messages), nil
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := client.CompleteStreamWithOptions(ctx, []message.Message{{Role: message.RoleUser, Content: "first"}}, nil, nil, options)
			if err != nil {
				t.Fatal(err)
			}
			if !boundaryRan || len(fallbackImpl.defs) != 1 || fallbackImpl.defs[0].Name != newDef.Name {
				t.Fatalf("boundary ran=%v, effective definitions=%#v", boundaryRan, fallbackImpl.defs)
			}
		})
	}
}
