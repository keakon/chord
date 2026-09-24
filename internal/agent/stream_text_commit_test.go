package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// commitReleaseProvider streams damaged text deltas (as a relay that cuts
// multi-byte characters would produce them) and then returns a preset response
// whose clean terminal text supersedes the accumulation.
type commitReleaseProvider struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	response  *message.Response
}

func (p *commitReleaseProvider) CompleteStream(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	_ []message.Message,
	_ []message.ToolDefinition,
	_ int,
	_ llm.RequestTuning,
	cb llm.StreamCallback,
) (*message.Response, error) {
	p.startOnce.Do(func() { close(p.started) })
	if cb != nil {
		cb(message.StreamDelta{Type: message.StreamDeltaText, Text: "damaged \ufffd delta"})
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return p.response, nil
	}
}

func (p *commitReleaseProvider) Complete(
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

// TestMainAgentEmitsStreamTextCommitBeforeSegmentEnd pins the full event
// sequence of a successful main-agent request: the deltas stream, the sanitized
// final text is published as StreamTextCommitEvent with the segment identity,
// and the segment end reports last.
func TestMainAgentEmitsStreamTextCommitBeforeSegmentEnd(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	providerCfg := llm.NewProviderConfig("sample/test-provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	provider := &commitReleaseProvider{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		response: &message.Response{Content: "clean terminal text", StopReason: "stop"},
	}
	a.swapLLMClientWithRef(llm.NewClient(providerCfg, provider, "test-model", 4096, "sys"), "test-model", 128000, "sample/test-provider/test-model")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	a.newTurn()
	turnID := a.turn.ID
	a.spawnMainLLMResponseGoroutine(a.turn.Ctx, turnID, []message.Message{{Role: "user", Content: "hello"}}, "")
	<-provider.started
	close(provider.release)
	a.outputWg.Wait()

	sawCommit := false
	sawEnd := false
	var streamedText string
	for _, evt := range drainAgentEvents(a.Events()) {
		switch evt := evt.(type) {
		case StreamTextEvent:
			if sawEnd {
				t.Fatalf("text delta %q emitted after StreamSegmentEndedEvent", evt.Text)
			}
			if evt.TurnID != turnID || evt.RequestSeq == 0 {
				t.Fatalf("text delta identity = (turn %d, request %d), want turn %d and a request sequence", evt.TurnID, evt.RequestSeq, turnID)
			}
			streamedText += evt.Text
		case StreamTextCommitEvent:
			if sawEnd {
				t.Fatal("StreamTextCommitEvent emitted after StreamSegmentEndedEvent")
			}
			if evt.Text != "clean terminal text" {
				t.Fatalf("commit text = %q, want the sanitized final content", evt.Text)
			}
			if evt.AgentID != "" {
				t.Fatalf("commit agent = %q, want the main agent's empty ID", evt.AgentID)
			}
			if evt.TurnID != turnID || evt.RequestSeq == 0 {
				t.Fatalf("commit identity = (turn %d, request %d), want turn %d and a request sequence", evt.TurnID, evt.RequestSeq, turnID)
			}
			sawCommit = true
		case StreamSegmentEndedEvent:
			if !sawCommit {
				t.Fatal("StreamSegmentEndedEvent must not precede StreamTextCommitEvent")
			}
			sawEnd = true
		}
	}
	if !sawEnd {
		t.Fatal("StreamSegmentEndedEvent missing")
	}
	if !sawCommit {
		t.Fatal("StreamTextCommitEvent missing")
	}
	if streamedText != "damaged \ufffd delta" {
		t.Fatalf("streamed text = %q, want the damaged delta accumulation", streamedText)
	}
}

// Even empty successful content is authoritative and retracts prior deltas.
func TestMainAgentCommitsEmptyContent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	providerCfg := llm.NewProviderConfig("sample/test-provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	provider := &commitReleaseProvider{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		response: &message.Response{StopReason: "tool_calls", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "Read", Args: []byte(`{"path":"a.txt"}`)}}},
	}
	a.swapLLMClientWithRef(llm.NewClient(providerCfg, provider, "test-model", 4096, "sys"), "test-model", 128000, "sample/test-provider/test-model")
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	a.newTurn()
	turnID := a.turn.ID
	a.spawnMainLLMResponseGoroutine(a.turn.Ctx, turnID, []message.Message{{Role: "user", Content: "hello"}}, "")
	<-provider.started
	// A tool-only round: no content, a single tool call. This is the shape that
	// must still retract any streamed text via an empty commit.
	close(provider.release)
	a.outputWg.Wait()

	found := false
	for _, evt := range drainAgentEvents(a.Events()) {
		if commit, ok := evt.(StreamTextCommitEvent); ok {
			found = true
			if commit.Text != "" {
				t.Fatalf("commit = %q, want empty", commit.Text)
			}
		}
	}
	if !found {
		t.Fatal("empty final content must be committed")
	}

}

// A focused SubAgent streams through the same damaged-delta shape and must
// publish its sanitized final text with the SubAgent identity before the
// segment ends, mirroring the main agent path.
func TestSubAgentEmitsStreamTextCommitBeforeSegmentEnd(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	providerCfg := llm.NewProviderConfig("sample/test-provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{
		streams: []message.StreamDelta{{Type: message.StreamDeltaText, Text: "damaged \ufffd delta"}},
		resp:    &message.Response{Content: "clean terminal text", StopReason: "stop"},
	}}}
	sub.llmMu.Lock()
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")
	sub.llmMu.Unlock()
	parent.focusedAgent.Store(sub)

	sub.asyncCallLLMWithFlightMarked(sub.turn, sub.ctxMgr.Snapshot())
	waitForSubAgentLLMResult(t, sub, 2*time.Second)
	// The request goroutine reports its segment end in a deferred call, after
	// it queues the result the wait above consumed: llmWG.Wait is the barrier
	// that proves the segment end reached the output channel before the drain.
	sub.llmWG.Wait()

	sawCommit := false
	sawEnd := false
	var streamedText string
	for _, evt := range drainAgentEvents(parent.Events()) {
		switch evt := evt.(type) {
		case StreamTextEvent:
			if sawEnd {
				t.Fatalf("text delta %q emitted after StreamSegmentEndedEvent", evt.Text)
			}
			streamedText += evt.Text
		case StreamTextCommitEvent:
			if sawEnd {
				t.Fatal("StreamTextCommitEvent emitted after StreamSegmentEndedEvent")
			}
			if evt.Text != "clean terminal text" {
				t.Fatalf("commit text = %q, want the sanitized final content", evt.Text)
			}
			if evt.AgentID != sub.instanceID {
				t.Fatalf("commit agent = %q, want %q", evt.AgentID, sub.instanceID)
			}
			if evt.TurnID != sub.turn.ID || evt.RequestSeq == 0 {
				t.Fatalf("commit identity = (turn %d, request %d), want turn %d and a request sequence", evt.TurnID, evt.RequestSeq, sub.turn.ID)
			}
			sawCommit = true
		case StreamSegmentEndedEvent:
			if evt.AgentID == sub.instanceID && !sawCommit {
				t.Fatal("StreamSegmentEndedEvent must not precede StreamTextCommitEvent")
			}
			if evt.AgentID == sub.instanceID {
				sawEnd = true
			}
		}
	}
	if !sawEnd {
		t.Fatal("StreamSegmentEndedEvent missing")
	}
	if !sawCommit {
		t.Fatal("StreamTextCommitEvent missing")
	}
	if streamedText != "damaged \ufffd delta" {
		t.Fatalf("streamed text = %q, want the damaged delta accumulation", streamedText)
	}
}

// An unfocused SubAgent is gated out of the TUI output path: the commit is
// dropped alongside the deltas, and the run loop must not stall.
func TestSubAgentStreamTextCommitGatedWhenUnfocused(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	providerCfg := llm.NewProviderConfig("sample/test-provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{
		resp: &message.Response{Content: "clean terminal text", StopReason: "stop"},
	}}}
	sub.llmMu.Lock()
	sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")
	sub.llmMu.Unlock()

	sub.asyncCallLLMWithFlightMarked(sub.turn, sub.ctxMgr.Snapshot())
	waitForSubAgentLLMResult(t, sub, 2*time.Second)

	for _, evt := range drainAgentEvents(parent.Events()) {
		if _, ok := evt.(StreamTextCommitEvent); ok {
			t.Fatalf("StreamTextCommitEvent must be gated out for an unfocused SubAgent: %+v", evt)
		}
	}
}

func TestSubAgentCommitsEmptyFinalText(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	cfg := llm.NewProviderConfig("sample/test-provider", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"test-key"})
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{
		streams: []message.StreamDelta{{Type: message.StreamDeltaText, Text: "retracted"}},
		resp:    &message.Response{StopReason: "tool_calls", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "Read", Args: []byte(`{"path":"a.txt"}`)}}},
	}}}
	sub.llmMu.Lock()
	sub.llmClient = llm.NewClient(cfg, provider, "model", 1024, "sys")
	sub.llmMu.Unlock()
	parent.focusedAgent.Store(sub)
	sub.asyncCallLLMWithFlightMarked(sub.turn, sub.ctxMgr.Snapshot())
	waitForSubAgentLLMResult(t, sub, 2*time.Second)
	found := false
	for _, evt := range drainAgentEvents(parent.Events()) {
		switch evt := evt.(type) {
		case StreamTextCommitEvent:
			if evt.Text != "" || evt.AgentID != sub.instanceID {
				t.Fatalf("unexpected commit: %+v", evt)
			}
			found = true
		case StreamSegmentEndedEvent:
			if !found {
				t.Fatal("empty commit missing before segment end")
			}
		}
	}
	if !found {
		t.Fatal("empty final text must retract streamed text")
	}
}
