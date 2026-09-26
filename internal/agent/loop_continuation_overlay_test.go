package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// requestCaptureProvider records the messages of every request and answers
// each one with a plain terminal stop.
type requestCaptureProvider struct {
	mu       sync.Mutex
	requests [][]message.Message
}

func (p *requestCaptureProvider) CompleteStream(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	messages []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	_ llm.StreamCallback,
) (*message.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, append([]message.Message(nil), messages...))
	p.mu.Unlock()
	return &message.Response{Content: "ok", StopReason: "stop"}, nil
}

func (p *requestCaptureProvider) Complete(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning llm.RequestTuning,
) (*message.Response, error) {
	return p.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, nil)
}

func (p *requestCaptureProvider) requestText(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var sb strings.Builder
	for _, msg := range p.requests[i] {
		sb.WriteString(msg.Content)
		for _, part := range msg.Parts {
			sb.WriteString(part.Text)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// A rejected done keeps its reason on the tool result and hands the
// continuation note to the next request as a one-shot overlay; the request
// goroutine must still see the note when it assembles that overlay.
func TestPendingLoopContinuationReachesNextRequestOnce(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	providerCfg := llm.NewProviderConfig("sample/test-provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	provider := &requestCaptureProvider{}
	a.swapLLMClientWithRef(llm.NewClient(providerCfg, provider, "test-model", 4096, "sys"), "test-model", 128000, "sample/test-provider/test-model")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	a.setPendingLoopContinuation(&LoopContinuationNote{Title: "LOOP CONTINUE", Text: "Open TODO items: sample-marker"})
	a.newTurn()
	history := []message.Message{{Role: "user", Content: "hello"}}
	a.spawnMainLLMResponseGoroutine(a.turn.Ctx, a.turn.ID, history, "")
	a.outputWg.Wait()
	a.spawnMainLLMResponseGoroutine(a.turn.Ctx, a.turn.ID, history, "")
	a.outputWg.Wait()

	if got := len(provider.requests); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if first := provider.requestText(0); !strings.Contains(first, "## LOOP CONTINUE\n\nOpen TODO items: sample-marker") {
		t.Fatalf("first request lacks the continuation overlay:\n%s", first)
	}
	if second := provider.requestText(1); strings.Contains(second, "sample-marker") {
		t.Fatalf("continuation overlay must be consumed by one request:\n%s", second)
	}
}
